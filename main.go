package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

// ── Data types ───────────────────────────────────────────────

type ProviderUsage struct {
	Name       string   `json:"name"`
	Metrics    []Metric `json:"metrics"`
	ResetIn    string   `json:"resetIn"`
	ResetEpoch int64    `json:"resetEpoch"`
}

type Metric struct {
	Label string  `json:"label"`
	Used  float64 `json:"used"`
	Max   float64 `json:"max"`
	Pct   float64 `json:"pct"`
}

type AllUsage struct {
	UpdatedAt string          `json:"updatedAt"`
	Providers []ProviderUsage `json:"providers"`
}

// ── Wails events ────────────────────────────────────────────

func init() {
	application.RegisterEvent[AllUsage]("usage")
}

// ── TokenWatch service (bound to frontend) ─────────────────

type TokenWatch struct{}

type AppConfig struct {
	ZaiToken      string `json:"zaiToken"`
	ClaudeSession string `json:"claudeSession"`
}

func getConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tokenwatch.json")
}

func (t *TokenWatch) SaveSettings(zaiToken, claudeSession string) error {
	// Verify tmux session exists if provided
	if claudeSession != "" {
		cmd := exec.Command("tmux", "has-session", "-t", claudeSession)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("tmux session '%s' not found. Please run: tmux new -s %s", claudeSession, claudeSession)
		}
	}

	cfg := AppConfig{ZaiToken: zaiToken, ClaudeSession: claudeSession}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	err = os.WriteFile(getConfigPath(), data, 0644)
	if err == nil {
		go refreshUsage()
	}
	return err
}

func (t *TokenWatch) GetSettings() AppConfig {
	data, err := os.ReadFile(getConfigPath())
	if err != nil {
		return AppConfig{}
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AppConfig{}
	}
	return cfg
}

func (t *TokenWatch) QuitApp() {
	os.Exit(0)
}

var cachedUsage AllUsage
var appWindow *application.WebviewWindow

func refreshUsage() {
	var providers []ProviderUsage
	var hasClaude bool

	if claude := fetchClaudeUsage(); claude != nil {
		providers = append(providers, *claude)
		hasClaude = true
	}
	if oc := fetchOpenCodeUsage(); oc != nil {
		providers = append(providers, *oc)
	}
	if zai := fetchZaiUsage(); zai != nil {
		providers = append(providers, *zai)
	}

	// If Claude session is configured but no data yet, show loading placeholder
	cfg := AppConfig{}
	if data, err := os.ReadFile(getConfigPath()); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg.ClaudeSession != "" && !hasClaude {
		providers = append([]ProviderUsage{{
			Name:    "Claude",
			Metrics: []Metric{{Label: "Session", Pct: -1}},
			ResetIn: "loading…",
		}}, providers...)
	}

	if len(providers) > 0 {
		cachedUsage = AllUsage{
			UpdatedAt: time.Now().Format("15:04:05"),
			Providers: providers,
		}
	}

	// Push updated cache to frontend
	if appWindow != nil {
		appWindow.EmitEvent("usage", cachedUsage)
	}
}

func (t *TokenWatch) FetchUsage() AllUsage {
	go refreshUsage()
	return cachedUsage
}

func (t *TokenWatch) StartPolling() {
	go refreshUsage()
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			refreshUsage()
		}
	}()
}

// ── Claude (tmux /usage) ──────────────────────────────────

func fetchClaudeUsage() *ProviderUsage {
	sessionName := (&TokenWatch{}).GetSettings().ClaudeSession
	if sessionName == "" {
		return nil
	}

	// Verify the session is alive
	if err := exec.Command("tmux", "has-session", "-t", sessionName).Run(); err != nil {
		return &ProviderUsage{
			Name: "Claude",
			Metrics: []Metric{{
				Label: "Status",
				Pct:   0,
			}},
			ResetIn: "session dead/inactive",
		}
	}

	// 1. Send Escape to ensure clean prompt state
	exec.Command("tmux", "send-keys", "-t", sessionName, "Escape").Run()
	time.Sleep(100 * time.Millisecond)

	// Dismiss satisfaction survey if present
	out, _ := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p").Output()
	if strings.Contains(string(out), "How is Claude") {
		exec.Command("tmux", "send-keys", "-t", sessionName, "0").Run()
		time.Sleep(500 * time.Millisecond)
		exec.Command("tmux", "send-keys", "-t", sessionName, "Escape").Run()
		time.Sleep(100 * time.Millisecond)
	}

	// 2. Clear visible screen AND scrollback history
	exec.Command("tmux", "send-keys", "-t", sessionName, "C-l").Run()
	time.Sleep(200 * time.Millisecond)
	exec.Command("tmux", "clear-history", "-t", sessionName).Run()

	// 3. Send /usage
	exec.Command("tmux", "send-keys", "-t", sessionName, "/usage", "Enter").Run()

	// 4. Poll until we see "% used" or timeout after 10s
	var output string
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		out, err := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p").Output()
		if err != nil {
			continue
		}
		output = string(out)
		if strings.Contains(output, "% used") {
			break
		}

		// If dialog was dismissed (no loading, no data), bail out
		if !strings.Contains(output, "Loading") && i > 1 {
			break
		}
	}

	// 5. Send Escape to close the dialog and keep the prompt clean for next iteration
	exec.Command("tmux", "send-keys", "-t", sessionName, "Escape").Run()

	// 6. If no percentage data found, return cache as-is
	if !strings.Contains(output, "% used") {
		return nil
	}

	if output == "" {
		return nil
	}
	return parseClaudeUsageOutput(output)
}

