package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
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
	// WindowMinutes is the length of the limit's window; 0 when unknown
	WindowMinutes int `json:"windowMinutes,omitempty"`
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
	ZaiToken       string   `json:"zaiToken"`
	ClaudeSession  string   `json:"claudeSession"`
	OpencodeApiKey string   `json:"opencodeApiKey"`
	KimiApiKey     string   `json:"kimiApiKey,omitempty"`
	ProviderOrder  []string `json:"providerOrder"`
	HideTrayLabel  bool     `json:"hideTrayLabel"`
	// StaticTrayLabel turns off the rotation, leaving one provider on screen
	StaticTrayLabel bool `json:"staticTrayLabel,omitempty"`
}

func getConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dotoken.json")
}

func (t *DoToken) SaveSettings(zaiToken, claudeSession, opencodeApiKey, kimiApiKey string, showTrayLabel, rotateTrayLabel bool) (string, error) {
	// Start from the saved config so fields not on the form (provider order) survive
	cfg := t.GetSettings()
	cfg.ZaiToken = zaiToken
	cfg.ClaudeSession = claudeSession
	cfg.OpencodeApiKey = opencodeApiKey
	cfg.KimiApiKey = kimiApiKey
	cfg.HideTrayLabel = !showTrayLabel
	cfg.StaticTrayLabel = !rotateTrayLabel
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
var usageMu sync.Mutex
var claudeInFlight atomic.Bool

// claudeRetryAt backs off while Anthropic's usage endpoint reports 429, so the
// 5-minute poller does not hammer it and the card does not flicker away.
var claudeRetryAt atomic.Int64
var appWindow *application.WebviewWindow
var tray *application.SystemTray

// How often usage is refreshed in the background so the menu bar label stays current
const pollInterval = 5 * time.Minute

// How long each provider's label stays in the menu bar before the next one
const trayRotateInterval = 4 * time.Second

// publishUsage pushes cachedUsage to the popup and the menu bar label.
// Callers must hold usageMu.
func publishUsage() {
	if appWindow != nil {
		appWindow.EmitEvent("usage", cachedUsage)
	}
	if tray != nil {
		cfg := (&DoToken{}).GetSettings()
		label := ""
		switch {
		case cfg.HideTrayLabel:
		case cfg.StaticTrayLabel:
			label = trayLabel(cachedUsage.Providers)
		default:
			// Rotating labels are advanced by rotateTrayLabel; start at the first
			if labels := trayLabels(cachedUsage.Providers); len(labels) > 0 {
				label = labels[0]
			}
		}
		tray.SetLabel(label)
	}
}

// ANSI colours for the menu bar label. Wails renders these through the
// system status item, which keeps the text legible in light and dark menu bars.
const (
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiReset  = "\x1b[0m"
)

// windowTier ranks a limit's window so the menu bar can report the shortest one
// first: up to 5 hours, then up to a week, then month and beyond, then unknown.
func windowTier(windowMinutes int) int {
	switch {
	case windowMinutes > 0 && windowMinutes <= 5*60:
		return 0
	case windowMinutes > 0 && windowMinutes <= 7*24*60:
		return 1
	default:
		return 2
	}
}

// providerShort is the name shown in the menu bar, where space is tight.
func providerShort(name string) string {
	switch name {
	case "Claude":
		return "C"
	case "OpenCode":
		return "OC"
	case "Z.ai":
		return "Z"
	case "Kimi":
		return "K"
	default:
		return name
	}
}

func remainingColour(remaining int) string {
	switch {
	case remaining < 20:
		return ansiRed
	case remaining <= 50:
		return ansiYellow
	default:
		return ansiGreen
	}
}

// tightestUsed returns the most used limit in a provider, taking the shortest
// window that has data, and reports its tier and whether there is anything to
// show at all.
func tightestUsed(p ProviderUsage) (float64, int, bool) {
	best := [3]float64{-1, -1, -1}
	for _, m := range p.Metrics {
		if m.Pct < 0 {
			continue
		}
		tier := windowTier(m.WindowMinutes)
		if m.Pct > best[tier] {
			best[tier] = m.Pct
		}
	}
	for tier, used := range best {
		if used >= 0 {
			return used, tier, true
		}
	}
	return 0, 0, false
}

// trayLabel is the single menu bar label: the tightest limit across every
// provider, taking the shortest window that has data.
func trayLabel(providers []ProviderUsage) string {
	best := [3]struct {
		used float64
		name string
	}{}
	for i := range best {
		best[i].used = -1
	}
	expired := ""
	for _, p := range providers {
		if p.Expired {
			if expired == "" {
				expired = p.Name
			}
			continue
		}
		used, tier, ok := tightestUsed(p)
		if !ok {
			continue
		}
		if used > best[tier].used {
			best[tier] = struct {
				used float64
				name string
			}{used, p.Name}
		}
	}

	for _, b := range best {
		if b.used >= 0 {
			remaining := int(math.Round(100 - b.used))
			return remainingColour(remaining) + fmt.Sprintf("%s %d%%", providerShort(b.name), remaining) + ansiReset
		}
	}
	if expired != "" {
		return ansiRed + providerShort(expired) + " !" + ansiReset
	}
	return ""
}

// trayLabels returns one entry per provider for the rotating menu bar label.
// Provider names and percentages are padded into two columns so the menu bar
// does not shift as the label changes.
func trayLabels(providers []ProviderUsage) []string {
	type entry struct {
		provider  string
		value     string
		remaining int
		plain     bool
	}
	var entries []entry
	for _, p := range providers {
		if p.Expired {
			entries = append(entries, entry{provider: providerShort(p.Name), value: "!", plain: true})
			continue
		}
		used, _, ok := tightestUsed(p)
		if !ok {
			continue
		}
		remaining := int(math.Round(100 - used))
		entries = append(entries, entry{
			provider:  providerShort(p.Name),
			value:     fmt.Sprintf("%d%%", remaining),
			remaining: remaining,
		})
	}
	if len(entries) == 0 {
		return nil
	}

	providerWidth, valueWidth := 0, 0
	for _, e := range entries {
		if len(e.provider) > providerWidth {
			providerWidth = len(e.provider)
		}
		if len(e.value) > valueWidth {
			valueWidth = len(e.value)
		}
	}
	labels := make([]string, 0, len(entries))
	for _, e := range entries {
		text := e.provider + strings.Repeat(" ", providerWidth-len(e.provider)) +
			" " + strings.Repeat(" ", valueWidth-len(e.value)) + e.value
		if e.plain {
			labels = append(labels, ansiRed+text+ansiReset)
			continue
		}
		labels = append(labels, remainingColour(e.remaining)+text+ansiReset)
	}
	return labels
}

// rotateTrayLabel cycles the menu bar label through the providers.
func rotateTrayLabel() {
	ticker := time.NewTicker(trayRotateInterval)
	defer ticker.Stop()
	i := 0
	for range ticker.C {
		cfg := (&DoToken{}).GetSettings()
		if cfg.HideTrayLabel || cfg.StaticTrayLabel {
			continue
		}
		usageMu.Lock()
		labels := trayLabels(cachedUsage.Providers)
		usageMu.Unlock()
		if len(labels) == 0 {
			continue
		}
		i++
		tray.SetLabel(labels[i%len(labels)])
	}
}

func pollUsage() {
	refreshUsage()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		refreshUsage()
	}
}

