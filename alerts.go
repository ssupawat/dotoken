package main

import (
	"fmt"
	"log"
	"math"
	"os/exec"
	"strings"
)

// Usage levels that trigger a notification, lowest first. Once a metric has
// alerted it stays quiet until usage drops back below the lowest level (its
// window reset), so a limit hovering around a threshold doesn't re-alert.
var alertLevels = []float64{80, 95}

type alert struct {
	Title string
	Body  string
}

// alertTracker remembers, per provider metric, the highest level already
// alerted, and which providers have already been reported as signed out.
// Metrics missing from a refresh (a failed fetch) keep their state.
type alertTracker struct {
	notified map[string]float64
	expired  map[string]bool
}

func newAlertTracker() *alertTracker {
	return &alertTracker{notified: map[string]float64{}, expired: map[string]bool{}}
}

// check compares a refresh against what was already alerted and returns the
// notifications to send.
func (a *alertTracker) check(providers []ProviderUsage) []alert {
	var out []alert
	for _, p := range providers {
		if p.Expired {
			if !a.expired[p.Name] {
				a.expired[p.Name] = true
				out = append(out, alert{Title: p.Name + " needs sign-in", Body: p.ResetIn})
			}
			continue
		}
		delete(a.expired, p.Name)

		for _, m := range p.Metrics {
			if m.Pct < 0 {
				continue // loading placeholder
			}
			key := p.Name + "/" + m.Label
			prev := a.notified[key]
			level := 0.0
			for _, l := range alertLevels {
				if m.Pct >= l {
					level = l
				}
			}

			switch {
			case level > prev:
				a.notified[key] = level
				out = append(out, alert{
					Title: fmt.Sprintf("%s %s at %d%%", p.Name, m.Label, int(math.Round(m.Pct))),
					Body:  alertDetail(m),
				})
			case level == 0 && prev > 0:
				delete(a.notified, key)
				out = append(out, alert{
					Title: fmt.Sprintf("%s %s reset", p.Name, m.Label),
					Body:  fmt.Sprintf("Back to %d%% — you're clear to go.", int(math.Round(m.Pct))),
				})
			}
		}
	}
	return out
}

func alertDetail(m Metric) string {
	var parts []string
	if m.RunOut != "" {
		parts = append(parts, "runs out at "+m.RunOut+" at this rate")
	}
	if m.ResetAt != "" {
		parts = append(parts, "resets "+m.ResetAt)
	}
	if len(parts) == 0 {
		return "Approaching the limit."
	}
	s := strings.Join(parts, ", ")
	return strings.ToUpper(s[:1]) + s[1:]
}

var usageAlerts = newAlertTracker()

// notifyUsage sends macOS notifications for limits that crossed an alert
// level, reset, or lost their sign-in. Callers must hold usageMu.
func notifyUsage(providers []ProviderUsage, muted bool) {
	alerts := usageAlerts.check(providers)
	if muted {
		return // still track state so unmuting doesn't replay old alerts
	}
	for _, al := range alerts {
		go sendNotification(al)
	}
}

func sendNotification(al alert) {
	script := fmt.Sprintf("display notification %s with title %s subtitle %s",
		appleScriptString(al.Body), appleScriptString("DoToken"), appleScriptString(al.Title))
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Printf("notify: %v", err)
	}
}

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ", "\r", " ")
	return `"` + r.Replace(s) + `"`
}
