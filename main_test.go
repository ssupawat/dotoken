package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestParseStatusPage(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    ServiceStatus
		wantErr bool
	}{
		{"operational", `{"page":{"id":"x"},"status":{"indicator":"none","description":"All Systems Operational"}}`, ServiceStatus{}, false},
		{"minor incident", `{"status":{"indicator":"minor","description":"Partially Degraded Service"}}`, ServiceStatus{"minor", "Partially Degraded Service"}, false},
		{"major outage", `{"status":{"indicator":"major","description":"Partial System Outage"}}`, ServiceStatus{"major", "Partial System Outage"}, false},
		{"missing indicator", `{"status":{}}`, ServiceStatus{}, true},
		{"not json", `<html>`, ServiceStatus{}, true},
	}
	for _, tt := range tests {
		got, err := parseStatusPage([]byte(tt.body))
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: parseStatusPage() error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("%s: parseStatusPage() = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestApplyServiceStatus(t *testing.T) {
	saved := serviceStatus
	defer func() { serviceStatus = saved }()
	serviceStatus = map[string]ServiceStatus{"Claude": {"major", "Partial System Outage"}}

	providers := []ProviderUsage{
		{Name: "Claude"},
		{Name: "Z.ai", StatusIndicator: "minor", StatusText: "stale"},
	}
	applyServiceStatus(providers)
	if providers[0].StatusIndicator != "major" || providers[0].StatusText != "Partial System Outage" {
		t.Errorf("Claude status = %q/%q, want major/Partial System Outage", providers[0].StatusIndicator, providers[0].StatusText)
	}
	if providers[1].StatusIndicator != "" || providers[1].StatusText != "" {
		t.Errorf("Z.ai status should be cleared, got %q/%q", providers[1].StatusIndicator, providers[1].StatusText)
	}
}

func TestRefreshServiceStatusKeepsLastKnownOnFailure(t *testing.T) {
	savedPages, savedStatus := statusPages, serviceStatus
	defer func() { statusPages, serviceStatus = savedPages, savedStatus }()

	body := `{"status":{"indicator":"major","description":"Partial System Outage"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body == "" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	statusPages = map[string]string{"Claude": srv.URL}
	serviceStatus = map[string]ServiceStatus{}

	statusCheckedAt.Store(0)
	refreshServiceStatus()
	if got := serviceStatus["Claude"]; got.Indicator != "major" {
		t.Fatalf("after outage, status = %+v, want major", got)
	}

	// A failing status page must not clear the incident
	body = ""
	statusCheckedAt.Store(0)
	refreshServiceStatus()
	if got := serviceStatus["Claude"]; got.Indicator != "major" {
		t.Errorf("after failed check, status = %+v, want last known major", got)
	}

	// Checks inside the throttle window are skipped
	body = `{"status":{"indicator":"none","description":"All Systems Operational"}}`
	refreshServiceStatus()
	if got := serviceStatus["Claude"]; got.Indicator != "major" {
		t.Errorf("throttled check changed status to %+v", got)
	}
	statusCheckedAt.Store(0)
	refreshServiceStatus()
	if got := serviceStatus["Claude"]; got != (ServiceStatus{}) {
		t.Errorf("after recovery, status = %+v, want operational", got)
	}
}
