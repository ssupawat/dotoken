package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestPriceFor(t *testing.T) {
	tests := []struct {
		model string
		want  modelPrice
		ok    bool
	}{
		{"claude-opus-5-5", modelPrice{4, 20, 0.20}, true},
		{"claude-opus-5", modelPrice{5, 25, 0.50}, true},
		{"claude-opus-4-5-20251101", modelPrice{5, 25, 0.50}, true},
		{"claude-opus-4-20250514", modelPrice{15, 75, 1.50}, true},
		{"claude-sonnet-4-5-20250929", modelPrice{3, 15, 0.30}, true},
		{"claude-sonnet-4-6[1m]", modelPrice{3, 15, 0.30}, true},
		{"claude-haiku-4-5-20251001", modelPrice{1, 5, 0.10}, true},
		{"anthropic.claude-sonnet-5-5", modelPrice{2, 10, 0.20}, true},
		// A newer point release must not fall back to an older, pricier family
		{"claude-opus-4-9", modelPrice{}, false},
		{"<synthetic>", modelPrice{}, false},
	}
	for _, tt := range tests {
		got, ok := priceFor(tt.model)
		if ok != tt.ok || got != tt.want {
			t.Errorf("priceFor(%q) = %v, %v; want %v, %v", tt.model, got, ok, tt.want, tt.ok)
		}
	}
}

func TestUsageCost(t *testing.T) {
	p := modelPrice{Input: 3, Output: 15, CacheRead: 0.30}
	// Without the per-TTL breakdown, cache writes are billed at the 5-minute rate
	u := ccUsage{InputTokens: 1e6, OutputTokens: 1e6, CacheCreationInputTokens: 1e6, CacheReadInputTokens: 1e6}
	if got, want := u.cost(p), 3+15+0.30+3.75; math.Abs(got-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", got, want)
	}
	u.CacheCreation = &struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	}{Ephemeral1h: 1e6}
	if got, want := u.cost(p), 3+15+0.30+6.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("cost with 1h cache writes = %v, want %v", got, want)
	}
}

func TestParseAndSummarizeSpend(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.Local)
	ts := func(d time.Time) string { return d.UTC().Format(time.RFC3339) }
	today := now.Add(-time.Hour)
	threeDaysAgo := now.AddDate(0, 0, -3)
	lastMonth := now.AddDate(0, -1, 0)

	log := strings.Join([]string{
		`{"type":"user","timestamp":"` + ts(today) + `","message":{"role":"user","content":"hi"}}`,
		// Streamed message logged twice with the same usage: counted once
		`{"type":"assistant","timestamp":"` + ts(today) + `","requestId":"req_1","message":{"id":"msg_1","model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":1000000,"output_tokens":0}}}`,
		`{"type":"assistant","timestamp":"` + ts(today) + `","requestId":"req_1","message":{"id":"msg_1","model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":1000000,"output_tokens":0}}}`,
		`{"type":"assistant","timestamp":"` + ts(threeDaysAgo) + `","requestId":"req_2","message":{"id":"msg_2","model":"claude-opus-5-5","usage":{"input_tokens":0,"output_tokens":1000000}}}`,
		`{"type":"assistant","timestamp":"` + ts(lastMonth) + `","requestId":"req_3","message":{"id":"msg_3","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":5}}}`,
		`{"type":"assistant","timestamp":"` + ts(today) + `","message":{"id":"msg_4","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`not json "usage"`,
		// Last line without a trailing newline is still read
		`{"type":"assistant","timestamp":"` + ts(today) + `","requestId":"req_5","message":{"id":"msg_5","model":"claude-opus-9","usage":{"input_tokens":10,"output_tokens":0}}}`,
	}, "\n")

	got := summarizeSpend(parseTranscript(strings.NewReader(log)), now)
	want := LocalSpend{
		TodayTokens: 1000010,
		TodayCost:   3,
		WeekTokens:  2000010,
		WeekCost:    23,
		Unpriced:    true,
	}
	if got.TodayTokens != want.TodayTokens || got.WeekTokens != want.WeekTokens || got.Unpriced != want.Unpriced ||
		math.Abs(got.TodayCost-want.TodayCost) > 1e-9 || math.Abs(got.WeekCost-want.WeekCost) > 1e-9 {
		t.Errorf("summarizeSpend() = %+v, want %+v", got, want)
	}
}
