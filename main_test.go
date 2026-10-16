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

func TestLastKnown(t *testing.T) {
	cached := []ProviderUsage{
		{Name: "OpenCode", FetchedAt: "14:05", Metrics: []Metric{{Label: "5h rolling", Pct: 42, WindowMinutes: 300}}},
		{Name: "Z.ai", Expired: true, Metrics: []Metric{}},
		{Name: "Claude", Metrics: []Metric{{Label: "Session", Pct: -1}}},
	}

	got := lastKnown("OpenCode", cached)
	if got == nil {
		t.Fatal("lastKnown(OpenCode) = nil, want the cached numbers")
	}
	if !got.Stale || got.FetchedAt != "14:05" || got.Metrics[0].Pct != 42 {
		t.Errorf("lastKnown(OpenCode) = %+v, want stale copy fetched at 14:05 with 42%%", got)
	}
	if cached[0].Stale {
		t.Error("lastKnown modified the cache entry")
	}
	got.Metrics[0].Pct = 0
	if cached[0].Metrics[0].Pct != 42 {
		t.Error("lastKnown shares the metrics slice with the cache")
	}

	if got := lastKnown("Z.ai", cached); got != nil {
		t.Errorf("lastKnown(expired) = %+v, want nil so the sign-in warning is not hidden", got)
	}
	if got := lastKnown("Claude", cached); got != nil {
		t.Errorf("lastKnown(loading placeholder) = %+v, want nil", got)
	}
	if got := lastKnown("Codex", cached); got != nil {
		t.Errorf("lastKnown(never fetched) = %+v, want nil", got)
	}
}

func TestStaleProvidersStayInTrayLabels(t *testing.T) {
	providers := []ProviderUsage{
		{Name: "OpenCode", Stale: true, FetchedAt: "14:05", Metrics: []Metric{{Pct: 42, WindowMinutes: 300}}},
	}
	if labels := trayLabels(providers); len(labels) != 1 {
		t.Errorf("trayLabels() = %q, want the stale provider kept", labels)
	}
}
