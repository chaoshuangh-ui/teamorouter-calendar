package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	dbPath     = `C:\Users\32517\.local\share\opencode\opencode.db`
	keyPath    = `C:\Users\32517\.config\opencode\.secrets\teamorouter-key`
	balanceURL = "https://api.teamorouter.ai/v1/billing/me/balance"
	numMonths  = 6
)

// Reuse clients so keep-alive connections survive the minute-by-minute
// refreshes instead of being rebuilt for every request.
var (
	apiHTTP10 = &http.Client{Timeout: 10 * time.Second}
	apiHTTP15 = &http.Client{Timeout: 15 * time.Second}
)

// ModelUsage is one model's contribution to a single day.
type ModelUsage struct {
	Name   string
	Cost   float64
	Tokens int64
	Count  int
}

// DayData is the aggregate for one calendar day.
type DayData struct {
	Key     string // YYYY-MM-DD
	Day     int
	Cost    float64
	Tokens  int64
	Count   int
	Models  []ModelUsage // sorted by cost desc
	IsToday bool
}

// MonthData is one month page.
type MonthData struct {
	Key          string // YYYY-MM
	Label        string // "NOVEMBER 2026"
	ShortLabel   string // "NOV 2026"
	FirstWeekday int    // 0 = Monday
	NumDays      int
	Days         []DayData // index 0 == day 1
}

// AppData is everything the UI needs.
type AppData struct {
	Months   []MonthData
	MaxCost  float64
	Balance  string
	Codex    *CodexUsage // current Codex quota snapshot
	Anti     *AntiQuota  // current Antigravity quota snapshot
	TodayKey string
	Updated  time.Time
}

var tokenFields = []string{
	"tokens_input", "tokens_output", "tokens_reasoning",
	"tokens_cache_read", "tokens_cache_write",
}

func shortModel(id string) string {
	m := strings.ToLower(id)
	switch {
	case strings.Contains(m, "haiku"):
		return "Claude Haiku 5.5"
	case strings.Contains(m, "opus"):
		return "Claude Opus 5.5"
	case strings.Contains(m, "sonnet"):
		return "Claude Sonnet"
	case strings.Contains(m, "deepseek") && strings.Contains(m, "1017"):
		return "DeepSeek V4.1 1017"
	case strings.Contains(m, "deepseek") && strings.Contains(m, "v4"):
		return "DeepSeek V4 Flash"
	case strings.Contains(m, "deepseek"):
		return "DeepSeek Flash"
	case strings.Contains(m, "gpt-6.1-sol"):
		return "GPT 6.1 Sol"
	case strings.Contains(m, "gpt-6.1-terra"):
		return "GPT 6.1 Terra"
	case strings.Contains(m, "gpt-6-luna"):
		return "GPT-6 Luna"
	case strings.Contains(m, "gpt-6-astra"):
		return "GPT-6 Astra"
	case strings.Contains(m, "gemini-3.8"):
		return "Gemini 3.8 Flash"
	case strings.Contains(m, "gemini-3.5"):
		return "Gemini 3.5 Flash Lite"
	case strings.Contains(m, "gpt"):
		return "GPT"
	case strings.Contains(m, "gemini"):
		return "Gemini"
	}
	if len(id) > 18 {
		return id[:18]
	}
	return id
}

type rawKey struct {
	model string
	day   string
}

type rawVal struct {
	cost   float64
	tokens int64
	count  int
}

