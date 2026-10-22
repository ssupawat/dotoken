package main

import "testing"

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

func TestParseKimiUsageCounters(t *testing.T) {
	body := []byte(`{
		"usage": { "limit": "1024", "used": "256", "remaining": "768", "resetTime": "2026-10-27T00:00:00.000Z" },
		"limits": [
			{ "window": { "duration": 300, "timeUnit": "TIME_UNIT_MINUTE" },
			  "detail": { "limit": 200, "remaining": 50, "resetTime": "2026-10-22T23:00:00.123Z" } }
		]
	}`)

	p := parseKimiUsage(body)
	if p == nil {
		t.Fatal("expected provider usage, got nil")
	}
	if p.Name != "Kimi" {
		t.Errorf("Name = %q, want Kimi", p.Name)
	}
	want := []Metric{
		{Label: "Session", Pct: 75, WindowMinutes: 300},
		{Label: "Weekly", Pct: 25, WindowMinutes: 7 * 24 * 60},
	}
	if len(p.Metrics) != len(want) {
		t.Fatalf("got %d metrics, want %d: %+v", len(p.Metrics), len(want), p.Metrics)
	}
	for i, w := range want {
		if p.Metrics[i] != w {
			t.Errorf("metric[%d] = %+v, want %+v", i, p.Metrics[i], w)
		}
	}
	wantReset := int64(1792710000) // 2026-10-22T23:00:00Z, the session reset
	if p.ResetEpoch != wantReset {
		t.Errorf("ResetEpoch = %d, want %d", p.ResetEpoch, wantReset)
	}
}

func TestParseKimiUsageRatioPools(t *testing.T) {
	body := []byte(`{
		"usage": { "limit": "100", "used": "10", "resetTime": "2026-10-27T00:00:00Z" },
		"usages": {
			"limit_5h": { "used_ratio": 0.4, "reset_time": "2026-10-22T23:00:00Z" },
			"limit_7d": { "used_ratio": 0.05, "reset_time": "2026-10-27T00:00:00Z" },
			"limit_month_total": { "used_ratio": 1.3, "reset_time": "2026-11-01T00:00:00Z" }
		}
	}`)

	p := parseKimiUsage(body)
	if p == nil {
		t.Fatal("expected provider usage, got nil")
	}
	want := []Metric{
		{Label: "Session", Pct: 40, WindowMinutes: 300},
		// The weekly counter reports more usage than the ratio pool, so it wins
		{Label: "Weekly", Pct: 10, WindowMinutes: 7 * 24 * 60},
		// Ratios over 1 are capped at 100%
		{Label: "Monthly", Pct: 100, WindowMinutes: 30 * 24 * 60},
	}
	if len(p.Metrics) != len(want) {
		t.Fatalf("got %d metrics, want %d: %+v", len(p.Metrics), len(want), p.Metrics)
	}
	for i, w := range want {
		if p.Metrics[i] != w {
			t.Errorf("metric[%d] = %+v, want %+v", i, p.Metrics[i], w)
		}
	}
}

func TestParseKimiUsageEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `not json`, `{"usage": {"limit": "0", "used": "0"}}`} {
		if p := parseKimiUsage([]byte(body)); p != nil {
			t.Errorf("parseKimiUsage(%s) = %+v, want nil", body, p)
		}
	}
}

func TestKimiWindowMinutes(t *testing.T) {
	tests := []struct {
		duration int
		unit     string
		want     int
	}{
		{300, "TIME_UNIT_MINUTE", 300},
		{5, "TIME_UNIT_HOUR", 300},
		{7, "TIME_UNIT_DAY", 7 * 24 * 60},
		{1, "TIME_UNIT_UNKNOWN", 0},
		{0, "TIME_UNIT_MINUTE", 0},
	}
	for _, tt := range tests {
		if got := kimiWindowMinutes(tt.duration, tt.unit); got != tt.want {
			t.Errorf("kimiWindowMinutes(%d, %q) = %d, want %d", tt.duration, tt.unit, got, tt.want)
		}
	}
}
