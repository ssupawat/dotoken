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

func TestParseCopilotUsage(t *testing.T) {
	t.Run("paid plan skips unlimited quotas", func(t *testing.T) {
		body := []byte(`{
			"copilot_plan": "individual",
			"quota_reset_date": "2026-11-01",
			"quota_snapshots": {
				"chat": {"entitlement": 0, "remaining": 0, "percent_remaining": 100, "unlimited": true},
				"completions": {"entitlement": 0, "remaining": 0, "percent_remaining": 100, "unlimited": true},
				"premium_interactions": {"entitlement": 300, "remaining": 75, "percent_remaining": 25, "unlimited": false}
			}
		}`)
		got := parseCopilotUsage(body)
		if got == nil {
			t.Fatal("got nil")
		}
		if got.Name != "Copilot" || len(got.Metrics) != 1 {
			t.Fatalf("got %+v", got)
		}
		m := got.Metrics[0]
		if m.Label != "Premium" || m.Pct != 75 || m.Used != 225 || m.Max != 300 || m.WindowMinutes != 30*24*60 {
			t.Errorf("got %+v", m)
		}
		if want := int64(1793491200); got.ResetEpoch != want {
			t.Errorf("reset epoch = %d, want %d", got.ResetEpoch, want)
		}
	})

	t.Run("free plan uses counts", func(t *testing.T) {
		body := []byte(`{
			"limited_user_reset_date": "2026-11-05",
			"limited_user_quotas": {"chat": 40, "completions": 1500},
			"monthly_quotas": {"chat": 50, "completions": 2000}
		}`)
		got := parseCopilotUsage(body)
		if got == nil || len(got.Metrics) != 2 {
			t.Fatalf("got %+v", got)
		}
		if m := got.Metrics[0]; m.Label != "Chat" || m.Pct != 20 {
			t.Errorf("chat = %+v", m)
		}
		if m := got.Metrics[1]; m.Label != "Completions" || m.Pct != 25 {
			t.Errorf("completions = %+v", m)
		}
	})

	t.Run("nothing metered", func(t *testing.T) {
		body := []byte(`{"quota_snapshots": {"premium_interactions": {"unlimited": true}}}`)
		if got := parseCopilotUsage(body); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("bad json", func(t *testing.T) {
		if got := parseCopilotUsage([]byte(`<html>`)); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
}
