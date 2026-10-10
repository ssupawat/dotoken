package main

import "testing"

func TestTrayLabel(t *testing.T) {
	tests := []struct {
		name      string
		providers []ProviderUsage
		want      string
	}{
		{"none", nil, ""},
		{"peak across providers", []ProviderUsage{
			{Name: "Claude", Metrics: []Metric{{Label: "Session", Pct: 41.6}, {Label: "Weekly", Pct: 12}}},
			{Name: "Z.ai", Metrics: []Metric{{Label: "Tokens", Pct: 7}}},
		}, "42%"},
		{"loading placeholder ignored", []ProviderUsage{
			{Name: "Claude", Metrics: []Metric{{Label: "Session", Pct: -1}}},
		}, ""},
		{"expired flagged", []ProviderUsage{
			{Name: "Claude", Expired: true},
			{Name: "OpenCode", Metrics: []Metric{{Label: "Weekly", Pct: 90}}},
		}, "90%!"},
		{"only expired", []ProviderUsage{{Name: "Claude", Expired: true}}, "!"},
	}
	for _, tt := range tests {
		if got := trayLabel(tt.providers); got != tt.want {
			t.Errorf("%s: trayLabel() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTrayTooltip(t *testing.T) {
	got := trayTooltip([]ProviderUsage{
		{Name: "Claude", Metrics: []Metric{{Label: "Session", Pct: 41.6}, {Label: "Weekly", Pct: 12}}},
		{Name: "OpenCode", Expired: true},
		{Name: "Z.ai", Metrics: []Metric{{Label: "Tokens", Pct: -1}}},
	})
	want := "Claude: Session 42% · Weekly 12%\nOpenCode: sign-in needed"
	if got != want {
		t.Errorf("trayTooltip() = %q, want %q", got, want)
	}
	if got := trayTooltip(nil); got != "DoToken" {
		t.Errorf("trayTooltip(nil) = %q, want DoToken", got)
	}
}