func refreshUsage() {
	// Fetch fast providers first, emit immediately
	var providers []ProviderUsage
	if oc := fetchOpenCodeUsage(); oc != nil {
		providers = append(providers, *oc)
	}
	if zai := fetchZaiUsage(); zai != nil {
		providers = append(providers, *zai)
	}
	if kimi := fetchKimiUsage(); kimi != nil {
		providers = append(providers, *kimi)
	}

	// Claude: use cached data if available, otherwise show loading placeholder
	cfg := AppConfig{}
	if data, err := os.ReadFile(getConfigPath()); err == nil {
		json.Unmarshal(data, &cfg)
	}
	creds, _ := readClaudeOauth()
	usageMu.Lock()
	if creds != nil {
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
		publishUsage()
	}
	usageMu.Unlock()

	// Fetch Claude in background, emit again when done. Skip if a fetch is
	// already running so two refreshes never rotate the same refresh token.
	if !claudeInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer claudeInFlight.Store(false)
		claude := fetchClaudeUsage()
		if claude == nil {
			return
		}

		usageMu.Lock()
		defer usageMu.Unlock()

		// Replace placeholder with real data
		for i, p := range cachedUsage.Providers {
			if p.Name == "Claude" {
				cachedUsage.Providers[i] = *claude
				cachedUsage.UpdatedAt = time.Now().Format("15:04:05")
				cachedUsage.Providers = applyOrder(cachedUsage.Providers, (&DoToken{}).GetSettings().ProviderOrder)
				publishUsage()
				return
			}
		}
		// Claude wasn't in cache yet, prepend it
		cachedUsage.Providers = append([]ProviderUsage{*claude}, cachedUsage.Providers...)
		cachedUsage.Providers = applyOrder(cachedUsage.Providers, (&DoToken{}).GetSettings().ProviderOrder)
		cachedUsage.UpdatedAt = time.Now().Format("15:04:05")
		publishUsage()
	}()
}