var (
	rePctUsed  = regexp.MustCompile(`(\d+)%\s+used`)
	reResetsIn = regexp.MustCompile(`Resets\s+(.+)`)
)

type usageBlock struct {
	label string
	pct   float64
	reset string
	key   string // "session" or "week"
}

func parseClaudeUsageOutput(output string) *ProviderUsage {
	lines := strings.Split(output, "\n")
	var blocks []usageBlock

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		var labelKey string
		var shortLabel string
		if strings.HasPrefix(trimmed, "Current session") {
			labelKey = "session"
			shortLabel = "Session"
		} else if strings.HasPrefix(trimmed, "Current week") {
			labelKey = "week"
			shortLabel = "Weekly"
		}
		if labelKey == "" {
			continue
		}

		var resetAcc string
		foundPct := false
		for j := i + 1; j < i+8 && j < len(lines); j++ {
			nextLine := strings.TrimSpace(lines[j])
			if nextLine == "" {
				continue
			}

			if m := rePctUsed.FindStringSubmatch(nextLine); m != nil {
				var pct float64
				fmt.Sscanf(m[1], "%f", &pct)
				// Remove previous duplicate for this label
				for k := len(blocks) - 1; k >= 0; k-- {
					if blocks[k].key == labelKey {
						blocks = append(blocks[:k], blocks[k+1:]...)
					}
				}
				blocks = append(blocks, usageBlock{label: shortLabel, pct: pct, key: labelKey, reset: resetAcc})
				foundPct = true
				continue
			}

			// After finding % used, keep scanning for reset time
			if foundPct {
				if m := reResetsIn.FindStringSubmatch(nextLine); m != nil {
					blocks[len(blocks)-1].reset = strings.TrimSpace(m[1])
					break
				}
				if strings.HasPrefix(nextLine, "Current session") || strings.HasPrefix(nextLine, "Current week") {
					break
				}
			} else {
				if m := reResetsIn.FindStringSubmatch(nextLine); m != nil {
					resetAcc += " " + strings.TrimSpace(m[1])
				}
			}
		}
	}

	if len(blocks) == 0 {
		return nil
	}

	var metrics []Metric
	var resetStr string

	for _, b := range blocks {
		metrics = append(metrics, Metric{
			Label: b.label,
			Pct:   b.pct,
		})

		if b.reset != "" && resetStr == "" {
			resetStr = b.reset
		}
	}

	return &ProviderUsage{
		Name:    "Claude",
		Metrics: metrics,
		ResetIn: resetStr,
	}
}

// ── OpenCode Go (local SQLite) ────────────────────────────

// OpenCode Go limits (dollar-based)
const (
	ocRollingLimit = 12.0 // 5-hour rolling window
	ocWeeklyLimit  = 30.0 // per week
	ocMonthlyLimit = 60.0 // per month
)

