package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── Claude Code spend (local logs) ──────────────────────────
//
// Claude Code writes every assistant turn, with its token usage, to JSONL
// transcripts under ~/.claude/projects. Summing those gives the tokens used
// today and over the last 7 days, and what they would have cost at API prices,
// without any network call.

type LocalSpend struct {
	TodayTokens int64   `json:"todayTokens"`
	TodayCost   float64 `json:"todayCost"`
	WeekTokens  int64   `json:"weekTokens"`
	WeekCost    float64 `json:"weekCost"`
	// Unpriced is true when some usage came from a model missing from the
	// price table, so the costs are a lower bound
	Unpriced bool `json:"unpriced,omitempty"`
}

// modelPrice is USD per million tokens. Cache writes are priced from input:
// 1.25x for the 5-minute TTL and 2x for the 1-hour TTL.
type modelPrice struct {
	Input, Output, CacheRead float64
}

// claudePrices maps a model id (without date suffix) to Anthropic's API list
// prices. Haiku 5.5 is billed at its <=100K-prompt rate.
var claudePrices = map[string]modelPrice{
	"claude-fable-5-1":  {10, 50, 0.25},
	"claude-mythos-5-1": {10, 50, 0.25},
	"claude-fable-5":    {10, 50, 1},
	"claude-mythos-5":   {10, 50, 1},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-opus-4-8":   {5, 25, 0.50},
	"claude-opus-4-7":   {5, 25, 0.50},
	"claude-opus-4-6":   {5, 25, 0.50},
	"claude-opus-4-5":   {5, 25, 0.50},
	"claude-opus-4-1":   {15, 75, 1.50},
	"claude-opus-4":     {15, 75, 1.50},
	"claude-sonnet-5-5": {2, 10, 0.20},
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-sonnet-4-6": {3, 15, 0.30},
	"claude-sonnet-4-5": {3, 15, 0.30},
	"claude-sonnet-4":   {3, 15, 0.30},
	"claude-3-7-sonnet": {3, 15, 0.30},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01},
	"claude-haiku-4-5":  {1, 5, 0.10},
	"claude-3-5-haiku":  {0.80, 4, 0.08},
}

// modelSuffix matches what may follow a model id in the logs: a release date
// and/or the 1M-context marker.
var modelSuffix = regexp.MustCompile(`^(-\d{8})?(\[1m\])?$`)

func priceFor(model string) (modelPrice, bool) {
	model = strings.TrimPrefix(model, "anthropic.")
	best := ""
	for id := range claudePrices {
		if len(id) > len(best) && strings.HasPrefix(model, id) && modelSuffix.MatchString(model[len(id):]) {
			best = id
		}
	}
	if best == "" {
		return modelPrice{}, false
	}
	return claudePrices[best], true
}

type ccUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func (u ccUsage) tokens() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

func (u ccUsage) cost(p modelPrice) float64 {
	write5m, write1h := u.CacheCreationInputTokens, int64(0)
	if u.CacheCreation != nil && u.CacheCreation.Ephemeral5m+u.CacheCreation.Ephemeral1h > 0 {
		write5m, write1h = u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
	}
	return (float64(u.InputTokens)*p.Input +
		float64(u.OutputTokens)*p.Output +
		float64(u.CacheReadInputTokens)*p.CacheRead +
		float64(write5m)*p.Input*1.25 +
		float64(write1h)*p.Input*2) / 1e6
}

// spendRecord is one billed assistant message.
type spendRecord struct {
	key      string
	at       time.Time
	tokens   int64
	cost     float64
	unpriced bool
}

// parseTranscript reads the billed assistant messages from one JSONL file.
func parseTranscript(r io.Reader) []spendRecord {
	var records []spendRecord
	br := bufio.NewReaderSize(r, 1<<16)
	for {
		line, err := br.ReadBytes('\n')
		// Most lines are user turns and tool output; skip them before decoding
		if bytes.Contains(line, []byte(`"usage"`)) {
			var entry struct {
				Type      string `json:"type"`
				Timestamp string `json:"timestamp"`
				RequestID string `json:"requestId"`
				Message   struct {
					ID    string   `json:"id"`
					Model string   `json:"model"`
					Usage *ccUsage `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Type == "assistant" && entry.Message.Usage != nil {
				at, terr := time.Parse(time.RFC3339, entry.Timestamp)
				// <synthetic> marks messages Claude Code made up locally, never billed
				if terr == nil && entry.Message.Model != "<synthetic>" {
					u := *entry.Message.Usage
					rec := spendRecord{key: entry.Message.ID + ":" + entry.RequestID, at: at, tokens: u.tokens()}
					if p, ok := priceFor(entry.Message.Model); ok {
						rec.cost = u.cost(p)
					} else {
						rec.unpriced = u.tokens() > 0
					}
					records = append(records, rec)
				}
			}
		}
		if err != nil {
			return records
		}
	}
}

// summarizeSpend totals records for today and the 7 days ending today, in
// local time. A streamed message is logged once per content block with the
// same usage, so each message id + request id is only counted once.
func summarizeSpend(records []spendRecord, now time.Time) LocalSpend {
	now = now.Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekStart := today.AddDate(0, 0, -6)
	seen := map[string]bool{}
	var s LocalSpend
	for _, r := range records {
		if r.at.Before(weekStart) {
			continue
		}
		if r.key != ":" {
			if seen[r.key] {
				continue
			}
			seen[r.key] = true
		}
		s.WeekTokens += r.tokens
		s.WeekCost += r.cost
		if !r.at.Before(today) {
			s.TodayTokens += r.tokens
			s.TodayCost += r.cost
		}
		if r.unpriced {
			s.Unpriced = true
		}
	}
	return s
}

func claudeProjectDirs() []string {
	var dirs []string
	if env := os.Getenv("CLAUDE_CONFIG_DIR"); env != "" {
		for _, d := range strings.Split(env, ",") {
			if d = strings.TrimSpace(d); d != "" {
				dirs = append(dirs, filepath.Join(d, "projects"))
			}
		}
		return dirs
	}
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".config", "claude", "projects"),
	}
}

type transcriptCache struct {
	size    int64
	modTime time.Time
	records []spendRecord
}

// Transcripts only grow, so a file is re-read only when its size or mtime
// changes; the 5-minute poll stays cheap even with large histories.
var transcripts = map[string]transcriptCache{}
var transcriptsMu sync.Mutex

// fetchClaudeCodeSpend returns nil when there are no Claude Code logs to read.
func fetchClaudeCodeSpend() *LocalSpend {
	transcriptsMu.Lock()
	defer transcriptsMu.Unlock()

	now := time.Now()
	// Files untouched since before the 7-day window cannot hold records in it
	cutoff := now.AddDate(0, 0, -8)
	found := false
	live := map[string]bool{}
	var records []spendRecord
	for _, dir := range claudeProjectDirs() {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			found = true
			info, err := d.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				return nil
			}
			live[path] = true
			c, ok := transcripts[path]
			if !ok || c.size != info.Size() || !c.modTime.Equal(info.ModTime()) {
				f, err := os.Open(path)
				if err != nil {
					return nil
				}
				c = transcriptCache{size: info.Size(), modTime: info.ModTime(), records: parseTranscript(f)}
				f.Close()
				transcripts[path] = c
			}
			records = append(records, c.records...)
			return nil
		})
	}
	for path := range transcripts {
		if !live[path] {
			delete(transcripts, path)
		}
	}
	if !found {
		return nil
	}
	s := summarizeSpend(records, now)
	return &s
}
