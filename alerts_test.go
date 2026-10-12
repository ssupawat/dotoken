package main

import (
	"reflect"
	"testing"
)

func usage(name string, pct float64) []ProviderUsage {
	return []ProviderUsage{{Name: name, Metrics: []Metric{{Label: "Session", Pct: pct, ResetAt: "3:00pm"}}}}
}

func titles(alerts []alert) []string {
	var out []string
	for _, a := range alerts {
		out = append(out, a.Title)
	}
	return out
}

func TestAlertTracker(t *testing.T) {
	a := newAlertTracker()
	steps := []struct {
		name      string
		providers []ProviderUsage
		want      []string
	}{
		{"below levels", usage("Claude", 50), nil},
		{"loading placeholder", usage("Claude", -1), nil},
		{"crosses 80", usage("Claude", 81.6), []string{"Claude Session at 82%"}},
		{"same level again", usage("Claude", 88), nil},
		{"failed fetch keeps state", nil, nil},
		{"crosses 95", usage("Claude", 96), []string{"Claude Session at 96%"}},
		{"dips between levels", usage("Claude", 85), nil},
		{"back above 95 stays quiet", usage("Claude", 97), nil},
		{"window resets", usage("Claude", 3), []string{"Claude Session reset"}},
		{"reset only once", usage("Claude", 4), nil},
		{"jumps straight to 95", usage("Claude", 99), []string{"Claude Session at 99%"}},
		{"signed out", []ProviderUsage{{Name: "Claude", Expired: true, ResetIn: "run: claude /login"}}, []string{"Claude needs sign-in"}},
		{"still signed out", []ProviderUsage{{Name: "Claude", Expired: true}}, nil},
		{"signed back in", usage("Claude", 99), nil},
		{"signed out again", []ProviderUsage{{Name: "Claude", Expired: true}}, []string{"Claude needs sign-in"}},
	}
	for _, s := range steps {
		if got := titles(a.check(s.providers)); !reflect.DeepEqual(got, s.want) {
			t.Errorf("%s: alerts = %q, want %q", s.name, got, s.want)
		}
	}
}

func TestAlertTrackerKeysPerMetric(t *testing.T) {
	a := newAlertTracker()
	got := titles(a.check([]ProviderUsage{
		{Name: "Claude", Metrics: []Metric{{Label: "Session", Pct: 90}, {Label: "Weekly", Pct: 90}}},
		{Name: "Z.ai", Metrics: []Metric{{Label: "Session", Pct: 90}}},
	}))
	want := []string{"Claude Session at 90%", "Claude Weekly at 90%", "Z.ai Session at 90%"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("alerts = %q, want %q", got, want)
	}
}

func TestAlertDetail(t *testing.T) {
	tests := []struct {
		m    Metric
		want string
	}{
		{Metric{}, "Approaching the limit."},
		{Metric{ResetAt: "3:00pm"}, "Resets 3:00pm"},
		{Metric{ResetAt: "3:00pm", RunOut: "1:10pm"}, "Runs out at 1:10pm at this rate, resets 3:00pm"},
	}
	for _, tt := range tests {
		if got := alertDetail(tt.m); got != tt.want {
			t.Errorf("alertDetail(%+v) = %q, want %q", tt.m, got, tt.want)
		}
	}
}

func TestAppleScriptString(t *testing.T) {
	got := appleScriptString("say \"hi\"\\ now\nok")
	want := `"say \"hi\"\\ now ok"`
	if got != want {
		t.Errorf("appleScriptString = %s, want %s", got, want)
	}
}
