package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/trayicon.png
var iconData []byte

// ── Data types ───────────────────────────────────────────────

type ProviderUsage struct {
	Name       string   `json:"name"`
	Metrics    []Metric `json:"metrics"`
	ResetIn    string   `json:"resetIn"`
	ResetEpoch int64    `json:"resetEpoch"`
	Expired    bool     `json:"expired,omitempty"`
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

// ── DoToken service (bound to frontend) ─────────────────

type DoToken struct{}

type AppConfig struct {
	ZaiToken        string   `json:"zaiToken"`
	ClaudeSession   string   `json:"claudeSession"`
	OpenCodeCookie  string   `json:"openCodeCookie"`
	ProviderOrder   []string `json:"providerOrder"`
}

func getConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dotoken.json")
}

func (t *DoToken) SaveSettings(zaiToken, claudeSession, openCodeCookie string) (string, error) {
	cfg := AppConfig{ZaiToken: zaiToken, ClaudeSession: claudeSession, OpenCodeCookie: openCodeCookie}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	err = os.WriteFile(getConfigPath(), data, 0644)
	if err == nil {
		go refreshUsage()
	}
	return "", err
}

func (t *DoToken) GetSettings() AppConfig {
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

func (t *DoToken) QuitApp() {
	os.Exit(0)
}

func (t *DoToken) SaveProviderOrder(order []string) error {
	cfg := t.GetSettings()
	cfg.ProviderOrder = order
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getConfigPath(), data, 0644)
}

func applyOrder(providers []ProviderUsage, order []string) []ProviderUsage {
	if len(order) == 0 {
		return providers
	}
	rank := map[string]int{}
	for i, name := range order {
		rank[name] = i
	}
	reordered := make([]ProviderUsage, len(providers))
	copy(reordered, providers)
	for i := 0; i < len(reordered); i++ {
		for j := i + 1; j < len(reordered); j++ {
			ri := rank[reordered[i].Name]
			rj := rank[reordered[j].Name]
			// Unknown providers go to the end
			if _, ok := rank[reordered[j].Name]; !ok {
				continue
			}
			if _, ok := rank[reordered[i].Name]; !ok || ri > rj {
				reordered[i], reordered[j] = reordered[j], reordered[i]
			}
		}
	}
	return reordered
}

var cachedUsage AllUsage
var appWindow *application.WebviewWindow

func refreshUsage() {
	// Fetch fast providers first, emit immediately
	var providers []ProviderUsage
	if oc := fetchOpenCodeUsage(); oc != nil {
		providers = append(providers, *oc)
	}
	if zai := fetchZaiUsage(); zai != nil {
		providers = append(providers, *zai)
	}

	// Claude: use cached data if available, otherwise show loading placeholder
	cfg := AppConfig{}
	if data, err := os.ReadFile(getConfigPath()); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if creds, _ := readClaudeOauth(); creds != nil {
		hasClaudeCache := false
		for _, p := range cachedUsage.Providers {
			if p.Name == "Claude" {
				providers = append([]ProviderUsage{p}, providers...)
				hasClaudeCache = true
				break
			}
		}
		if !hasClaudeCache {
			providers = append([]ProviderUsage{{
				Name:    "Claude",
				Metrics: []Metric{{Label: "Session", Pct: -1}},
				ResetIn: "loading…",
			}}, providers...)
		}
	}

	if len(providers) > 0 {
		providers = applyOrder(providers, cfg.ProviderOrder)
		cachedUsage = AllUsage{
			UpdatedAt: time.Now().Format("15:04:05"),
			Providers: providers,
		}
		if appWindow != nil {
			appWindow.EmitEvent("usage", cachedUsage)
		}
	}

	// Fetch Claude in background, emit again when done
	go func() {
		claude := fetchClaudeUsage()
		if claude == nil {
			return
		}

		// Replace placeholder with real data
		for i, p := range cachedUsage.Providers {
			if p.Name == "Claude" {
				cachedUsage.Providers[i] = *claude
				cachedUsage.UpdatedAt = time.Now().Format("15:04:05")
				cachedUsage.Providers = applyOrder(cachedUsage.Providers, (&DoToken{}).GetSettings().ProviderOrder)
				if appWindow != nil {
					appWindow.EmitEvent("usage", cachedUsage)
				}
				return
			}
		}
		// Claude wasn't in cache yet, prepend it
		cachedUsage.Providers = append([]ProviderUsage{*claude}, cachedUsage.Providers...)
		cachedUsage.Providers = applyOrder(cachedUsage.Providers, (&DoToken{}).GetSettings().ProviderOrder)
		cachedUsage.UpdatedAt = time.Now().Format("15:04:05")
		if appWindow != nil {
			appWindow.EmitEvent("usage", cachedUsage)
		}
	}()
}

