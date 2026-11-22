package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	codexAuthPath = `C:\Users\32517\.codex\auth.json`
	codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"
)

// CodexWindow is one rate-limit window reported by ChatGPT.
type CodexWindow struct {
	UsedPercent float64
	ResetAt     int64 // unix seconds
	Seconds     int64 // limit_window_seconds
}

// CodexUsage is a snapshot of the current Codex quota. Tokens are read from
// auth.json and never logged or persisted.
type CodexUsage struct {
	Available bool
	Plan      string
	Primary   *CodexWindow
	Secondary *CodexWindow
	Credits   string
	Error     string
}

// fetchCodexUsage reads the Codex ChatGPT OAuth token and queries the usage
// endpoint. It is safe to call from a background goroutine. Any failure is
// returned as a non-fatal Error string so the UI can show a compact status.
func fetchCodexUsage() *CodexUsage {
	out := &CodexUsage{}

	b, err := os.ReadFile(codexAuthPath)
	if err != nil {
		out.Error = "not logged in"
		return out
	}
	var auth struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &auth); err != nil {
		out.Error = "auth unreadable"
		return out
	}
	tok := strings.TrimSpace(auth.Tokens.AccessToken)
	if tok == "" {
		out.Error = "not logged in"
		return out
	}

	req, err := http.NewRequest(http.MethodGet, codexUsageURL, nil)
	if err != nil {
		out.Error = "request error"
		return out
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")
	if id := strings.TrimSpace(auth.Tokens.AccountID); id != "" {
		req.Header.Set("ChatGPT-Account-Id", id)
	}
	client := apiHTTP15
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		out.Error = "network error"
		return out
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		out.Error = "read error"
		return out
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		out.Error = "login expired"
		return out
	}
	if resp.StatusCode != http.StatusOK {
		out.Error = "api " + strconv.Itoa(resp.StatusCode)
		return out
	}

	var payload struct {
		PlanType  string `json:"plan_type"`
		RateLimit *struct {
			Primary *struct {
				UsedPercent        float64 `json:"used_percent"`
				ResetAt            int64   `json:"reset_at"`
				LimitWindowSeconds int64   `json:"limit_window_seconds"`
			} `json:"primary_window"`
			Secondary *struct {
				UsedPercent        float64 `json:"used_percent"`
				ResetAt            int64   `json:"reset_at"`
				LimitWindowSeconds int64   `json:"limit_window_seconds"`
			} `json:"secondary_window"`
		} `json:"rate_limit"`
		Credits *struct {
			Balance json.RawMessage `json:"balance"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		out.Error = "parse error"
		return out
	}

	out.Available = true
	out.Plan = payload.PlanType
	if rl := payload.RateLimit; rl != nil {
		if w := rl.Primary; w != nil {
			out.Primary = &CodexWindow{UsedPercent: w.UsedPercent, ResetAt: w.ResetAt, Seconds: w.LimitWindowSeconds}
		}
		if w := rl.Secondary; w != nil {
			out.Secondary = &CodexWindow{UsedPercent: w.UsedPercent, ResetAt: w.ResetAt, Seconds: w.LimitWindowSeconds}
		}
	}
	if c := payload.Credits; c != nil && len(c.Balance) > 0 {
		var v any
		if json.Unmarshal(c.Balance, &v) == nil {
			switch n := v.(type) {
			case string:
				out.Credits = strings.TrimSpace(n)
			case float64:
				out.Credits = strconv.FormatFloat(n, 'f', 2, 64)
			}
		}
		if out.Credits == "" {
			out.Credits = string(c.Balance)
		}
	}
	return out
}
