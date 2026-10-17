package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFetchCodex(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acc" {
			t.Errorf("ChatGPT-Account-Id = %q", got)
		}
		if r.URL.Path == "/rate-limit-reset-credits" {
			w.Write([]byte(`{
				"credits": [
					{"status": "available", "title": "Full reset", "expires_at": "2026-10-29T17:34:32.549434Z"},
					{"status": "redeemed", "title": "Full reset", "expires_at": "2026-10-20T00:00:00Z"},
					{"status": "available", "title": "Full reset", "expires_at": "2026-10-22T19:24:29.124029Z"}
				],
				"available_count": 2
			}`))
			return
		}
		w.Write([]byte(`{
			"plan_type": "plus",
			"rate_limit": {
				"primary_window": {"used_percent": 25, "limit_window_seconds": 18000, "reset_after_seconds": 3600, "reset_at": 1792238400},
				"secondary_window": {"used_percent": 40.5, "limit_window_seconds": 604800.0, "reset_after_seconds": 7200}
			},
			"additional_rate_limits": [
				{"limit_name": "codex_other", "rate_limit": {"primary_window": {"used_percent": 10, "limit_window_seconds": 18000, "reset_at": "2026-10-17T15:00:00Z"}}},
				{"limit_name": "codex_null", "rate_limit": {"primary_window": {"used_percent": 0, "limit_window_seconds": 18000, "reset_after_seconds": 600, "reset_at": null}}}
			]
		}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"tokens":{"access_token":"tok","account_id":"acc"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	origURL, origCreditsURL, origAuth := codexUsageURL, codexResetCreditsURL, *codexAuth
	codexUsageURL, codexResetCreditsURL, *codexAuth = srv.URL+"/usage", srv.URL+"/rate-limit-reset-credits", auth
	defer func() { codexUsageURL, codexResetCreditsURL, *codexAuth = origURL, origCreditsURL, origAuth }()

	r, err := fetchCodex(srv.Client(), now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Plan != "plus" {
		t.Errorf("Plan = %q, want plus", r.Plan)
	}

	want := []struct {
		name      string
		remaining float64
		resetsAt  time.Time
	}{
		{"5h", 75, time.Unix(1792238400, 0)},                                   // reset_at (unix seconds) is used
		{"weekly", 59.5, now.Add(2 * time.Hour)},                               // falls back to reset_after_seconds
		{"codex_other 5h", 90, time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)}, // reset_at as RFC3339 string
		{"codex_null 5h", 100, now.Add(10 * time.Minute)},                      // reset_at null falls back to reset_after_seconds
	}
	if len(r.Windows) != len(want) {
		t.Fatalf("got %d windows, want %d: %+v", len(r.Windows), len(want), r.Windows)
	}
	for i, w := range want {
		got := r.Windows[i]
		if got.Name != w.name || got.RemainingPercent != w.remaining || !got.ResetsAt.Equal(w.resetsAt) {
			t.Errorf("window[%d] = {%q %v %v}, want {%q %v %v}", i, got.Name, got.RemainingPercent, got.ResetsAt, w.name, w.remaining, w.resetsAt)
		}
	}

	// only available tickets, soonest expiry first
	wantCredits := []ResetCredit{
		{"Full reset", time.Date(2026, 10, 22, 19, 24, 29, 124029000, time.UTC), "5d07h24m"},
		{"Full reset", time.Date(2026, 10, 29, 17, 34, 32, 549434000, time.UTC), "12d05h35m"},
	}
	if len(r.ResetCredits) != len(wantCredits) {
		t.Fatalf("got %d reset credits, want %d: %+v", len(r.ResetCredits), len(wantCredits), r.ResetCredits)
	}
	for i, w := range wantCredits {
		got := r.ResetCredits[i]
		if got.Title != w.Title || !got.ExpiresAt.Equal(w.ExpiresAt) || got.ExpiresIn != w.ExpiresIn {
			t.Errorf("reset credit[%d] = %+v, want %+v", i, got, w)
		}
	}
}

func TestFetchCodexWithoutLogin(t *testing.T) {
	orig := *codexAuth
	*codexAuth = filepath.Join(t.TempDir(), "missing.json")
	defer func() { *codexAuth = orig }()

	if _, err := fetchCodex(http.DefaultClient, time.Now()); err == nil {
		t.Error("expected an error when auth.json is missing")
	}
}

func TestClaudeWindowName(t *testing.T) {
	for _, tc := range []struct {
		kind, model string
		want        string
	}{
		{"session", "", "5h session"},
		{"weekly_all", "", "weekly all"},
		{"weekly_scoped", "Fable", "weekly Fable"},
		{"session", "Fable", "5h session Fable"}, // a per-model session limit is not labeled weekly
		{"unknown", "", "unknown"},
	} {
		if got := claudeWindowName(tc.kind, tc.model); got != tc.want {
			t.Errorf("claudeWindowName(%q, %q) = %q, want %q", tc.kind, tc.model, got, tc.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "now"},
		{42 * time.Minute, "42m"},
		{3*time.Hour + 5*time.Minute, "3h05m"},
		{29*time.Hour + 20*time.Minute, "1d05h20m"},
	} {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