func (t *DoToken) FetchUsage() AllUsage {
	go refreshUsage()
	return cachedUsage
}

// StartPolling is kept for compatibility but does nothing
func (t *DoToken) StartPolling() {}

func (t *DoToken) ResizePopup(height float64) {
	if appWindow == nil {
		return
	}
	h := int(height)
	if h < 120 {
		h = 120
	}
	if h > 600 {
		h = 600
	}
	appWindow.SetSize(300, h)
}

// ── Claude (OAuth usage API) ──────────────────────────────

const claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

type claudeOauthCreds struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"`
}

func readClaudeOauth() (*claudeOauthCreds, func(*claudeOauthCreds) error) {
	// macOS Keychain — Claude Code's default store
	if out, err := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output(); err == nil {
		var parsed struct {
			ClaudeAiOauth claudeOauthCreds `json:"claudeAiOauth"`
		}
		if json.Unmarshal(out, &parsed) == nil && parsed.ClaudeAiOauth.AccessToken != "" {
			raw := out
			persist := func(n *claudeOauthCreds) error { return writeClaudeKeychain(raw, n) }
			return &parsed.ClaudeAiOauth, persist
		}
	}

	// File fallback (older CLI versions / headless logins)
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".claude", "credentials.json"),
	} {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var parsed struct {
			ClaudeAiOauth claudeOauthCreds `json:"claudeAiOauth"`
		}
		if json.Unmarshal(data, &parsed) == nil && parsed.ClaudeAiOauth.AccessToken != "" {
			path := p
			persist := func(n *claudeOauthCreds) error { return writeClaudeFile(path, data, n) }
			return &parsed.ClaudeAiOauth, persist
		}
	}
	return nil, nil
}

// writeClaudeKeychain rewrites the Keychain item with refreshed OAuth fields,
// preserving every other top-level key Claude Code keeps there.
func writeClaudeKeychain(raw []byte, n *claudeOauthCreds) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	updated, err := json.Marshal(n)
	if err != nil {
		return err
	}
	top["claudeAiOauth"] = updated
	full, err := json.Marshal(top)
	if err != nil {
		return err
	}

	meta, err := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials").CombinedOutput()
	if err != nil {
		return err
	}
	reAcct := regexp.MustCompile(`"acct"<blob>="([^"]+)"`)
	m := reAcct.FindSubmatch(meta)
	if m == nil {
		return fmt.Errorf("keychain account not found")
	}
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", "Claude Code-credentials", "-a", string(m[1]), "-w", string(full))
	return cmd.Run()
}

// writeClaudeFile rewrites the credentials file with refreshed OAuth fields.
func writeClaudeFile(path string, raw []byte, n *claudeOauthCreds) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	updated, err := json.Marshal(n)
	if err != nil {
		return err
	}
	top["claudeAiOauth"] = updated
	full, err := json.Marshal(top)
	if err != nil {
		return err
	}
	return os.WriteFile(path, full, 0600)
}

