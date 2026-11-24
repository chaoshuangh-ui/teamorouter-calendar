package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-gui/goui/platform/windows/sdk/winapi"
)

// ---- persisted panel state ----

type todoItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Date string `json:"date"` // day this todo is scheduled for (YYYY-MM-DD)
	Done bool   `json:"done"`
}

// workSegment is kept only to migrate data written by the earlier detailed
// per-session build. New data uses the coarser two-hour buckets below.
type workSegment struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Sec   int64  `json:"sec"`
}

type workData struct {
	Day      string                   `json:"day"`                // day DaySec belongs to
	DaySec   int64                    `json:"day_sec"`            // accumulated work seconds today
	History  map[string]int64         `json:"history"`            // date -> work seconds
	Buckets  map[string][]int64       `json:"buckets,omitempty"`  // date -> 12 two-hour buckets
	Segments map[string][]workSegment `json:"segments,omitempty"` // legacy, migrated on load
}

type weatherLoc struct {
	City string  `json:"city"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

type panelData struct {
	Todos      []todoItem `json:"todos"`
	Work       workData   `json:"work"`
	WeatherLoc weatherLoc `json:"weather_loc"`
}

// panelState guards panelData because the background tracker and the GUI
// thread both touch it. All access goes through these methods.
type panelState struct {
	mu     sync.Mutex // guards data
	saveMu sync.Mutex // serialises complete save operations, preserving order
	data   panelData
	// onChange is called (from the tracker goroutine) to request a repaint.
	onChange func()
}

func newPanelState() *panelState {
	p := &panelState{}
	p.data = loadPanelData()
	// Initial rollover at startup covers "shut down overnight". Persist it
	// immediately so a quick exit cannot repeat the carry-over next time.
	p.rollover(time.Now())
	p.save()
	return p
}

func panelPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "TeamoRouterCalendar", "panel.json")
}

func loadPanelData() panelData {
	d := panelData{}
	d.Work.History = map[string]int64{}
	d.Work.Buckets = map[string][]int64{}
	path := panelPath()
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, &d); err != nil {
				// Keep a copy instead of silently turning a corrupt file into
				// an empty state on the next start.
				log.Printf("panel.json: parse failed (%v); kept a .corrupt copy", err)
				_ = os.WriteFile(path+".corrupt", b, 0644)
				d = panelData{}
			}
		}
	}
	if d.Work.History == nil {
		d.Work.History = map[string]int64{}
	}
	if d.Work.Buckets == nil {
		d.Work.Buckets = map[string][]int64{}
	}
	migrateWorkSegments(&d)
	return d
}

// ensureWorkBuckets returns the 12 two-hour counters for one day, creating
// them when absent or malformed.
func ensureWorkBuckets(m map[string][]int64, day string) []int64 {
	b := m[day]
	if len(b) != bucketCount {
		b = make([]int64, bucketCount)
		m[day] = b
	}
	return b
}

// normalizeBuckets rescales a bucket array so its sum matches the day total.
// Legacy sessions recorded the same total twice (day_sec and segment.sec), so
// folding them must not change the total; rounding is corrected here.
func normalizeBuckets(b []int64, target int64) {
	if target <= 0 || len(b) == 0 {
		return
	}
	sum := int64(0)
	for _, v := range b {
		sum += v
	}
	if sum == target {
		return
	}
	if sum <= 0 {
		b[len(b)-1] = target
		return
	}
	for i := range b {
		b[i] = int64(float64(b[i]) * float64(target) / float64(sum))
	}
	cur := int64(0)
	big := 0
	for i, v := range b {
		cur += v
		if v > b[big] {
			big = i
		}
	}
	b[big] += target - cur
}

// migrateWorkSegments folds legacy detailed sessions into two-hour buckets.
// Seconds are distributed in proportion to the wall-clock span of each old
// segment, so the bucket sum stays equal to the day total. A day that already
// has buckets is left alone to keep the migration idempotent.
func migrateWorkSegments(d *panelData) {
	if len(d.Work.Segments) == 0 {
		d.Work.Segments = nil
		return
	}
	if d.Work.Buckets == nil {
		d.Work.Buckets = map[string][]int64{}
	}
	type span struct {
		start, end time.Time
		sec        int64
	}
	for day, segs := range d.Work.Segments {
		if existing := d.Work.Buckets[day]; len(existing) == bucketCount {
			sum := int64(0)
			for _, v := range existing {
				sum += v
			}
			if sum > 0 {
				continue
			}
		}
		dayStart, err := time.ParseInLocation("2006-01-02", day, time.Local)
		if err != nil {
			continue
		}
		var spans []span
		for _, s := range segs {
			if s.Sec <= 0 {
				continue
			}
			clock, err := time.ParseInLocation("15:04", s.Start, time.Local)
			if err != nil {
				continue
			}
			start := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(),
				clock.Hour(), clock.Minute(), 0, 0, time.Local)
			end := start.Add(time.Duration(s.Sec) * time.Second)
			if clockEnd, e := time.ParseInLocation("15:04", s.End, time.Local); e == nil {
				candidate := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(),
					clockEnd.Hour(), clockEnd.Minute(), 0, 0, time.Local)
				if !candidate.After(start) {
					candidate = candidate.AddDate(0, 0, 1) // crossed midnight
				}
				end = candidate
			}
			spans = append(spans, span{start: start, end: end, sec: s.Sec})
		}

		migrated := map[string][]int64{}
		for _, sp := range spans {
			total := sp.end.Sub(sp.start)
			if total <= 0 {
				continue
			}
			for cur := sp.start; cur.Before(sp.end); {
				next := time.Date(cur.Year(), cur.Month(), cur.Day(),
					(cur.Hour()/bucketHours+1)*bucketHours, 0, 0, 0, cur.Location())
				if next.After(sp.end) {
					next = sp.end
				}
				key := cur.Format("2006-01-02")
				b := migrated[key]
				if b == nil {
					b = make([]int64, bucketCount)
					migrated[key] = b
				}
				b[cur.Hour()/bucketHours] += int64(math.Round(
					float64(sp.sec) * float64(next.Sub(cur)) / float64(total)))
				cur = next
			}
		}
		for key, b := range migrated {
			target := d.Work.History[key]
			if key == d.Work.Day {
				target = d.Work.DaySec
			}
			if target <= 0 {
				// A segment can spill into the next day, which has no stored
				// total yet. Seed it from the migrated seconds so the tooltip
				// total agrees with its buckets.
				var sum int64
				for _, v := range b {
					sum += v
				}
				if sum > 0 {
					if key == d.Work.Day {
						d.Work.DaySec = sum
					} else {
						d.Work.History[key] = sum
					}
					target = sum
				}
			}
			normalizeBuckets(b, target)
			d.Work.Buckets[key] = b
		}
	}
	d.Work.Segments = nil
}

// save serialises marshal + write so a slow write cannot be overtaken by a
// newer save. The file is replaced atomically to avoid a truncated JSON file
// if the process exits mid-write.
func (p *panelState) save() {
	path := panelPath()
	if path == "" {
		return
	}
	p.saveMu.Lock()
	defer p.saveMu.Unlock()

	p.mu.Lock()
	b, err := json.MarshalIndent(p.data, "", "  ")
	p.mu.Unlock()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// rollover archives finished work time, resets today's counter and moves
// unfinished todos to today. Finished todos from earlier days are dropped so
// the list stays short.
func (p *panelState) rollover(now time.Time) {
	today := now.Format("2006-01-02")
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.data.Work.History == nil {
		p.data.Work.History = map[string]int64{}
	}
	if p.data.Work.Buckets == nil {
		p.data.Work.Buckets = map[string][]int64{}
	}
	if p.data.Work.Day != "" && p.data.Work.Day != today {
		p.data.Work.History[p.data.Work.Day] = p.data.Work.DaySec
		p.data.Work.DaySec = 0
		p.data.Work.Day = today
	}
	if p.data.Work.Day == "" {
		p.data.Work.Day = today
	}

	kept := p.data.Todos[:0]
	for _, t := range p.data.Todos {
		if t.Date >= today {
			kept = append(kept, t)
			continue
		}
		if !t.Done {
			t.Date = today // carry unfinished tasks forward
			kept = append(kept, t)
		}
		// finished tasks from earlier days are discarded
	}
	p.data.Todos = kept
}

// ---- todo operations (called on the GUI thread) ----

func (p *panelState) addTodo(text string, today string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	p.mu.Lock()
	p.data.Todos = append(p.data.Todos, todoItem{
		ID:   strconv.FormatInt(time.Now().UnixNano(), 36),
		Text: text,
		Date: today,
	})
	p.mu.Unlock()
	p.save()
}

func (p *panelState) toggleTodo(id string) {
	p.mu.Lock()
	for i := range p.data.Todos {
		if p.data.Todos[i].ID == id {
			p.data.Todos[i].Done = !p.data.Todos[i].Done
			break
		}
	}
	p.mu.Unlock()
	p.save()
}

func (p *panelState) removeTodo(id string) {
	p.mu.Lock()
	for i := range p.data.Todos {
		if p.data.Todos[i].ID == id {
			p.data.Todos = append(p.data.Todos[:i], p.data.Todos[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	p.save()
}

func (p *panelState) todayTodos(today string) []todoItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]todoItem, 0, len(p.data.Todos))
	for _, t := range p.data.Todos {
		if t.Date == today {
			out = append(out, t)
		}
	}
	return out
}

// ---- work time tracking ----

const (
	idlePause   = 10 * time.Minute
	bucketHours = 2
	bucketCount = 24 / bucketHours

	// A successful reading is reused for this long; a failed one is retried
	// much sooner so a transient network hiccup at startup cannot leave the
	// card stuck on "天气获取中..." for half an hour.
	weatherRefreshEvery = 30 * time.Minute
	weatherRetryEvery   = time.Minute
)

type workTracker struct {
	panel  *panelState
	notify func()     // requests a repaint; must be safe from any goroutine
	mu     sync.Mutex // guards the snapshot fields below
	idle   bool       // true when input has been absent for idlePause
	// weather state, fetched on a dedicated worker so a slow HTTP request
	// cannot block work-time tracking or repaints
	weather     weatherData
	weatherAt   time.Time
	weatherOK   bool          // whether the last fetch succeeded
	weatherBusy bool          // a fetch is in progress
	weatherReq  chan struct{} // buffered trigger for the weather worker
	carry       time.Duration // sub-second remainder between ticks
}

func (t *workTracker) run(stop <-chan struct{}) {
	var lastPos winapi.POINT
	winapi.GetCursorPos(&lastPos)
	lastMove := time.Now()
	lastTick := lastMove
	lastSave := lastMove

	// Weather is fetched on its own worker: an HTTP request can take several
	// seconds, and blocking this loop would stall work-time tracking and
	// repaints. A buffered request channel coalesces overlapping triggers.
	go t.weatherWorker(stop)
	t.requestWeather()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			t.panel.save()
			return
		case now := <-ticker.C:
			// Accumulate real elapsed time instead of one second per tick, so
			// a delayed tick does not silently lose work time. Cap the catch-up
			// to avoid crediting a long sleep or a suspended process.
			elapsed := now.Sub(lastTick)
			lastTick = now
			if elapsed < 0 {
				elapsed = 0
			}
			if elapsed > 2*time.Second {
				elapsed = 2 * time.Second
			}

			// GetLastInputInfo covers keyboard as well as mouse activity.
			// Fall back to cursor polling on systems that deny the call.
			idleFor, ok := idleDuration()
			if !ok {
				var pos winapi.POINT
				if winapi.GetCursorPos(&pos) == 1 {
					if pos.X != lastPos.X || pos.Y != lastPos.Y {
						lastPos = pos
						lastMove = now
					}
				}
				idleFor = now.Sub(lastMove)
			}
			idle := idleFor >= idlePause
			t.mu.Lock()
			t.idle = idle
			t.mu.Unlock()

			didRollover := false
			t.panel.mu.Lock()
			if t.panel.data.Work.Buckets == nil {
				t.panel.data.Work.Buckets = map[string][]int64{}
			}
			// date change -> archive the previous day
			today := now.Format("2006-01-02")
			if t.panel.data.Work.Day != today {
				didRollover = true
				old := t.panel.data.Work.Day
				if old != "" {
					t.panel.data.Work.History[old] = t.panel.data.Work.DaySec
				}
				t.panel.data.Work.DaySec = 0
				t.panel.data.Work.Day = today
				// carry unfinished todos forward at midnight too
				kept := t.panel.data.Todos[:0]
				for _, td := range t.panel.data.Todos {
					if td.Date >= today {
						kept = append(kept, td)
					} else if !td.Done {
						td.Date = today
						kept = append(kept, td)
					}
				}
				t.panel.data.Todos = kept
			}
			if idle {
				t.carry = 0
			} else {
				t.carry += elapsed
				if secs := int64(t.carry / time.Second); secs > 0 {
					t.carry -= time.Duration(secs) * time.Second
					buckets := ensureWorkBuckets(t.panel.data.Work.Buckets, today)
					buckets[now.Hour()/bucketHours] += secs
					t.panel.data.Work.DaySec += secs
				}
			}
			t.panel.mu.Unlock()
			if didRollover {
				// Persist the archive and carried todos immediately; a crash in
				// the next 30s must not restore yesterday's in-memory-only state.
				t.panel.save()
			}

			// Request a weather refresh when due; the worker performs the
			// actual HTTP call off this loop.
			if t.weatherDue(now) {
				t.requestWeather()
			}
			// persist every 30 seconds
			if now.Sub(lastSave) >= 30*time.Second {
				t.panel.save()
				lastSave = now
			}
			if t.notify != nil {
				t.notify()
			}
		}
	}
}

// requestWeather queues a fetch without blocking. The channel has capacity 1,
// so repeated triggers while a fetch is pending collapse into one request.
func (t *workTracker) requestWeather() {
	if t.weatherReq == nil {
		return
	}
	select {
	case t.weatherReq <- struct{}{}:
	default:
	}
}

// weatherDue reports whether a new reading should be started. A failed fetch
// gets a short retry interval; a successful one lasts weatherRefreshEvery.
func (t *workTracker) weatherDue(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.weatherBusy {
		return false
	}
	interval := weatherRefreshEvery
	if !t.weatherOK {
		interval = weatherRetryEvery
	}
	return t.weatherAt.IsZero() || now.Sub(t.weatherAt) >= interval
}

// weatherWorker performs weather HTTP requests off the 1-second tracker loop.
func (t *workTracker) weatherWorker(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-t.weatherReq:
			t.mu.Lock()
			if t.weatherBusy {
				t.mu.Unlock()
				continue
			}
			t.weatherBusy = true
			t.mu.Unlock()

			w := fetchWeather(t.panel)

			t.mu.Lock()
			t.weather = w
			t.weatherOK = w.Available
			t.weatherAt = time.Now()
			t.weatherBusy = false
			t.mu.Unlock()

			// Repaint once the new reading (or a retry-worthy failure) lands.
			if t.notify != nil {
				t.notify()
			}
		}
	}
}

func (t *workTracker) snapshot() (weatherData, int64, map[string]int64, map[string][]int64, bool, bool) {
	t.panel.mu.Lock()
	sec := t.panel.data.Work.DaySec
	hist := make(map[string]int64, len(t.panel.data.Work.History))
	for k, v := range t.panel.data.Work.History {
		hist[k] = v
	}
	buckets := make(map[string][]int64, len(t.panel.data.Work.Buckets))
	for k, v := range t.panel.data.Work.Buckets {
		buckets[k] = append([]int64(nil), v...)
	}
	t.panel.mu.Unlock()

	t.mu.Lock()
	weather := t.weather
	idle := t.idle
	t.mu.Unlock()
	return weather, sec, hist, buckets, weather.Available, idle
}

// ---- weather ----

type weatherData struct {
	Available bool
	City      string
	Desc      string
	Humidity  int
	Temp      float64
}

var panelHTTP = &http.Client{Timeout: 12 * time.Second}

func fetchWeather(p *panelState) weatherData {
	var out weatherData

	p.mu.Lock()
	loc := p.data.WeatherLoc
	p.mu.Unlock()

	// Resolve location once (IP based) and cache it.
	if loc.City == "" || !validLatLon(loc.Lat, loc.Lon) {
		resolved, ok := ipLocate()
		if !ok {
			return out
		}
		loc = resolved
		p.mu.Lock()
		p.data.WeatherLoc = loc
		p.mu.Unlock()
		p.save()
	}
	out.City = loc.City

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,weather_code",
		loc.Lat, loc.Lon)
	resp, err := panelHTTP.Get(url)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return out
	}
	var mr struct {
		Current *struct {
			Temp     float64 `json:"temperature_2m"`
			Humidity int     `json:"relative_humidity_2m"`
			Code     int     `json:"weather_code"`
		} `json:"current"`
	}
	if json.Unmarshal(b, &mr) != nil || mr.Current == nil {
		return out
	}
	out.Available = true
	out.Temp = mr.Current.Temp
	out.Humidity = mr.Current.Humidity
	out.Desc = weatherDesc(mr.Current.Code)
	return out
}

type ipapiResp struct {
	City      string  `json:"city"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func ipLocate() (weatherLoc, bool) {
	// ipapi.co is free and needs no API key. Fall back to ipinfo.io.
	resp, err := panelHTTP.Get("https://ipapi.co/json/")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			var r ipapiResp
			if json.Unmarshal(b, &r) == nil && (r.LatOK()) {
				return weatherLoc{City: r.City, Lat: r.Latitude, Lon: r.Longitude}, true
			}
		}
	}
	resp2, err := panelHTTP.Get("https://ipinfo.io/json")
	if err != nil {
		return weatherLoc{}, false
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		return weatherLoc{}, false
	}
	b, _ := io.ReadAll(io.LimitReader(resp2.Body, 1<<16))
	var r2 struct {
		City string `json:"city"`
		Loc  string `json:"loc"`
	}
	if json.Unmarshal(b, &r2) != nil {
		return weatherLoc{}, false
	}
	parts := strings.Split(r2.Loc, ",")
	if len(parts) != 2 {
		return weatherLoc{}, false
	}
	lat, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lon, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if e1 != nil || e2 != nil {
		return weatherLoc{}, false
	}
	return weatherLoc{City: r2.City, Lat: lat, Lon: lon}, true
}

