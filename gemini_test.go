package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseGeminiQuota(t *testing.T) {
	body := []byte(`{
		"buckets": [
			{"remainingFraction": 0.75, "resetTime": "2026-11-06T08:00:00Z", "tokenType": "REQUESTS", "modelId": "gemini-2.5-pro"},
			{"remainingFraction": 1, "resetTime": "2026-11-06T09:00:00Z", "tokenType": "REQUESTS", "modelId": "gemini-2.5-flash"},
			{"remainingFraction": 0.5, "resetTime": "2026-11-06T08:00:00Z", "tokenType": "REQUESTS", "modelId": "gemini-2.5-pro_vertex"},
			{"remainingFraction": 0.9, "resetTime": "2026-11-06T08:00:00Z", "tokenType": "INPUT_TOKENS", "modelId": "gemini-2.5-flash"},
			{"resetTime": "2026-11-06T08:00:00Z", "tokenType": "REQUESTS", "modelId": "gemini-2.0-flash"}
		]
	}`)
	p := parseGeminiQuota(body)
	if p == nil {
		t.Fatal("expected provider usage, got nil")
	}
	if p.Name != "Gemini" {
		t.Errorf("Name = %q, want Gemini", p.Name)
	}
	want := []struct {
		label string
		pct   float64
	}{
		{"2.5 Flash", 0},
		{"2.5 Pro", 25},
	}
	if len(p.Metrics) != len(want) {
		t.Fatalf("got %d metrics, want %d: %+v", len(p.Metrics), len(want), p.Metrics)
	}
	for i, w := range want {
		m := p.Metrics[i]
		if m.Label != w.label || m.Pct != w.pct || m.WindowMinutes != 1440 {
			t.Errorf("metric[%d] = %+v, want label %q pct %v window 1440", i, m, w.label, w.pct)
		}
	}
	if want := int64(1793952000); p.ResetEpoch != want { // 2026-11-06T08:00:00Z
		t.Errorf("ResetEpoch = %d, want %d", p.ResetEpoch, want)
	}
}

func TestParseGeminiQuotaEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `{"buckets": []}`, `not json`} {
		if p := parseGeminiQuota([]byte(body)); p != nil {
			t.Errorf("parseGeminiQuota(%s) = %+v, want nil", body, p)
		}
	}
}

func TestGeminiModelLabel(t *testing.T) {
	tests := map[string]string{
		"gemini-2.5-pro":                "2.5 Pro",
		"gemini-2.5-flash":              "2.5 Flash",
		"gemini-2.5-flash-lite":         "2.5 Lite",
		"gemini-3-pro-preview":          "3 Pro",
		"gemini-2.5-flash-preview-0520": "2.5 Flash",
	}
	for in, want := range tests {
		if got := geminiModelLabel(in); got != want {
			t.Errorf("geminiModelLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeminiProject(t *testing.T) {
	tests := map[string]string{
		`{"cloudaicompanionProject": "proj-123"}`:         "proj-123",
		`{"cloudaicompanionProject": {"id": "proj-456"}}`: "proj-456",
		`{"currentTier": {"id": "free-tier"}}`:            "",
	}
	for body, want := range tests {
		if got := geminiProject([]byte(body)); got != want {
			t.Errorf("geminiProject(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestGeminiAuthType(t *testing.T) {
	tests := map[string]string{
		`{"security": {"auth": {"selectedType": "oauth-personal"}}}`: "oauth-personal",
		`{"selectedAuthType": "gemini-api-key"}`:                     "gemini-api-key",
		`{}`:                                                         "",
	}
	for body, want := range tests {
		if got := geminiAuthType([]byte(body)); got != want {
			t.Errorf("geminiAuthType(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestParseGeminiOAuthClient(t *testing.T) {
	src := []byte("// comment\nconst OAUTH_CLIENT_ID = 'abc.apps.googleusercontent.com';\nconst OAUTH_CLIENT_SECRET = \"s3cret\";\n")
	id, secret := parseGeminiOAuthClient(src)
	if id != "abc.apps.googleusercontent.com" || secret != "s3cret" {
		t.Errorf("parseGeminiOAuthClient() = %q, %q", id, secret)
	}
}

func TestWriteGeminiCredsKeepsOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth_creds.json")
	raw := []byte(`{"access_token":"old","refresh_token":"rt","scope":"s","id_token":"idt","expiry_date":1}`)
	if err := writeGeminiCreds(path, raw, &geminiOauthCreds{AccessToken: "new", ExpiryDate: 42}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["access_token"] != "new" || got["expiry_date"] != float64(42) ||
		got["refresh_token"] != "rt" || got["scope"] != "s" || got["id_token"] != "idt" {
		t.Errorf("unexpected creds after write: %v", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
		t.Errorf("creds file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestTrayLabelGemini(t *testing.T) {
	providers := []ProviderUsage{{Name: "Gemini", Metrics: []Metric{{Pct: 40, WindowMinutes: 1440}}}}
	if got, want := trayLabel(providers), ansiGreen+"G 60%"+ansiReset; got != want {
		t.Errorf("trayLabel() = %q, want %q", got, want)
	}
}
