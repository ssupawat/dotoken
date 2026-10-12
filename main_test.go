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
		{"highest across providers", []ProviderUsage{
			{Name: "Claude", Metrics: []Metric{{Pct: 12.4}, {Pct: 41}}},
			{Name: "Z.ai", Metrics: []Metric{{Pct: 72.6}}},
		}, "73%"},
		{"zero usage", []ProviderUsage{
			{Name: "Z.ai", Metrics: []Metric{{Pct: 0}}},
		}, "0%"},
		{"expired only", []ProviderUsage{
			{Name: "Claude", Expired: true, Metrics: []Metric{}},
		}, "!"},
		{"expired alongside usage", []ProviderUsage{
			{Name: "Claude", Expired: true, Metrics: []Metric{}},
			{Name: "OpenCode", Metrics: []Metric{{Pct: 55}}},
		}, "55% !"},
	}
	for _, tt := range tests {
		if got := trayLabel(tt.providers); got != tt.want {
			t.Errorf("%s: trayLabel() = %q, want %q", tt.name, got, tt.want)
		}
	}
}
