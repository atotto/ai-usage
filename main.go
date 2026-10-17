package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	jsonOut     = flag.Bool("json", false, "print machine-readable JSON")
	timeout     = flag.Duration("timeout", 15*time.Second, "HTTP timeout")
	claudeCreds = flag.String("claude-credentials", filepath.Join(homeDir(), ".claude", ".credentials.json"), "Claude Code credentials file")
	codexAuth   = flag.String("codex-auth", filepath.Join(codexHome(), "auth.json"), "Codex auth file")
)

// Window is one rate-limit window of a provider.
type Window struct {
	Name             string    `json:"name"`
	UsedPercent      float64   `json:"used_percent"`
	RemainingPercent float64   `json:"remaining_percent"`
	ResetsAt         time.Time `json:"resets_at,omitzero"`
	ResetsIn         string    `json:"resets_in,omitempty"`
}

// ResetCredit is a ticket that resets the rate limits once.
type ResetCredit struct {
	Title     string    `json:"title"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	ExpiresIn string    `json:"expires_in,omitempty"`
}

// Report is the result of one provider.
type Report struct {
	Provider     string        `json:"provider"`
	Plan         string        `json:"plan,omitempty"`
	Windows      []Window      `json:"windows,omitempty"`
	ResetCredits []ResetCredit `json:"reset_credits,omitempty"`
	Error        string        `json:"error,omitempty"`
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] [codex|claude ...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	targets := flag.Args()
	if len(targets) == 0 {
		targets = []string{"codex", "claude"}
	}

	for _, t := range targets {
		switch t {
		// Why not drop chatgpt: it was the name of this provider before, so keep it as an alias
		case "claude", "codex", "chatgpt":
		default:
			fmt.Fprintf(os.Stderr, "ai-usage: unknown provider %q (want codex or claude)\n", t)
			os.Exit(1)
		}
	}

	client := &http.Client{Timeout: *timeout}
	now := time.Now()

	var reports []Report
	for _, t := range targets {
		var r Report
		var err error
		switch t {
		case "claude":
			r, err = fetchClaude(client, now)
		case "codex", "chatgpt":
			r, err = fetchCodex(client, now)
		}
		if err != nil {
			r.Error = err.Error()
		}
		reports = append(reports, r)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			fmt.Fprintf(os.Stderr, "ai-usage: %v\n", err)
			os.Exit(1)
		}
	} else {
		printText(reports)
	}

	// Why not exit 1 on any error: show what we could fetch, and fail only when all providers fail
	for _, r := range reports {
		if r.Error == "" {
			return
		}
	}
	os.Exit(1)
}

func printText(reports []Report) {
	for i, r := range reports {
		if i > 0 {
			fmt.Println()
		}
		title := r.Provider
		if r.Plan != "" {
			title += " (" + r.Plan + ")"
		}
		fmt.Println(title)
		if r.Error != "" {
			fmt.Printf("  error: %s\n", r.Error)
			continue
		}
		for _, w := range r.Windows {
			reset := "-"
			if !w.ResetsAt.IsZero() {
				reset = fmt.Sprintf("%s (%s)", w.ResetsIn, w.ResetsAt.Local().Format("01/02 15:04"))
			}
			fmt.Printf("  %-16s remaining %5.1f%%  %s  reset in %s\n", w.Name, w.RemainingPercent, bar(w.RemainingPercent), reset)
		}
		for _, c := range r.ResetCredits {
			expires := "-"
			if !c.ExpiresAt.IsZero() {
				expires = fmt.Sprintf("%s (%s)", c.ExpiresIn, c.ExpiresAt.Local().Format("01/02 15:04"))
			}
			fmt.Printf("  %-16s %s  expires in %s\n", "reset ticket", c.Title, expires)
		}
	}
}

func bar(remaining float64) string {
	const width = 20
	n := int(remaining/100*width + 0.5)
	n = max(0, min(width, n))
	return "[" + strings.Repeat("#", n) + strings.Repeat(".", width-n) + "]"
}

func newWindow(name string, used float64, resetsAt time.Time, now time.Time) Window {
	w := Window{
		Name:             name,
		UsedPercent:      used,
		RemainingPercent: max(0, 100-used),
		ResetsAt:         resetsAt,
	}
	if !resetsAt.IsZero() {
		w.ResetsIn = formatDuration(resetsAt.Sub(now))
	}
	return w
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%02dh%02dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh%02dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// --- Claude -----------------------------------------------------------------

type claudeCredentials struct {
	ClaudeAiOauth struct {
		AccessToken      string `json:"accessToken"`
		ExpiresAt        int64  `json:"expiresAt"` // unix millis
		SubscriptionType string `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

type claudeUsage struct {
	Limits []struct {
		Kind     string    `json:"kind"`
		Percent  float64   `json:"percent"`
		ResetsAt time.Time `json:"resets_at"`
		Scope    *struct {
			Model *struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	} `json:"limits"`
}

func fetchClaude(client *http.Client, now time.Time) (Report, error) {
	r := Report{Provider: "Claude"}

	var creds claudeCredentials
	if err := readJSON(*claudeCreds, &creds); err != nil {
		return r, fmt.Errorf("%v (log in with `claude` first)", err)
	}
	oauth := creds.ClaudeAiOauth
	r.Plan = oauth.SubscriptionType
	if oauth.AccessToken == "" {
		return r, fmt.Errorf("no OAuth token in %s (log in with `claude` first)", *claudeCreds)
	}
	// Why not refresh: refreshing here may invalidate the refresh token that Claude Code holds, so let Claude Code do it
	if oauth.ExpiresAt > 0 && time.UnixMilli(oauth.ExpiresAt).Before(now) {
		return r, fmt.Errorf("OAuth token expired at %s (run `claude` once to refresh it)", time.UnixMilli(oauth.ExpiresAt).Local().Format(time.DateTime))
	}

	req, err := http.NewRequest(http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return r, err
	}
	req.Header.Set("Authorization", "Bearer "+oauth.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	var u claudeUsage
	if err := doJSON(client, req, &u); err != nil {
		return r, err
	}
	for _, l := range u.Limits {
		var model string
		if l.Scope != nil && l.Scope.Model != nil {
			model = l.Scope.Model.DisplayName
		}
		r.Windows = append(r.Windows, newWindow(claudeWindowName(l.Kind, model), l.Percent, l.ResetsAt, now))
	}
	return r, nil
}

func claudeWindowName(kind, model string) string {
	if model != "" {
		if kind == "session" {
			return "5h session " + model
		}
		return "weekly " + model
	}
	switch kind {
	case "session":
		return "5h session"
	case "weekly_all":
		return "weekly all"
	}
	return kind
}

// --- Codex (ChatGPT plan) ---------------------------------------------------

var (
	codexUsageURL        = "https://chatgpt.com/backend-api/wham/usage"
	codexResetCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
)

type codexAuthFile struct {
	Tokens *struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type codexWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds float64 `json:"limit_window_seconds"`
	ResetAfterSeconds  float64 `json:"reset_after_seconds"`
	// Why not int64: the type is not confirmed, so a non-number value must not break decoding of other fields
	ResetAt json.RawMessage `json:"reset_at"`
}

func (w *codexWindow) resetsAt(now time.Time) time.Time {
	var sec float64
	if err := json.Unmarshal(w.ResetAt, &sec); err == nil && sec > 0 {
		return time.Unix(int64(sec), 0)
	}
	var t time.Time
	if err := json.Unmarshal(w.ResetAt, &t); err == nil && !t.IsZero() {
		return t
	}
	if w.ResetAfterSeconds > 0 {
		return now.Add(time.Duration(w.ResetAfterSeconds * float64(time.Second)))
	}
	return time.Time{}
}

type codexRateLimit struct {
	PrimaryWindow   *codexWindow `json:"primary_window"`
	SecondaryWindow *codexWindow `json:"secondary_window"`
}

type codexResetCredits struct {
	Credits []struct {
		Status    string    `json:"status"`
		Title     string    `json:"title"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"credits"`
}

type codexUsage struct {
	PlanType             string         `json:"plan_type"`
	RateLimit            codexRateLimit `json:"rate_limit"`
	AdditionalRateLimits []struct {
		LimitName string         `json:"limit_name"`
		RateLimit codexRateLimit `json:"rate_limit"`
	} `json:"additional_rate_limits"`
}

func fetchCodex(client *http.Client, now time.Time) (Report, error) {
	r := Report{Provider: "Codex"}

	var auth codexAuthFile
	if err := readJSON(*codexAuth, &auth); err != nil {
		return r, fmt.Errorf("%v (log in with `codex login` using a ChatGPT account)", err)
	}
	if auth.Tokens == nil || auth.Tokens.AccessToken == "" {
		return r, fmt.Errorf("no ChatGPT token in %s (API key login has no plan limits; use `codex login` with a ChatGPT account)", *codexAuth)
	}

	get := func(url string, v any) error {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
		if auth.Tokens.AccountID != "" {
			req.Header.Set("ChatGPT-Account-Id", auth.Tokens.AccountID)
		}
		req.Header.Set("User-Agent", "ai-usage")
		return doJSON(client, req, v)
	}

	var u codexUsage
	if err := get(codexUsageURL, &u); err != nil {
		return r, err
	}
	r.Plan = u.PlanType
	r.Windows = append(r.Windows, codexWindows("", u.RateLimit, now)...)
	for _, a := range u.AdditionalRateLimits {
		r.Windows = append(r.Windows, codexWindows(a.LimitName+" ", a.RateLimit, now)...)
	}

	var rc codexResetCredits
	// Why not return the error: reset tickets are extra info, so the rate limits above should still be shown
	if err := get(codexResetCreditsURL, &rc); err == nil {
		for _, c := range rc.Credits {
			if c.Status != "available" {
				continue
			}
			credit := ResetCredit{Title: c.Title, ExpiresAt: c.ExpiresAt}
			if !c.ExpiresAt.IsZero() {
				credit.ExpiresIn = formatDuration(c.ExpiresAt.Sub(now))
			}
			r.ResetCredits = append(r.ResetCredits, credit)
		}
		slices.SortFunc(r.ResetCredits, func(a, b ResetCredit) int { return a.ExpiresAt.Compare(b.ExpiresAt) })
	}
	return r, nil
}

func codexWindows(prefix string, rl codexRateLimit, now time.Time) []Window {
	var ws []Window
	for _, w := range []*codexWindow{rl.PrimaryWindow, rl.SecondaryWindow} {
		if w == nil {
			continue
		}
		ws = append(ws, newWindow(prefix+windowLabel(int64(w.LimitWindowSeconds)), w.UsedPercent, w.resetsAt(now), now))
	}
	return ws
}

func windowLabel(sec int64) string {
	switch d := time.Duration(sec) * time.Second; {
	case sec <= 0:
		return "window"
	case d%(24*time.Hour) == 0 && d >= 7*24*time.Hour:
		return "weekly"
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// --- helpers ----------------------------------------------------------------

func doJSON(client *http.Client, req *http.Request, v any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s: %s", req.Method, req.URL, resp.Status, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", req.URL, err)
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func codexHome() string {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	return filepath.Join(homeDir(), ".codex")
}
