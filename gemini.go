package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ── Gemini CLI (Code Assist quota API) ─────────────────────
//
// Gemini CLI signs in with Google and stores the OAuth token in
// ~/.gemini/oauth_creds.json. The same Code Assist backend the CLI talks to
// reports each model's daily request quota, so the card needs no setup.

const geminiCodeAssistBase = "https://cloudcode-pa.googleapis.com/v1internal"

type geminiOauthCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiryDate is milliseconds since the epoch
	ExpiryDate int64 `json:"expiry_date"`
}

func geminiDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini")
}

func geminiExpired(msg string) *ProviderUsage {
	return &ProviderUsage{
		Name:    "Gemini",
		Expired: true,
		Metrics: []Metric{},
		ResetIn: msg,
	}
}

// geminiAuthType returns the sign-in method chosen in ~/.gemini/settings.json,
// reading both the current (security.auth.selectedType) and older
// (selectedAuthType) locations. Empty when unset.
func geminiAuthType(settings []byte) string {
	var s struct {
		SelectedAuthType string `json:"selectedAuthType"`
		Security         struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
	}
	if json.Unmarshal(settings, &s) != nil {
		return ""
	}
	if s.Security.Auth.SelectedType != "" {
		return s.Security.Auth.SelectedType
	}
	return s.SelectedAuthType
}

// geminiOAuthClient finds the OAuth client Gemini CLI signs in with, by reading
// it out of the installed CLI. It is needed to refresh an expired access token;
// when the CLI cannot be found the user is asked to run gemini instead.
func geminiOAuthClient() (id, secret string) {
	bin, err := exec.LookPath("gemini")
	if err != nil {
		for _, p := range []string{"/opt/homebrew/bin/gemini", "/usr/local/bin/gemini"} {
			if _, err := os.Stat(p); err == nil {
				bin = p
				break
			}
		}
	}
	if bin == "" {
		return "", ""
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	// Current releases ship as a single bundled script holding the client
	if data, err := os.ReadFile(bin); err == nil {
		if id, secret := parseGeminiOAuthClient(data); id != "" && secret != "" {
			return id, secret
		}
	}

	// Walk up from the CLI's entry point to the node_modules directory that
	// holds it, then look for the core package's OAuth module.
	var roots []string
	for dir := filepath.Dir(bin); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "node_modules" {
			roots = append(roots, dir)
		}
		roots = append(roots, filepath.Join(dir, "node_modules"))
		if len(roots) > 16 {
			break
		}
	}
	for _, root := range roots {
		for _, rel := range []string{
			"@google/gemini-cli-core/dist/src/code_assist/oauth2.js",
			"@google/gemini-cli/node_modules/@google/gemini-cli-core/dist/src/code_assist/oauth2.js",
		} {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				continue
			}
			if id, secret := parseGeminiOAuthClient(data); id != "" && secret != "" {
				return id, secret
			}
		}
	}
	return "", ""
}

var (
	reGeminiClientID     = regexp.MustCompile(`OAUTH_CLIENT_ID\s*=\s*['"]([^'"]+)['"]`)
	reGeminiClientSecret = regexp.MustCompile(`OAUTH_CLIENT_SECRET\s*=\s*['"]([^'"]+)['"]`)
)

func parseGeminiOAuthClient(src []byte) (id, secret string) {
	if m := reGeminiClientID.FindSubmatch(src); m != nil {
		id = string(m[1])
	}
	if m := reGeminiClientSecret.FindSubmatch(src); m != nil {
		secret = string(m[1])
	}
	return id, secret
}

func refreshGeminiToken(refreshToken string) (*geminiOauthCreds, bool) {
	id, secret := geminiOAuthClient()
	if id == "" {
		return nil, false
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {id},
		"client_secret": {secret},
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.PostForm("https://oauth2.googleapis.com/token", form)
	if err != nil {
		return nil, true
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("gemini: token refresh failed: %d", resp.StatusCode)
		return nil, true
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.NewDecoder(resp.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		return nil, true
	}
	return &geminiOauthCreds{
		AccessToken:  tok.AccessToken,
		RefreshToken: refreshToken,
		ExpiryDate:   time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli(),
	}, true
}

// writeGeminiCreds stores a refreshed access token back in oauth_creds.json,
// keeping every other field Gemini CLI writes there.
func writeGeminiCreds(path string, raw []byte, n *geminiOauthCreds) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	for k, v := range map[string]any{
		"access_token": n.AccessToken,
		"expiry_date":  n.ExpiryDate,
	} {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		top[k] = b
	}
	full, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, full, 0600)
}

func geminiPost(token, method string, payload any) ([]byte, int) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", geminiCodeAssistBase+":"+method, bytes.NewReader(body))
	if err != nil {
		return nil, 0
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("gemini: %s failed: %v", method, err)
		return nil, 0
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out, resp.StatusCode
}

// geminiProject reads the Code Assist project id from a loadCodeAssist
// response; the field is a string for most accounts and an object for some.
func geminiProject(body []byte) string {
	var r struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.Project) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(r.Project, &s) == nil {
		return s
	}
	var obj struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.Project, &obj) == nil {
		return obj.ID
	}
	return ""
}

