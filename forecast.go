package main

import "time"

// Forecasts are suppressed until this much of the window has passed and usage
// is at least minForecastPct, so a burst right after a reset doesn't raise a
// false "runs out" alarm.
const (
	minForecastElapsed = 0.05
	minForecastPct     = 10.0
)

// applyForecast records when a metric's window resets and, if the window
// length is known, where usage would sit at an even pace (Pace) and when the
// limit runs out at the current rate (RunOut) — only if that's before reset.
func applyForecast(m *Metric, reset time.Time, window time.Duration, now time.Time) {
	if reset.IsZero() {
		return
	}
	m.ResetEpoch = reset.Unix()
	m.ResetAt = formatResetTime(m.ResetEpoch)

	if window <= 0 || m.Pct < 0 {
		return
	}
	start := reset.Add(-window)
	elapsed := now.Sub(start)
	if elapsed <= 0 || elapsed > window {
		return
	}
	frac := float64(elapsed) / float64(window)
	m.Pace = frac * 100

	if m.Pct >= 100 || m.Pct < minForecastPct || frac < minForecastElapsed {
		return
	}
	runOut := start.Add(time.Duration(float64(elapsed) * 100 / m.Pct))
	if runOut.Before(reset) {
		m.RunOutEpoch = runOut.Unix()
		m.RunOut = formatResetTime(m.RunOutEpoch)
	}
}

// monthWindow is the length of a calendar-month window ending at reset.
func monthWindow(reset time.Time) time.Duration {
	return reset.Sub(reset.AddDate(0, -1, 0))
}
