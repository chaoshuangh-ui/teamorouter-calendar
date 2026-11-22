package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const (
	antiHubGlob  = `C:\Users\32517\.gemini\antigravity\daemon\hub_*.json`
	antiQuotaRPC = "/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary"
)

// AntiBucket is one quota window (e.g. "5h" or "weekly").
type AntiBucket struct {
	ID        string
	Label     string  // short window label: "5H", "WEEK", ...
	Remaining float64 // 0..1 fraction remaining
	ResetAt   time.Time
}

// AntiQuota is a snapshot of the Antigravity quota groups.
type AntiQuota struct {
	Available bool
	Groups    []AntiGroup
	Error     string
}

// AntiGroup is one model group with its quota buckets.
type AntiGroup struct {
	Name    string
	Buckets []AntiBucket
}

type antiHub struct {
	PID       int    `json:"pid"`
	HTTPPort  int    `json:"httpPort"`
	CsrfToken string `json:"csrfToken"`
	Mtime     time.Time
}

// fetchAntiQuota finds the live Antigravity language-server hub and reads its
// quota summary over loopback. The csrf token is read from the hub file and used
// only in the request header; it is never logged or persisted.
func fetchAntiQuota() *AntiQuota {
	out := &AntiQuota{}
	hub, err := liveAntiHub()
	if err != nil {
		out.Error = "not running"
		return out
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", hub.HTTPPort, antiQuotaRPC)
	client := apiHTTP10
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		// A POST body is consumed by Do, so each retry needs a fresh request.
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(`{"forceRefresh":false}`)))
		if err != nil {
			out.Error = "request error"
			return out
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-codeium-csrf-token", hub.CsrfToken)
		resp, err = client.Do(req)
		if err == nil {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if err != nil {
		out.Error = "not running"
		return out
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		out.Error = "unavailable"
		return out
	}

	var payload struct {
		Response struct {
			Groups []struct {
				DisplayName string `json:"displayName"`
				Buckets     []struct {
					BucketID          string  `json:"bucketId"`
					RemainingFraction float64 `json:"remainingFraction"`
					ResetTime         string  `json:"resetTime"`
					Window            string  `json:"window"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		out.Error = "parse error"
		return out
	}
	out.Available = true
	for _, g := range payload.Response.Groups {
		ag := AntiGroup{Name: g.DisplayName}
		for _, b := range g.Buckets {
			reset, _ := time.Parse(time.RFC3339, b.ResetTime)
			ag.Buckets = append(ag.Buckets, AntiBucket{
				ID:        b.BucketID,
				Label:     shortWindow(b.Window, b.BucketID),
				Remaining: b.RemainingFraction,
				ResetAt:   reset,
			})
		}
		out.Groups = append(out.Groups, ag)
	}
	return out
}

// liveAntiHub picks the hub whose pid is alive and whose mtime is newest.
// Older hubs whose pid happens to still be alive are ignored in favour of the
// most recently written one.
func liveAntiHub() (*antiHub, error) {
	entries, _ := filepath.Glob(antiHubGlob)
	var hubs []antiHub
	for _, path := range entries {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var h antiHub
		if json.Unmarshal(b, &h) != nil || h.HTTPPort == 0 || h.CsrfToken == "" {
			continue
		}
		if st, err := os.Stat(path); err == nil {
			h.Mtime = st.ModTime()
		}
		if !pidAlive(h.PID) {
			continue
		}
		hubs = append(hubs, h)
	}
	if len(hubs) == 0 {
		return nil, fmt.Errorf("no live hub")
	}
	sort.Slice(hubs, func(i, j int) bool { return hubs[i].Mtime.After(hubs[j].Mtime) })
	return &hubs[0], nil
}

func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil || h == 0 {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// fiveHourWindow matches a genuine 5-hour window, not "15m" or "25h".
var fiveHourWindow = regexp.MustCompile(`(?i)(^|[^0-9])5\s*(h|hours?|hrs?)\b`)

// shortWindow produces a compact label for a quota window.
func shortWindow(window, id string) string {
	w := strings.ToLower(window)
	switch {
	case strings.Contains(w, "week"):
		return "WEEK"
	case fiveHourWindow.MatchString(w) || w == "5":
		return "5H"
	case strings.Contains(w, "day") || strings.Contains(w, "daily"):
		return "DAY"
	}
	lid := strings.ToLower(id)
	if strings.Contains(lid, "week") {
		return "WEEK"
	}
	if strings.Contains(lid, "5h") {
		return "5H"
	}
	return strings.ToUpper(window)
}