func fetchOpenCodeUsage() *ProviderUsage {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dbPath := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil // no OpenCode DB, skip silently
	}

	now := time.Now()
	fiveHrAgoMs := now.Add(-5 * time.Hour).UnixMilli()
	weekStartMs := time.Date(now.Year(), now.Month(), now.Day()-int(now.Weekday()), 0, 0, 0, 0, now.Location()).UnixMilli()
	monthStartMs := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).UnixMilli()

	// Query rolling + weekly + monthly costs in one pass
	query := fmt.Sprintf(`
		SELECT
			SUM(CASE WHEN json_extract(data, '$.time.completed') > %d THEN json_extract(data, '$.cost') ELSE 0 END),
			SUM(CASE WHEN json_extract(data, '$.time.completed') > %d THEN json_extract(data, '$.cost') ELSE 0 END),
			SUM(CASE WHEN json_extract(data, '$.time.completed') > %d THEN json_extract(data, '$.cost') ELSE 0 END)
		FROM message
		WHERE json_extract(data, '$.providerID') = 'opencode-go'
		  AND json_extract(data, '$.cost') IS NOT NULL
		  AND json_extract(data, '$.role') = 'assistant'
	`, fiveHrAgoMs, weekStartMs, monthStartMs)

	out, err := exec.Command("sqlite3", "-separator", " ", dbPath, query).Output()
	if err != nil {
		log.Printf("opencode: sqlite query failed: %v", err)
		return nil
	}

	var rollingCost, weeklyCost, monthlyCost float64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%f %f %f", &rollingCost, &weeklyCost, &monthlyCost)

	metrics := []Metric{
		{Label: "5h rolling", Pct: rollingCost / ocRollingLimit * 100},
		{Label: "Weekly", Pct: weeklyCost / ocWeeklyLimit * 100},
		{Label: "Monthly", Pct: monthlyCost / ocMonthlyLimit * 100},
	}

	// Reset time: find nearest reset
	// Rolling resets ~5h from first message; weekly resets next Sunday; monthly resets 1st
	var resetStr string
	// Use next Sunday 00:00 as the reset reference
	daysUntilSunday := (7 - int(now.Weekday())) % 7
	if daysUntilSunday == 0 && now.Hour() >= 0 {
		daysUntilSunday = 7 // if already Sunday, next Sunday
	}
	nextSunday := time.Date(now.Year(), now.Month(), now.Day()+daysUntilSunday, 0, 0, 0, 0, now.Location())
	resetStr = formatResetTime(nextSunday.Unix())

	return &ProviderUsage{
		Name:    "OpenCode",
		Metrics: metrics,
		ResetIn: resetStr,
	}
}

// ── Z.ai (API) ──────────────────────────────────────────────

type zaiLimit struct {
	Type          string  `json:"type"`
	Percentage    float64 `json:"percentage"`
	NextResetTime int64   `json:"nextResetTime"`
}

type zaiResponse struct {
	Data struct {
		Limits []zaiLimit `json:"limits"`
	} `json:"data"`
}

func fetchZaiUsage() *ProviderUsage {
	token := os.Getenv("ZAI_TOKEN")
	if token == "" {
		token = (&TokenWatch{}).GetSettings().ZaiToken
	}
	if token == "" {
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://z.ai/api/monitor/usage/quota/limit", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("z.ai: request failed: %v", err)
		return nil
	}
	defer resp.Body.Close()

	var result zaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("z.ai: decode failed: %v", err)
		return nil
	}

	var metrics []Metric
	var resetEpoch int64
	var nearestReset time.Duration

	for _, limit := range result.Data.Limits {
		label := limit.Type
		if limit.Type == "TIME_LIMIT" {
			label = "Queries"
		} else if limit.Type == "TOKENS_LIMIT" {
			label = "Tokens"
		}

		metrics = append(metrics, Metric{Label: label, Pct: limit.Percentage})

		resetTime := time.UnixMilli(limit.NextResetTime).UTC()
		until := time.Until(resetTime)
		if until > 0 && (nearestReset == 0 || until < nearestReset) {
			nearestReset = until
			resetEpoch = limit.NextResetTime / 1000
		}
	}

	if len(metrics) == 0 {
		return nil
	}

	var resetIn string
	if resetEpoch > 0 {
		resetIn = formatResetTime(resetEpoch)
	}

	return &ProviderUsage{
		Name:       "Z.ai",
		Metrics:    metrics,
		ResetIn:    resetIn,
		ResetEpoch: resetEpoch,
	}
}

// ── Helpers ─────────────────────────────────────────────────

func formatResetTime(epoch int64) string {
	if epoch == 0 {
		return ""
	}
	t := time.Unix(epoch, 0).Local()
	now := time.Now().Local()

	// Same day
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("3:04pm")
	}

	// Different day
	return t.Format("Jan _2 at 3:04pm")
}

// ── Main ────────────────────────────────────────────────────

func main() {
	app := application.New(application.Options{
		Name:        "Token Watch",
		Description: "AI token usage monitor",
		Services: []application.Service{
			application.NewService(&TokenWatch{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
	})

	appWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Token Watch",
		Width:     300,
		Height:    400,
		Frameless: true,
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHidden,
			Backdrop: application.MacBackdropTransparent,
		},
		BackgroundColour: application.NewRGB(0, 0, 0),
		Hidden:           true,
		AlwaysOnTop:      true,
		URL:              "/",
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
	})

	// System tray
	tray := app.SystemTray.New()

	// Resolve icon path relative to binary location
	var iconData []byte
	exePath, err := os.Executable()
	if err == nil {
		iconPath := filepath.Join(filepath.Dir(exePath), "..", "build", "appicon.png")
		iconData, err = os.ReadFile(iconPath)
	}
	if err != nil {
		log.Printf("warning: could not load tray icon: %v", err)
	} else {
		tray.SetIcon(iconData)
	}

	tray.AttachWindow(appWindow).WindowOffset(5)

	menu := app.NewMenu()
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	tray.SetMenu(menu)

	err = app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