func (r ipapiResp) LatOK() bool { return validLatLon(r.Latitude, r.Longitude) }

// validLatLon rejects unset or out-of-range coordinates. (0,0) is a real point
// in the ocean but useless for weather, so it is treated as unresolved.
func validLatLon(lat, lon float64) bool {
	return lat != 0 && lon != 0 && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func weatherDesc(code int) string {
	switch {
	case code == 0:
		return "晴"
	case code == 1, code == 2:
		return "多云"
	case code == 3:
		return "阴"
	case code == 45, code == 48:
		return "雾"
	case code >= 51 && code <= 57:
		return "毛毛雨"
	case code >= 61 && code <= 67:
		return "雨"
	case code >= 71 && code <= 77:
		return "雪"
	case code >= 80 && code <= 82:
		return "阵雨"
	case code >= 85 && code <= 86:
		return "阵雪"
	case code >= 95:
		return "雷阵雨"
	default:
		return "未知"
	}
}

// ---- formatting helpers ----

func formatDuration(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func shortWeekday(t time.Time) string {
	switch t.Weekday() {
	case time.Monday:
		return "一"
	case time.Tuesday:
		return "二"
	case time.Wednesday:
		return "三"
	case time.Thursday:
		return "四"
	case time.Friday:
		return "五"
	case time.Saturday:
		return "六"
	default:
		return "日"
	}
}
