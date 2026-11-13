package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTrayLabel(t *testing.T) {
	tests := []struct {
		name      string
		providers []ProviderUsage
		want      string
	}{
		{"no providers", nil, ""},
		{"loading placeholder only", []ProviderUsage{
			{Name: "Claude", Metrics: []Metric{{Label: "Session", Pct: -1}}},
		}, ""},
		{"tightest session wins", []ProviderUsage{
			{Name: "Claude", Metrics: []Metric{{Pct: 12.4, WindowMinutes: 300}, {Pct: 41, WindowMinutes: 10080}}},
			{Name: "Z.ai", Metrics: []Metric{{Pct: 72.6, WindowMinutes: 300}}},
		}, ansiYellow + "Z 27%" + ansiReset},
		{"weekly ignored while a session limit exists", []ProviderUsage{
			{Name: "Claude", Metrics: []Metric{{Pct: 20, WindowMinutes: 300}, {Pct: 95, WindowMinutes: 10080}}},
		}, ansiGreen + "C 80%" + ansiReset},
		{"weekly used when no session limit exists", []ProviderUsage{
			{Name: "Claude", Metrics: []Metric{{Pct: 95, WindowMinutes: 10080}}},
		}, ansiRed + "C 5%" + ansiReset},
		{"monthly is the last resort", []ProviderUsage{
			{Name: "OpenCode", Metrics: []Metric{{Pct: 90, WindowMinutes: 43200}}},
		}, ansiRed + "OC 10%" + ansiReset},
		{"unknown window ranks last", []ProviderUsage{
			{Name: "OpenCode", Metrics: []Metric{{Pct: 10, WindowMinutes: 10080}, {Pct: 90}}},
		}, ansiGreen + "OC 90%" + ansiReset},
		{"no usage is green", []ProviderUsage{
			{Name: "Z.ai", Metrics: []Metric{{Pct: 0, WindowMinutes: 300}}},
		}, ansiGreen + "Z 100%" + ansiReset},
		{"half left is green", []ProviderUsage{
			{Name: "Z.ai", Metrics: []Metric{{Pct: 49, WindowMinutes: 300}}},
		}, ansiGreen + "Z 51%" + ansiReset},
		{"half used is yellow", []ProviderUsage{
			{Name: "Z.ai", Metrics: []Metric{{Pct: 50, WindowMinutes: 300}}},
		}, ansiYellow + "Z 50%" + ansiReset},
		{"running out is red", []ProviderUsage{
			{Name: "Z.ai", Metrics: []Metric{{Pct: 85, WindowMinutes: 300}}},
		}, ansiRed + "Z 15%" + ansiReset},
		{"expired only", []ProviderUsage{
			{Name: "Claude", Expired: true, Metrics: []Metric{}},
		}, ansiRed + "C !" + ansiReset},
		{"expired alongside usage", []ProviderUsage{
			{Name: "Claude", Expired: true, Metrics: []Metric{}},
			{Name: "OpenCode", Metrics: []Metric{{Pct: 55, WindowMinutes: 300}}},
		}, ansiYellow + "OC 45%" + ansiReset},
	}
	for _, tt := range tests {
		if got := trayLabel(tt.providers); got != tt.want {
			t.Errorf("%s: trayLabel() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTrayLabels(t *testing.T) {
	providers := []ProviderUsage{
		{Name: "Claude", Metrics: []Metric{{Pct: 10, WindowMinutes: 300}, {Pct: 54, WindowMinutes: 10080}}},
		{Name: "OpenCode", Metrics: []Metric{{Pct: 4.52, WindowMinutes: 300}, {Pct: 14.34, WindowMinutes: 43200}}},
		{Name: "Z.ai", Metrics: []Metric{{Pct: 0, WindowMinutes: 300}}},
	}
	want := []string{
		ansiGreen + "C   90%" + ansiReset,
		ansiGreen + "OC  95%" + ansiReset,
		ansiGreen + "Z  100%" + ansiReset,
	}
	got := trayLabels(providers)
	if len(got) != len(want) {
		t.Fatalf("trayLabels() returned %d labels, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trayLabels()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTrayLabelsPadsToEqualWidth(t *testing.T) {
	providers := []ProviderUsage{
		{Name: "Claude", Metrics: []Metric{{Pct: 10, WindowMinutes: 300}}},
		{Name: "OpenCode", Metrics: []Metric{{Pct: 4.52, WindowMinutes: 300}}},
	}
	// The menu bar must not shift as the label rotates, so both entries occupy
	// the same number of visible characters.
	visible := func(s string) int {
		n := 0
		inEscape := false
		for _, r := range s {
			switch {
			case r == 0x1b:
				inEscape = true
			case inEscape && r == 'm':
				inEscape = false
			case !inEscape:
				n++
			}
		}
		return n
	}
	labels := trayLabels(providers)
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %q", labels)
	}
	if visible(labels[0]) != visible(labels[1]) {
		t.Errorf("labels differ in width: %q (%d) vs %q (%d)", labels[0], visible(labels[0]), labels[1], visible(labels[1]))
	}
}

func TestTrayLabelsSkipsProvidersWithoutData(t *testing.T) {
	providers := []ProviderUsage{
		{Name: "Claude", Metrics: []Metric{{Pct: -1, WindowMinutes: 300}}},
		{Name: "OpenCode", Metrics: []Metric{{Pct: 4.52, WindowMinutes: 300}}},
	}
	labels := trayLabels(providers)
	if len(labels) != 1 {
		t.Fatalf("expected only the provider with data, got %q", labels)
	}
}

func TestZaiWindowMinutes(t *testing.T) {
	tests := []struct {
		unit, number int
		want         int
	}{
		{3, 5, 300},   // hours
		{3, 1, 60},    // one hour
		{4, 1, 1440},  // days
		{5, 1, 43200}, // 30 days
		{6, 1, 10080}, // weeks
		{9, 1, 0},     // unknown unit
		{3, 0, 0},     // no number
	}
	for _, tt := range tests {
		got := zaiWindowMinutes(zaiLimit{Unit: tt.unit, Number: tt.number})
		if got != tt.want {
			t.Errorf("zaiWindowMinutes(unit=%d, number=%d) = %d, want %d", tt.unit, tt.number, got, tt.want)
		}
	}
}

func TestParseOpenCodeUsage(t *testing.T) {
	body := []byte(`{
		"useBalance": false,
		"usage": {
			"rolling": { "status": "ok", "percent": 11, "resetsAt": "2026-08-17T17:10:09.580Z" },
			"weekly":  { "status": "ok", "percent": 4,  "resetsAt": "2026-08-24T00:00:00.580Z" },
			"monthly": { "status": "ok", "percent": 2,  "resetsAt": "2026-09-13T04:26:00.580Z" }
		}
	}`)

	p := parseOpenCodeUsage(body)
	if p == nil {
		t.Fatal("expected provider usage, got nil")
	}
	if p.Name != "OpenCode" {
		t.Errorf("Name = %q, want OpenCode", p.Name)
	}
	want := []struct {
		label   string
		pct     float64
		minutes int
	}{
		{"5h rolling", 11, 300},
		{"Weekly", 4, 7 * 24 * 60},
		{"Monthly", 2, 30 * 24 * 60},
	}
	if len(p.Metrics) != len(want) {
		t.Fatalf("got %d metrics, want %d", len(p.Metrics), len(want))
	}
	for i, w := range want {
		if p.Metrics[i].Label != w.label {
			t.Errorf("metric[%d].Label = %q, want %q", i, p.Metrics[i].Label, w.label)
		}
		if p.Metrics[i].Pct != w.pct {
			t.Errorf("metric[%d].Pct = %v, want %v", i, p.Metrics[i].Pct, w.pct)
		}
		if p.Metrics[i].WindowMinutes != w.minutes {
			t.Errorf("metric[%d].WindowMinutes = %d, want %d", i, p.Metrics[i].WindowMinutes, w.minutes)
		}
	}

	wantReset := int64(1786986609) // 2026-08-17T17:10:09Z, the earliest resetsAt
	if p.ResetEpoch != wantReset {
		t.Errorf("ResetEpoch = %d, want %d", p.ResetEpoch, wantReset)
	}
}

func TestParseOpenCodeUsageEmpty(t *testing.T) {
	if p := parseOpenCodeUsage([]byte(`{}`)); p != nil {
		t.Errorf("expected nil for empty usage, got %+v", p)
	}
	if p := parseOpenCodeUsage([]byte(`not json`)); p != nil {
		t.Errorf("expected nil for invalid json, got %+v", p)
	}
}

func TestClaudeKeychainQueries(t *testing.T) {
	service := "Claude Code-credentials"
	tests := []struct {
		name     string
		username string
		want     [][]string
	}{
		{
			"username item first, service-only fallback",
			"art",
			[][]string{
				{"find-generic-password", "-s", service, "-a", "art"},
				{"find-generic-password", "-s", service},
			},
		},
		{
			"no username means service-only",
			"",
			[][]string{
				{"find-generic-password", "-s", service},
			},
		},
	}
	for _, tt := range tests {
		got := claudeKeychainQueries(tt.username)
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %d queries, want %d: %q", tt.name, len(got), len(tt.want), got)
			continue
		}
		for i := range got {
			if strings.Join(got[i], " ") != strings.Join(tt.want[i], " ") {
				t.Errorf("%s: query %d = %q, want %q", tt.name, i, got[i], tt.want[i])
			}
		}
	}
}

func TestParseClaudeOauth(t *testing.T) {
	tests := []struct {
		name string
		data string
		want *claudeOauthCreds
	}{
		{
			"valid payload",
			`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-x","refreshToken":"sk-ant-ort01-y","expiresAt":123}}`,
			&claudeOauthCreds{AccessToken: "sk-ant-oat01-x", RefreshToken: "sk-ant-ort01-y", ExpiresAt: 123},
		},
		{"missing access token", `{"claudeAiOauth":{"refreshToken":"y"}}`, nil},
		{"no oauth object", `{"other":1}`, nil},
		{"malformed json", `{`, nil},
	}
	for _, tt := range tests {
		got := parseClaudeOauth([]byte(tt.data))
		if tt.want == nil || got == nil {
			if (tt.want == nil) != (got == nil) {
				t.Errorf("%s: parseClaudeOauth() = %v, want %v", tt.name, got, tt.want)
			}
			continue
		}
		if *got != *tt.want {
			t.Errorf("%s: parseClaudeOauth() = %+v, want %+v", tt.name, *got, *tt.want)
		}
	}
}

func TestMergeClaudeCreds(t *testing.T) {
	raw := []byte(`{"claudeAiOauth":{"accessToken":"old-at","refreshToken":"old-rt","expiresAt":1,"scopes":["user:inference"],"subscriptionType":"pro","rateLimitTier":"default_claude_ai"},"extra":"keep-me"}`)
	fresh := &claudeOauthCreds{AccessToken: "new-at", RefreshToken: "new-rt", ExpiresAt: 2}
	merged, err := mergeClaudeCreds(raw, fresh)
	if err != nil {
		t.Fatalf("mergeClaudeCreds() error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatalf("merged output is not json: %v", err)
	}
	if got["extra"] != "keep-me" {
		t.Errorf("top-level key lost: extra = %v", got["extra"])
	}
	oauth, _ := got["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		t.Fatalf("claudeAiOauth missing in merged output: %s", merged)
	}
	for k, want := range map[string]any{
		"accessToken": "new-at", "refreshToken": "new-rt", "expiresAt": float64(2),
		"scopes": []any{"user:inference"}, "subscriptionType": "pro", "rateLimitTier": "default_claude_ai",
	} {
		if !reflect.DeepEqual(oauth[k], want) {
			t.Errorf("claudeAiOauth[%q] = %v, want %v", k, oauth[k], want)
		}
	}
	if _, err := mergeClaudeCreds([]byte(`{`), fresh); err == nil {
		t.Error("mergeClaudeCreds() should error on malformed input")
	}
}

func TestParseClaudeUsageResetsPerLimit(t *testing.T) {
	session := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	weekly := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	body := fmt.Sprintf(`{"limits":[
		{"kind":"session","percent":12,"resets_at":%q},
		{"kind":"weekly_all","utilization":41,"resets_at":%q},
		{"kind":"unknown","percent":99}
	]}`, session.Format(time.RFC3339), weekly.Format(time.RFC3339))

	p := parseClaudeUsage([]byte(body))
	if p == nil || len(p.Metrics) != 2 {
		t.Fatalf("parseClaudeUsage() = %+v, want 2 metrics", p)
	}
	if got, want := p.Metrics[0].ResetIn, formatResetTime(session.Unix()); got != want {
		t.Errorf("session ResetIn = %q, want %q", got, want)
	}
	if got, want := p.Metrics[1].ResetIn, formatResetTime(weekly.Unix()); got != want {
		t.Errorf("weekly ResetIn = %q, want %q", got, want)
	}
	if p.Metrics[0].ResetIn == p.Metrics[1].ResetIn {
		t.Errorf("session and weekly should reset at different times, both %q", p.Metrics[0].ResetIn)
	}
	if p.ResetIn != p.Metrics[0].ResetIn {
		t.Errorf("provider ResetIn = %q, want nearest %q", p.ResetIn, p.Metrics[0].ResetIn)
	}
}

func TestParseClaudeUsageWithoutResetTime(t *testing.T) {
	p := parseClaudeUsage([]byte(`{"limits":[{"kind":"session","percent":12}]}`))
	if p == nil || len(p.Metrics) != 1 {
		t.Fatalf("parseClaudeUsage() = %+v, want 1 metric", p)
	}
	if p.Metrics[0].ResetIn != "" || p.ResetIn != "" {
		t.Errorf("expected no reset time, got metric %q provider %q", p.Metrics[0].ResetIn, p.ResetIn)
	}
}

func TestParseZaiUsageResetsPerLimit(t *testing.T) {
	soon := time.Now().Add(3 * time.Hour)
	later := time.Now().Add(5 * 24 * time.Hour)
	body := fmt.Sprintf(`{"data":{"limits":[
		{"type":"TIME_LIMIT","unit":3,"number":5,"percentage":20,"nextResetTime":%d},
		{"type":"TOKENS_LIMIT","unit":6,"number":1,"percentage":60,"nextResetTime":%d},
		{"type":"OTHER","percentage":5,"nextResetTime":%d}
	]}}`, soon.UnixMilli(), later.UnixMilli(), time.Now().Add(-time.Hour).UnixMilli())

	p := parseZaiUsage([]byte(body))
	if p == nil || len(p.Metrics) != 3 {
		t.Fatalf("parseZaiUsage() = %+v, want 3 metrics", p)
	}
	if got, want := p.Metrics[0].ResetIn, formatResetTime(soon.UnixMilli()/1000); got != want {
		t.Errorf("Queries ResetIn = %q, want %q", got, want)
	}
	if got, want := p.Metrics[1].ResetIn, formatResetTime(later.UnixMilli()/1000); got != want {
		t.Errorf("Tokens ResetIn = %q, want %q", got, want)
	}
	if p.Metrics[2].ResetIn != "" {
		t.Errorf("a reset in the past should be left blank, got %q", p.Metrics[2].ResetIn)
	}
	if p.ResetEpoch != soon.UnixMilli()/1000 {
		t.Errorf("provider ResetEpoch = %d, want nearest %d", p.ResetEpoch, soon.UnixMilli()/1000)
	}
}