// geminiModelLabel shortens a model id to fit the row label,
// e.g. gemini-2.5-flash-lite -> "2.5 Lite", gemini-3-pro-preview -> "3 Pro".
func geminiModelLabel(model string) string {
	s := strings.TrimPrefix(strings.ToLower(model), "gemini-")
	if i := strings.Index(s, "-preview"); i >= 0 {
		s = s[:i]
	}
	s = strings.Replace(s, "flash-lite", "lite", 1)
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p != "" && (p[0] < '0' || p[0] > '9') {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	label := strings.Join(parts, " ")
	if len(label) > 10 {
		label = label[:10]
	}
	return label
}

// parseGeminiQuota turns a retrieveUserQuota response into one metric per
// model. Each bucket reports the fraction of the daily request quota left.
func parseGeminiQuota(body []byte) *ProviderUsage {
	var r struct {
		Buckets []struct {
			ModelID           string   `json:"modelId"`
			TokenType         string   `json:"tokenType"`
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
		} `json:"buckets"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}

	type bucket struct {
		model string
		used  float64
	}
	byModel := map[string]bucket{}
	var nearestReset int64
	for _, b := range r.Buckets {
		if b.ModelID == "" || b.RemainingFraction == nil || strings.HasSuffix(b.ModelID, "_vertex") {
			continue
		}
		if b.TokenType != "" && b.TokenType != "REQUESTS" {
			continue
		}
		used := (1 - *b.RemainingFraction) * 100
		if used < 0 {
			used = 0
		}
		if used > 100 {
			used = 100
		}
		// Keep the tightest bucket when a model is listed more than once
		if prev, ok := byModel[b.ModelID]; !ok || used > prev.used {
			byModel[b.ModelID] = bucket{b.ModelID, used}
		}
		if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil {
			if epoch := t.Unix(); nearestReset == 0 || epoch < nearestReset {
				nearestReset = epoch
			}
		}
	}
	if len(byModel) == 0 {
		return nil
	}

	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
	}
	sort.Strings(models)

	metrics := make([]Metric, 0, len(models))
	for _, m := range models {
		metrics = append(metrics, Metric{
			Label:         geminiModelLabel(m),
			Pct:           byModel[m].used,
			WindowMinutes: 24 * 60,
		})
	}
	return &ProviderUsage{
		Name:       "Gemini",
		Metrics:    metrics,
		ResetIn:    formatResetTime(nearestReset),
		ResetEpoch: nearestReset,
	}
}

func fetchGeminiUsage() *ProviderUsage {
	credsPath := filepath.Join(geminiDir(), "oauth_creds.json")
	raw, err := os.ReadFile(credsPath)
	if err != nil {
		// Gemini CLI not installed or never signed in: no card
		return nil
	}
	if settings, err := os.ReadFile(filepath.Join(geminiDir(), "settings.json")); err == nil {
		switch geminiAuthType(settings) {
		case "", "oauth-personal":
		default:
			// API key and Vertex AI sign-ins have no Code Assist quota to show
			return nil
		}
	}

	var creds geminiOauthCreds
	if json.Unmarshal(raw, &creds) != nil || (creds.AccessToken == "" && creds.RefreshToken == "") {
		return geminiExpired("Gemini CLI not signed in — Run: gemini")
	}

	// Refresh a little early so the token does not expire mid-request
	if creds.AccessToken == "" || creds.ExpiryDate <= time.Now().Add(time.Minute).UnixMilli() {
		if creds.RefreshToken == "" {
			return geminiExpired("Gemini sign-in expired — Run: gemini")
		}
		fresh, found := refreshGeminiToken(creds.RefreshToken)
		if fresh == nil {
			if !found {
				return geminiExpired("Gemini token expired — open the CLI to refresh it. Run: gemini")
			}
			return geminiExpired("Gemini sign-in expired — Run: gemini")
		}
		if err := writeGeminiCreds(credsPath, raw, fresh); err != nil {
			log.Printf("gemini: persisting refreshed token failed: %v", err)
		}
		creds = *fresh
	}

	meta := map[string]any{"metadata": map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}}
	body, status := geminiPost(creds.AccessToken, "loadCodeAssist", meta)
	if status == 401 {
		return geminiExpired("Gemini sign-in expired — Run: gemini")
	}
	if status != 200 {
		log.Printf("gemini: loadCodeAssist status %d", status)
		return nil
	}
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if project == "" {
		project = geminiProject(body)
	}

	body, status = geminiPost(creds.AccessToken, "retrieveUserQuota", map[string]string{"project": project})
	if status == 401 {
		return geminiExpired("Gemini sign-in expired — Run: gemini")
	}
	if status != 200 {
		log.Printf("gemini: retrieveUserQuota status %d", status)
		return nil
	}
	p := parseGeminiQuota(body)
	if p == nil {
		log.Printf("gemini: %s", fmt.Sprintf("no quota buckets in response (%d bytes)", len(body)))
	}
	return p
}
