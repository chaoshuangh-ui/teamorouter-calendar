package main

import (
	"os"
	"strings"
	"testing"
)

func TestMigrateWorkSegmentsBasic(t *testing.T) {
	d := panelData{Work: workData{
		Day:     "2026-11-22",
		DaySec:  9000,
		History: map[string]int64{"2026-11-22": 9000},
		Segments: map[string][]workSegment{
			"2026-11-22": {
				{Start: "08:10", End: "08:40", Sec: 1800},
				{Start: "08:30", End: "10:30", Sec: 7200},
			},
		},
	}}
	migrateWorkSegments(&d)
	b := d.Work.Buckets["2026-11-22"]
	if len(b) != bucketCount {
		t.Fatalf("bucket count = %d, want %d", len(b), bucketCount)
	}
	if b[4] != 7200 { // 08:00-10:00
		t.Errorf("bucket 4 = %d, want 7200", b[4])
	}
	if b[5] != 1800 { // 10:00-12:00
		t.Errorf("bucket 5 = %d, want 1800", b[5])
	}
	var sum int64
	for _, v := range b {
		sum += v
	}
	if sum != 9000 {
		t.Errorf("bucket sum = %d, want day total 9000", sum)
	}
	if d.Work.Segments != nil {
		t.Error("legacy segments were not cleared")
	}
}

func TestMigrateWorkSegmentsCrossMidnight(t *testing.T) {
	d := panelData{Work: workData{
		Day:     "2026-11-22",
		DaySec:  1800,
		History: map[string]int64{"2026-11-22": 1800},
		Segments: map[string][]workSegment{
			"2026-11-22": {{Start: "23:30", End: "00:30", Sec: 3600}},
		},
	}}
	migrateWorkSegments(&d)
	if got := d.Work.Buckets["2026-11-22"][11]; got != 1800 {
		t.Errorf("23:00 bucket = %d, want 1800", got)
	}
	if got := d.Work.Buckets["2026-11-23"][0]; got != 1800 {
		t.Errorf("next-day 00:00 bucket = %d, want 1800", got)
	}
	if got := d.Work.History["2026-11-23"]; got != 1800 {
		t.Errorf("next-day history total = %d, want 1800", got)
	}
}

func TestMigrateWorkSegmentsIdempotent(t *testing.T) {
	segs := map[string][]workSegment{
		"2026-11-22": {{Start: "09:00", End: "10:00", Sec: 3600}},
	}
	d := panelData{Work: workData{
		Day:      "2026-11-22",
		DaySec:   3600,
		History:  map[string]int64{"2026-11-22": 3600},
		Segments: segs,
	}}
	migrateWorkSegments(&d)
	first := append([]int64(nil), d.Work.Buckets["2026-11-22"]...)
	// Simulate a stale file that still carries both fields on the next start.
	d.Work.Segments = segs
	migrateWorkSegments(&d)
	second := d.Work.Buckets["2026-11-22"]
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("second migration changed bucket %d: %d -> %d", i, first[i], second[i])
		}
	}
}

func TestNormalizeBuckets(t *testing.T) {
	b := []int64{3, 5, 2}
	normalizeBuckets(b, 20)
	var sum int64
	for _, v := range b {
		sum += v
	}
	if sum != 20 {
		t.Errorf("sum = %d, want 20", sum)
	}
	if b[1] <= b[0] || b[1] <= b[2] {
		t.Errorf("proportions not preserved: %v", b)
	}
}

func TestEnsureWorkBuckets(t *testing.T) {
	m := map[string][]int64{}
	b := ensureWorkBuckets(m, "2026-11-22")
	b[0] = 7
	if got := ensureWorkBuckets(m, "2026-11-22")[0]; got != 7 {
		t.Errorf("existing buckets were replaced: got %d, want 7", got)
	}
	m["2026-11-22"] = []int64{1, 2}
	if got := len(ensureWorkBuckets(m, "2026-11-22")); got != bucketCount {
		t.Errorf("malformed buckets were not rebuilt: len = %d", got)
	}
}

func TestShortWindow(t *testing.T) {
	cases := []struct{ window, id, want string }{
		{"5h", "", "5H"},
		{"5 hours", "", "5H"},
		{"weekly", "", "WEEK"},
		{"daily", "", "DAY"},
		{"15m", "", "15M"}, // must not be mistaken for a 5-hour window
		{"25h", "", "25H"},
		{"", "claude-5h", "5H"},
	}
	for _, c := range cases {
		if got := shortWindow(c.window, c.id); got != c.want {
			t.Errorf("shortWindow(%q, %q) = %q, want %q", c.window, c.id, got, c.want)
		}
	}
}

func TestAntiGroupNameUnicode(t *testing.T) {
	// Cutting a UTF-8 string at byte 11 would split this rune sequence.
	name := "模型分组一二三四五六七八九十"
	got := antiGroupName(name)
	runes := []rune(got)
	if len(runes) > 11 {
		t.Errorf("name too long: %d runes in %q", len(runes), got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("invalid rune in %q", got)
	}
}

func TestFormattingExtremes(t *testing.T) {
	if got := itoa(-1 << 60); got == "-" || got == "" {
		t.Errorf("itoa on a large negative int = %q", got)
	}
	if got := fmtFloat(1.23456); got != "1.2346" {
		t.Errorf("fmtFloat = %q", got)
	}
	if got := fmtFloat1(23.45); got != "23.5" && got != "23.4" {
		t.Errorf("fmtFloat1 = %q", got)
	}
}

// Integration check is opt-in: it reads the real database and calls the live
// APIs. It must never run as part of the normal offline test suite.
func TestDumpIntegration(t *testing.T) {
	if os.Getenv("TRC_INTEGRATION") != "1" {
		t.Skip("set TRC_INTEGRATION=1 to run the live data check")
	}
	d, err := loadData()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Months) != numMonths {
		t.Errorf("months = %d, want %d", len(d.Months), numMonths)
	}
	t.Logf("balance=%s today=%s months=%d", d.Balance, d.TodayKey, len(d.Months))
}