func (t *DoToken) FetchUsage() AllUsage {
	go refreshUsage()
	usageMu.Lock()
	defer usageMu.Unlock()
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

	callUsage := func(token string) (retryAfter int64, body []byte, status int) {
		req, _ := http.NewRequest("GET", "https://api.anthropic.com/api/oauth/usage", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-version", "2023-06-01")
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, 0
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		if ra, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Retry-After")), 10, 64); err == nil && ra > 0 {
			retryAfter = ra
		}
		return retryAfter, body, resp.StatusCode
	}

	if retryUntil := claudeRetryAt.Load(); retryUntil > time.Now().UnixMilli() {
		log.Printf("claude: usage skipped, backing off for %s", time.Until(time.UnixMilli(retryUntil)).Round(time.Second))
		return nil
	}

	retryAfter, body, status := callUsage(creds.AccessToken)
	if status == 429 {
		wait := time.Duration(60) * time.Second
		if retryAfter > 0 {
			wait = time.Duration(retryAfter) * time.Second
		}
		if wait < 15*time.Second {
			wait = 15 * time.Second
		}
		claudeRetryAt.Store(time.Now().Add(wait).UnixMilli())
		log.Printf("claude: rate limited, backing off for %s", wait)
		return nil
	}
	if status == 401 && creds.RefreshToken != "" {
		if fresh := refreshClaudeToken(creds.RefreshToken); fresh != nil {
			creds = fresh
			// Write the rotated tokens back so Claude Code keeps a valid pair
			if persist != nil {
				if err := persist(fresh); err != nil {
					log.Printf("claude: persisting refreshed token failed: %v", err)
				}
			}
			_, body, status = callUsage(creds.AccessToken)
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
	windows := map[string]int{
		"session":    300,
		"weekly_all": 7 * 24 * 60,
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
		m := Metric{Label: label, Pct: pct}
		m.WindowMinutes = windows[l.Kind]
		if t, err := time.Parse(time.RFC3339, l.ResetsAt); err == nil {
			epoch := t.Unix()
			if nearestReset == 0 || epoch < nearestReset {
				nearestReset = epoch
			}
		}
		metrics = append(metrics, m)
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

// ── OpenCode Go (official usage API) ────────────────────────

type opencodeWindow struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}

type opencodeUsageResponse struct {
	UseBalance bool `json:"useBalance"`
	Usage     struct {
		Rolling opencodeWindow `json:"rolling"`
		Weekly  opencodeWindow `json:"weekly"`
		Monthly opencodeWindow `json:"monthly"`
	} `json:"usage"`
}

func opencodeExpired(msg string) *ProviderUsage {
	return &ProviderUsage{
		Name:    "OpenCode",
		Expired: true,
		Metrics: []Metric{},
		ResetIn: msg,
	}
}

func parseOpenCodeUsage(body []byte) *ProviderUsage {
	var result opencodeUsageResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	windows := []struct {
		label   string
		minutes int
		window  opencodeWindow
	}{
		{"5h rolling", 300, result.Usage.Rolling},
		{"Weekly", 7 * 24 * 60, result.Usage.Weekly},
		{"Monthly", 30 * 24 * 60, result.Usage.Monthly},
	}

	var metrics []Metric
	var nearestReset int64
	for _, w := range windows {
		if w.window.ResetsAt == "" {
			continue
		}
		metric := Metric{Label: w.label, Pct: w.window.Percent, WindowMinutes: w.minutes}
		if t, err := time.Parse(time.RFC3339, w.window.ResetsAt); err == nil {
			epoch := t.Unix()
			if nearestReset == 0 || epoch < nearestReset {
				nearestReset = epoch
			}
		}
		metrics = append(metrics, metric)
	}
	if len(metrics) == 0 {
		return nil
	}

	var resetStr string
	if nearestReset > 0 {
		resetStr = formatResetTime(nearestReset)
	}
	return &ProviderUsage{
		Name:       "OpenCode",
		Metrics:    metrics,
		ResetIn:    resetStr,
		ResetEpoch: nearestReset,
	}
}

func fetchOpenCodeUsage() *ProviderUsage {
	apiKey := os.Getenv("OPENCODE_API_KEY")
	if apiKey == "" {
		apiKey = (&DoToken{}).GetSettings().OpencodeApiKey
	}
	if apiKey == "" {
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", "https://opencode.ai/zen/go/v1/usage", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("opencode: request failed: %v", err)
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	switch resp.StatusCode {
	case 401:
		return opencodeExpired("API key rejected — paste a fresh key from opencode.ai/auth")
	case 403:
		return opencodeExpired("OpenCode Go subscription required")
	case 200:
		return parseOpenCodeUsage(body)
	default:
		log.Printf("opencode: unexpected status %d", resp.StatusCode)
		return nil
	}
}

// ── Z.ai (API) ──────────────────────────────────────────────

type zaiLimit struct {
	Type          string  `json:"type"`
	Unit          int     `json:"unit"`
	Number        int     `json:"number"`
	Percentage    float64 `json:"percentage"`
	NextResetTime int64   `json:"nextResetTime"`
}

type zaiResponse struct {
	Data struct {
		Limits []zaiLimit `json:"limits"`
	} `json:"data"`
}

// zaiWindowMinutes maps a Z.ai limit's unit and number onto a window length.
// The units are the codes the API returns: 3=hours, 4=days, 5=30 days, 6=weeks.
func zaiWindowMinutes(limit zaiLimit) int {
	units := map[int]int{3: 60, 4: 1440, 5: 43200, 6: 10080}
	unit, ok := units[limit.Unit]
	if !ok || limit.Number <= 0 {
		return 0
	}
	return unit * limit.Number
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

		// Z.ai doesn't document each limit's window length; the unit and number
		// fields above are what we use for the menu bar window ranking
		m := Metric{Label: label, Pct: limit.Percentage, WindowMinutes: zaiWindowMinutes(limit)}
		metrics = append(metrics, m)

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

// ── Kimi For Coding (Kimi Code usage API) ───────────────────

// kimiCount holds a Kimi counter, which the API sends as a string or a number.
type kimiCount string

func (c *kimiCount) UnmarshalJSON(b []byte) error {
	*c = kimiCount(strings.Trim(string(b), `"`))
	return nil
}

func (c kimiCount) value() (float64, bool) {
	v, err := strconv.ParseFloat(string(c), 64)
	return v, err == nil
}

type kimiDetail struct {
	Limit     kimiCount `json:"limit"`
	Used      kimiCount `json:"used"`
	Remaining kimiCount `json:"remaining"`
	ResetTime string    `json:"resetTime"`
}

// pct turns the request counters into a usage %, preferring used over remaining.
func (d *kimiDetail) pct() (float64, bool) {
	if d == nil {
		return 0, false
	}
	limit, ok := d.Limit.value()
	if !ok || limit <= 0 {
		return 0, false
	}
	if used, ok := d.Used.value(); ok && used >= 0 {
		return math.Min(used/limit*100, 100), true
	}
	if remaining, ok := d.Remaining.value(); ok && remaining >= 0 && remaining <= limit {
		return (limit - remaining) / limit * 100, true
	}
	return 0, false
}

type kimiPool struct {
	UsedRatio *float64 `json:"used_ratio"`
	ResetTime string   `json:"reset_time"`
}

type kimiUsageResponse struct {
	// Weekly request counter
	Usage *kimiDetail `json:"usage"`
	// Short rate-limit windows; the first is the 5-hour session
	Limits []struct {
		Window struct {
			Duration int    `json:"duration"`
			TimeUnit string `json:"timeUnit"`
		} `json:"window"`
		Detail kimiDetail `json:"detail"`
	} `json:"limits"`
	// Newer accounts report ratio pools instead of (or alongside) counters
	Usages struct {
		Session *kimiPool `json:"limit_5h"`
		Weekly  *kimiPool `json:"limit_7d"`
		Monthly *kimiPool `json:"limit_month_total"`
	} `json:"usages"`
}

func kimiWindowMinutes(duration int, unit string) int {
	units := map[string]int{"TIME_UNIT_MINUTE": 1, "TIME_UNIT_HOUR": 60, "TIME_UNIT_DAY": 1440}
	if duration <= 0 {
		return 0
	}
	return units[unit] * duration
}

func parseKimiUsage(body []byte) *ProviderUsage {
	var result kimiUsageResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	type window struct {
		label   string
		minutes int
		pct     float64
		reset   string
		ok      bool
	}
	// take records a reading for a window, keeping the more exhausted one when
	// the counters and the ratio pool both report it
	take := func(w *window, pct float64, reset string) {
		if !w.ok || pct > w.pct {
			w.pct, w.reset = pct, reset
		}
		w.ok = true
	}
	session := window{label: "Session", minutes: 300}
	weekly := window{label: "Weekly", minutes: 7 * 24 * 60}
	monthly := window{label: "Monthly", minutes: 30 * 24 * 60}

	if len(result.Limits) > 0 {
		l := result.Limits[0]
		if pct, ok := l.Detail.pct(); ok {
			if m := kimiWindowMinutes(l.Window.Duration, l.Window.TimeUnit); m > 0 {
				session.minutes = m
			}
			take(&session, pct, l.Detail.ResetTime)
		}
	}
	if pct, ok := result.Usage.pct(); ok {
		take(&weekly, pct, result.Usage.ResetTime)
	}
	for _, p := range []struct {
		w    *window
		pool *kimiPool
	}{{&session, result.Usages.Session}, {&weekly, result.Usages.Weekly}, {&monthly, result.Usages.Monthly}} {
		if p.pool != nil && p.pool.UsedRatio != nil && *p.pool.UsedRatio >= 0 {
			take(p.w, math.Min(*p.pool.UsedRatio, 1)*100, p.pool.ResetTime)
		}
	}

	var metrics []Metric
	var nearestReset int64
	for _, w := range []window{session, weekly, monthly} {
		if !w.ok {
			continue
		}
		metrics = append(metrics, Metric{Label: w.label, Pct: w.pct, WindowMinutes: w.minutes})
		if t, err := time.Parse(time.RFC3339, w.reset); err == nil {
			if epoch := t.Unix(); nearestReset == 0 || epoch < nearestReset {
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
		Name:       "Kimi",
		Metrics:    metrics,
		ResetIn:    resetStr,
		ResetEpoch: nearestReset,
	}
}

func kimiExpired(msg string) *ProviderUsage {
	return &ProviderUsage{
		Name:    "Kimi",
		Expired: true,
		Metrics: []Metric{},
		ResetIn: msg,
	}
}

// Kimi Code keys are issued per region; the international host is tried when
// the China host rejects the key.
var kimiUsageURLs = []string{
	"https://api.kimi.com/coding/v1/usages",
	"https://api.kimi.ai/coding/v1/usages",
}

func fetchKimiUsage() *ProviderUsage {
	apiKey := os.Getenv("KIMI_API_KEY")
	if apiKey == "" {
		apiKey = (&DoToken{}).GetSettings().KimiApiKey
	}
	if apiKey == "" {
		return nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	status := 0
	for _, url := range kimiUsageURLs {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("kimi: request failed: %v", err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		status = resp.StatusCode
		switch status {
		case 200:
			return parseKimiUsage(body)
		case 401:
			continue
		default:
			log.Printf("kimi: unexpected status %d", status)
			return nil
		}
	}
	if status == 401 {
		return kimiExpired("API key rejected — paste a fresh key from the Kimi Code console")
	}
	return nil
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
	tray = app.SystemTray.New()

	tray.SetIcon(iconData)

	tray.AttachWindow(appWindow).WindowOffset(5)

	menu := app.NewMenu()
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	tray.SetMenu(menu)

	// Keep usage fresh in the background so the menu bar label is current
	// without opening the popup
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go pollUsage()
		go rotateTrayLabel()
	})

	err := app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