func refreshClaudeToken(refreshToken string) *claudeOauthCreds {
	payload, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     claudeOAuthClientID,
	})
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post("https://console.anthropic.com/v1/oauth/token", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("claude: token refresh failed: %d", resp.StatusCode)
		return nil
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.NewDecoder(resp.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		return nil
	}
	rt := tok.RefreshToken
	if rt == "" {
		rt = refreshToken
	}
	return &claudeOauthCreds{
		AccessToken:  tok.AccessToken,
		RefreshToken: rt,
		ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli(),
	}
}

func claudeExpired(msg string) *ProviderUsage {
	return &ProviderUsage{
		Name:    "Claude",
		Expired: true,
		Metrics: []Metric{},
		ResetIn: msg,
	}
}

func fetchClaudeUsage() *ProviderUsage {
	var creds *claudeOauthCreds
	var persist func(*claudeOauthCreds) error
	creds, persist = readClaudeOauth()
	if creds == nil {
		return claudeExpired("Claude Code not signed in — run: claude /login")
	}

	callUsage := func(token string) ([]byte, int) {
		req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-version", "2023-06-01")
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return body, resp.StatusCode
	}

	body, status := callUsage(creds.AccessToken)
	if status == 401 && creds.RefreshToken != "" {
		if fresh := refreshClaudeToken(creds.RefreshToken); fresh != nil {
			creds = fresh
			// Write the rotated tokens back so Claude Code keeps a valid pair
			if persist != nil {
				if err := persist(fresh); err != nil {
					log.Printf("claude: persisting refreshed token failed: %v", err)
				}
			}
			body, status = callUsage(creds.AccessToken)
		}
	}
	if status == 401 {
		return claudeExpired("Claude sign-in expired — run: claude /login")
	}
	if status != 200 {
		log.Printf("claude: usage request failed: %d", status)
		return nil
	}

	var usage struct {
		Limits []struct {
			Kind        string  `json:"kind"`
			Percent     float64 `json:"percent"`
			Utilization float64 `json:"utilization"`
			ResetsAt    string  `json:"resets_at"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(body, &usage); err != nil {
		return nil
	}

	nameMap := map[string]string{
		"session":    "Session",
		"weekly_all": "Weekly",
	}
	var metrics []Metric
	var nearestReset int64
	for _, l := range usage.Limits {
		label, ok := nameMap[l.Kind]
		if !ok {
			continue
		}
		pct := l.Percent
		if pct == 0 {
			pct = l.Utilization
		}
		metrics = append(metrics, Metric{Label: label, Pct: pct})
		if t, err := time.Parse(time.RFC3339, l.ResetsAt); err == nil {
			epoch := t.Unix()
			if nearestReset == 0 || epoch < nearestReset {
				nearestReset = epoch
			}
		}
	}
	if len(metrics) == 0 {
		return nil
	}

	var resetStr string
	if nearestReset > 0 {
		resetStr = formatResetTime(nearestReset)
	}
	return &ProviderUsage{
		Name:    "Claude",
		Metrics: metrics,
		ResetIn: resetStr,
	}
}

// ── OpenCode Go (web scraping) ──────────────────────────

func opencodeExpired(cookie string) *ProviderUsage {
	if !strings.Contains(cookie, "__Host-console_session") {
		return &ProviderUsage{
			Name:    "OpenCode",
			Expired: true,
			Metrics: []Metric{},
			ResetIn: "cookie incomplete — copy the full Cookie header (must include __Host-console_session)",
		}
	}
	return &ProviderUsage{
		Name:    "OpenCode",
		Expired: true,
		Metrics: []Metric{},
		ResetIn: "session expired — paste fresh Cookie header from opencode.ai console",
	}
}

func fetchOpenCodeUsage() *ProviderUsage {
	cookie := os.Getenv("OPENCODE_AUTH_COOKIE")
	if cookie == "" {
		cookie = (&DoToken{}).GetSettings().OpenCodeCookie
	}
	if cookie == "" {
		return nil
	}
	// Users paste the full Cookie header from opencode.ai — it must include
	// both auth= and __Host-console_session= (auth alone is rejected with 401)
	client := &http.Client{Timeout: 10 * time.Second}

	// Resolve the org (workspace) that holds the Go subscription
	req, err := http.NewRequest("GET", "https://opencode.ai/console/api/orgs", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("opencode: request failed: %v", err)
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 401 {
		return opencodeExpired(cookie)
	}
	var orgs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &orgs); err != nil || len(orgs) == 0 {
		log.Printf("opencode: no orgs found (status %d)", resp.StatusCode)
		return nil
	}

	// Query go/status per org; first org with a Go plan wins
	var status *struct {
		Access struct {
			Meters map[string]struct {
				LimitMicroCents string `json:"limitMicroCents"`
				UsedMicroCents  string `json:"usedMicroCents"`
				ResetsAt        string `json:"resetsAt"`
			} `json:"meters"`
		} `json:"access"`
	}
	unauthorized := false
	for _, org := range orgs {
		req, err := http.NewRequest("GET", "https://opencode.ai/console/api/go/status", nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Cookie", cookie)
		req.Header.Set("x-org-id", org.ID)
		req.Header.Set("Accept", "*/*")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("opencode: request failed: %v", err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 401 || strings.Contains(string(body), "_tag\":\"Unauthorized") {
			unauthorized = true
			continue
		}
		if resp.StatusCode != 200 {
			continue
		}
		var s struct {
			Access struct {
				Meters map[string]struct {
					LimitMicroCents string `json:"limitMicroCents"`
					UsedMicroCents  string `json:"usedMicroCents"`
					ResetsAt        string `json:"resetsAt"`
				} `json:"meters"`
			} `json:"access"`
		}
		if err := json.Unmarshal(body, &s); err != nil {
			continue
		}
		status = &s
		break
	}

	if status == nil {
		if unauthorized {
			return opencodeExpired(cookie)
		}
		log.Printf("opencode: no Go subscription found in any org")
		return nil
	}

	nameMap := map[string]string{"fiveHour": "5h rolling", "week": "Weekly", "month": "Monthly"}
	var metrics []Metric
	var nearestReset int64
	for _, key := range []string{"fiveHour", "week", "month"} {
		m, ok := status.Access.Meters[key]
		if !ok {
			continue
		}
		limit, _ := strconv.ParseFloat(m.LimitMicroCents, 64)
		used, _ := strconv.ParseFloat(m.UsedMicroCents, 64)
		if limit <= 0 {
			continue
		}
		metrics = append(metrics, Metric{Label: nameMap[key], Pct: used / limit * 100})
		if m.ResetsAt != "" {
			if t, err := time.Parse(time.RFC3339, m.ResetsAt); err == nil {
				epoch := t.Unix()
				if nearestReset == 0 || epoch < nearestReset {
					nearestReset = epoch
				}
			}
		}
	}
	if len(metrics) == 0 {
		return nil
	}

	var resetStr string
	if nearestReset > 0 {
		resetStr = formatResetTime(nearestReset)
	}
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
		token = (&DoToken{}).GetSettings().ZaiToken
	}
	if token == "" {
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://api.z.ai/api/monitor/usage/quota/limit", nil)
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
		Name:        "DoToken",
		Description: "AI token usage monitor",
		Services: []application.Service{
			application.NewService(&DoToken{}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
		},
	})

	appWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "DoToken",
		Width:     300,
		Height:    400,
		Frameless: true,
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHidden,
			Backdrop: application.MacBackdropNormal,
		},
		BackgroundType:   application.BackgroundTypeSolid,
		BackgroundColour: application.NewRGBA(14, 15, 17, 255), // match --bg #0e0f11
		Hidden:           true,
		AlwaysOnTop:      true,
		URL:              "/",
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: true,
		},
	})

	// System tray
	tray := app.SystemTray.New()

	tray.SetIcon(iconData)

	tray.AttachWindow(appWindow).WindowOffset(5)

	menu := app.NewMenu()
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	tray.SetMenu(menu)

	err := app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
