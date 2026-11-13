package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const zaiOK = `{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":42}]}}`

func zaiServer(t *testing.T, status int, body string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if r.URL.Path != "/api/monitor/usage/quota/limit" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withZaiHosts(t *testing.T, hosts ...string) {
	t.Helper()
	saved := zaiHosts
	zaiHosts = hosts
	zaiHost.Store(0)
	t.Setenv("ZAI_TOKEN", "key")
	t.Cleanup(func() { zaiHosts = saved; zaiHost.Store(0) })
}

func TestZaiRejected(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   bool
	}{
		{401, ``, true},
		{403, `{}`, true},
		{200, `{"code":1002,"msg":"invalid token","success":false}`, true},
		{200, zaiOK, false},
		{500, `{"code":500}`, false},
		{200, `not json`, false},
	}
	for _, tt := range tests {
		if got := zaiRejected(tt.status, []byte(tt.body)); got != tt.want {
			t.Errorf("zaiRejected(%d, %s) = %v, want %v", tt.status, tt.body, got, tt.want)
		}
	}
}

func TestFetchZaiUsageFallsBackToBigModel(t *testing.T) {
	var zHits, bmHits int
	z := zaiServer(t, 401, `{"code":1001}`, &zHits)
	bm := zaiServer(t, 200, zaiOK, &bmHits)
	withZaiHosts(t, z.URL, bm.URL)

	got := fetchZaiUsage()
	if got == nil || got.Expired || len(got.Metrics) != 1 || got.Metrics[0].Pct != 42 || got.Metrics[0].WindowMinutes != 300 {
		t.Fatalf("fetchZaiUsage() = %+v", got)
	}
	fetchZaiUsage()
	if zHits != 1 || bmHits != 2 {
		t.Errorf("hits z.ai=%d bigmodel=%d, want 1 and 2", zHits, bmHits)
	}
}

func TestFetchZaiUsageRejectedEverywhereIsExpired(t *testing.T) {
	var zHits, bmHits int
	z := zaiServer(t, 401, ``, &zHits)
	bm := zaiServer(t, 200, `{"code":1003,"success":false}`, &bmHits)
	withZaiHosts(t, z.URL, bm.URL)

	got := fetchZaiUsage()
	if got == nil || !got.Expired || got.Name != "Z.ai" {
		t.Fatalf("fetchZaiUsage() = %+v, want expired Z.ai", got)
	}
	if label := trayLabel([]ProviderUsage{*got}); label != ansiRed+"Z !"+ansiReset {
		t.Errorf("trayLabel() = %q", label)
	}
}

func TestFetchZaiUsageServerErrorIsSkipped(t *testing.T) {
	var zHits, bmHits int
	z := zaiServer(t, 502, `bad gateway`, &zHits)
	bm := zaiServer(t, 200, zaiOK, &bmHits)
	withZaiHosts(t, z.URL, bm.URL)

	if got := fetchZaiUsage(); got != nil {
		t.Errorf("fetchZaiUsage() = %+v, want nil", got)
	}
	if bmHits != 0 {
		t.Errorf("bigmodel hit %d times, want 0", bmHits)
	}
}