// loadData reads the local opencode database and the live balance, then builds
// the six month pages. It is safe to call from a background goroutine.
func loadData() (*AppData, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	cols := []string{"time_created", "model", "cost"}
	for _, f := range tokenFields {
		cols = append(cols, f)
	}
	// Only the six-month window is displayed; scanning the whole history every
	// minute wastes CPU and holds a read lock on the live opencode database.
	cutoff := time.Now().AddDate(0, -numMonths, 0).UnixMilli()
	q := fmt.Sprintf("select %s from session_v2 where time_created >= %d",
		strings.Join(cols, ","), cutoff)
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// key -> aggregate
	agg := map[rawKey]*rawVal{}
	for rows.Next() {
		var created sql.NullInt64
		var modelJSON sql.NullString
		var cost sql.NullFloat64
		tok := make([]sql.NullInt64, len(tokenFields))
		dest := []any{&created, &modelJSON, &cost}
		for i := range tok {
			dest = append(dest, &tok[i])
		}
		if err := rows.Scan(dest...); err != nil {
			continue
		}
		if !created.Valid || created.Int64 == 0 {
			continue
		}
		day := time.UnixMilli(created.Int64).In(time.Local).Format("2006-01-02")
		var mj struct {
			ProviderID string `json:"providerID"`
			ID         string `json:"id"`
		}
		if modelJSON.Valid && modelJSON.String != "" {
			_ = json.Unmarshal([]byte(modelJSON.String), &mj)
		}
		if mj.ProviderID != "teamorouter" && mj.ProviderID != "teamorouter-claude" {
			continue
		}
		k := rawKey{model: shortModel(mj.ID), day: day}
		v := agg[k]
		if v == nil {
			v = &rawVal{}
			agg[k] = v
		}
		if cost.Valid {
			v.cost += cost.Float64
		}
		for i := range tok {
			if tok[i].Valid {
				v.tokens += tok[i].Int64
			}
		}
		v.count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Index aggregates by day once: the old loop scanned every (model, day)
	// pair for every calendar cell.
	type dayAgg struct {
		cost   float64
		tokens int64
		count  int
		models []ModelUsage
	}
	byDay := map[string]*dayAgg{}
	for k, v := range agg {
		d := byDay[k.day]
		if d == nil {
			d = &dayAgg{}
			byDay[k.day] = d
		}
		d.cost += v.cost
		d.tokens += v.tokens
		d.count += v.count
		d.models = append(d.models, ModelUsage{
			Name: k.model, Cost: v.cost, Tokens: v.tokens, Count: v.count,
		})
	}
	for _, d := range byDay {
		sort.Slice(d.models, func(i, j int) bool { return d.models[i].Cost > d.models[j].Cost })
	}

	// build month pages: current month first, then 5 previous
	today := time.Now().In(time.Local)
	todayKey := today.Format("2006-01-02")
	data := &AppData{TodayKey: todayKey, Updated: time.Now()}

	for back := 0; back < numMonths; back++ {
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.Local).AddDate(0, -back, 0)
		y, m := first.Year(), first.Month()
		numDays := time.Date(y, m+1, 0, 0, 0, 0, 0, time.Local).Day()
		mo := MonthData{
			Key:          fmt.Sprintf("%04d-%02d", y, int(m)),
			Label:        strings.ToUpper(first.Format("January 2006")),
			ShortLabel:   strings.ToUpper(first.Format("Jan 2006")),
			FirstWeekday: (int(first.Weekday()) + 6) % 7, // Monday = 0
			NumDays:      numDays,
			Days:         make([]DayData, numDays),
		}
		for d := 1; d <= numDays; d++ {
			dt := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
			key := dt.Format("2006-01-02")
			dd := DayData{Key: key, Day: d, IsToday: key == todayKey}
			if a := byDay[key]; a != nil {
				dd.Cost = a.cost
				dd.Tokens = a.tokens
				dd.Count = a.count
				dd.Models = a.models
			}
			mo.Days[d-1] = dd
			if dd.Cost > data.MaxCost {
				data.MaxCost = dd.Cost
			}
		}
		data.Months = append(data.Months, mo)
	}

	data.Balance = fetchBalance()
	data.Codex = fetchCodexUsage()
	data.Anti = fetchAntiQuota()
	return data, nil
}

func fetchBalance() string {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "N/A"
	}
	key := strings.TrimSpace(string(keyBytes))
	req, err := http.NewRequest(http.MethodGet, balanceURL, nil)
	if err != nil {
		return "N/A"
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client := apiHTTP10
	resp, err := client.Do(req)
	if err != nil {
		return "N/A"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "N/A"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "N/A"
	}
	var out struct {
		Data struct {
			AvailableBalance string  `json:"available_balance"`
			AvailableAlt     float64 `json:"availableBalance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "N/A"
	}
	clean := strings.ReplaceAll(strings.TrimSpace(out.Data.AvailableBalance), ",", "")
	clean = strings.TrimPrefix(clean, "$")
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		// Never turn a malformed amount into a confident "$0.0000".
		if out.Data.AvailableAlt <= 0 {
			return "N/A"
		}
		v = out.Data.AvailableAlt
	}
	return fmt.Sprintf("$%.4f", v)
}

// FormatTokens mirrors the Python helper.
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return fmt.Sprint(n)
	}
}
