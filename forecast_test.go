package main

import (
	"testing"
	"time"
)

func TestApplyForecast(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	window := 5 * time.Hour

	tests := []struct {
		name       string
		pct        float64
		reset      time.Time
		window     time.Duration
		wantPace   float64
		wantRunOut time.Time // zero = no forecast
	}{
		// 1h into a 5h window: even pace is 20%
		{"on pace", 20, now.Add(4 * time.Hour), window, 20, time.Time{}},
		{"below pace", 10, now.Add(4 * time.Hour), window, 20, time.Time{}},
		// 40% used in 1h → 100% after 2.5h, 2.5h before reset
		{"ahead of pace", 40, now.Add(4 * time.Hour), window, 20, now.Add(90 * time.Minute)},
		{"limit reached", 100, now.Add(4 * time.Hour), window, 20, time.Time{}},
		// 6 min into the window is under 5% elapsed: too early to call
		{"too early", 50, now.Add(window - 6*time.Minute), window, 2, time.Time{}},
		{"low usage", 8, now.Add(4 * time.Hour), window, 20, time.Time{}},
		{"unknown window", 40, now.Add(4 * time.Hour), 0, 0, time.Time{}},
		{"reset in past", 40, now.Add(-time.Minute), window, 0, time.Time{}},
	}
	for _, tt := range tests {
		m := Metric{Label: "x", Pct: tt.pct}
		applyForecast(&m, tt.reset, tt.window, now)
		if m.ResetEpoch != tt.reset.Unix() || m.ResetAt == "" {
			t.Errorf("%s: reset = %d %q, want %d", tt.name, m.ResetEpoch, m.ResetAt, tt.reset.Unix())
		}
		if diff := m.Pace - tt.wantPace; diff > 0.01 || diff < -0.01 {
			t.Errorf("%s: pace = %.2f, want %.2f", tt.name, m.Pace, tt.wantPace)
		}
		var wantEpoch int64
		if !tt.wantRunOut.IsZero() {
			wantEpoch = tt.wantRunOut.Unix()
		}
		if m.RunOutEpoch != wantEpoch || (wantEpoch != 0) != (m.RunOut != "") {
			t.Errorf("%s: runOut = %d %q, want %d", tt.name, m.RunOutEpoch, m.RunOut, wantEpoch)
		}
	}
}

func TestApplyForecastNoReset(t *testing.T) {
	m := Metric{Pct: 50}
	applyForecast(&m, time.Time{}, 5*time.Hour, time.Now())
	if m.ResetEpoch != 0 || m.Pace != 0 || m.RunOut != "" {
		t.Errorf("expected no forecast without a reset time, got %+v", m)
	}
}

func TestMonthWindow(t *testing.T) {
	reset := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if got := monthWindow(reset); got != 28*24*time.Hour {
		t.Errorf("monthWindow(Mar 1) = %v, want 672h", got)
	}
}
