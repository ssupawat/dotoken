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

	// 2. Clear scrollback history to prevent stale duplicates
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
		// Return nil so refreshUsage() keeps the existing cache
		return nil
	}

	if output == "" {
		return nil
	}
	return parseClaudeUsageOutput(output)
}

func tmuxCapture(session string) string {
	out, err := exec.Command("tmux", "capture-pane", "-t", session, "-p").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

var (
	rePctUsed  = regexp.MustCompile(`(\d+)%\s+used`)
	reResetsIn = regexp.MustCompile(`Resets\s+(.+)`)
)

type usageBlock struct {
	label string
	pct   float64
	reset string
}

func parseClaudeUsageOutput(output string) *ProviderUsage {
	lines := strings.Split(output, "\n")
	var blocks []usageBlock

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "Current session") ||
			strings.HasPrefix(trimmed, "Current week") {

			label := trimmed
			for j := i + 1; j < i+5 && j < len(lines); j++ {
				nextLine := strings.TrimSpace(lines[j])
				if nextLine == "" {
					continue
				}

				if m := rePctUsed.FindStringSubmatch(nextLine); m != nil {
					var pct float64
					fmt.Sscanf(m[1], "%f", &pct)
					blocks = append(blocks, usageBlock{label: label, pct: pct})
					continue
				}

				if m := reResetsIn.FindStringSubmatch(nextLine); m != nil {
					if len(blocks) > 0 {
						blocks[len(blocks)-1].reset = strings.TrimSpace(m[1])
					}
					break
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
		shortLabel := b.label
		if strings.HasPrefix(b.label, "Current session") {
			shortLabel = "Session"
		} else if strings.HasPrefix(b.label, "Current week") {
			shortLabel = "Weekly"
		}

		metrics = append(metrics, Metric{
			Label: shortLabel,
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

func formatDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
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

	iconData, err := os.ReadFile("build/appicon.png")
	if err != nil {
		log.Printf("warning: could not load tray icon: %v", err)
	} else {
		tray.SetIcon(iconData)
	}
	tray.SetTooltip("Token Watch")

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
