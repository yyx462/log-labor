package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/config"
	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/record"
	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/sources"
)

// TestDailyExcludeRoundTrip — list|add|remove against an isolated
// XDG_CONFIG_HOME (config.Path honors it), plus the loud-fail on a bad
// regex and compileConfigExcludes wiring.
func TestDailyExcludeRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "log-labor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(dir, "config.json")
	if err := os.WriteFile(seed, []byte(`{"endpoint":"https://x","profile":{"fields":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := dailyExclude([]string{"add", "job-seeking", "求职|interview"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Daily.Excludes) != 2 || c.Daily.Excludes[0] != "job-seeking" {
		t.Errorf("rules not saved: %v", c.Daily.Excludes)
	}
	// duplicate add must not grow the list
	if err := dailyExclude([]string{"add", "job-seeking"}); err != nil {
		t.Fatalf("dup add: %v", err)
	}
	if c, _ = config.Load(); len(c.Daily.Excludes) != 2 {
		t.Errorf("dup add grew the list: %v", c.Daily.Excludes)
	}
	// a bad regex never reaches the config
	if err := dailyExclude([]string{"add", "("}); err == nil {
		t.Error("bad regex must be rejected at add time")
	}
	// remove one
	if err := dailyExclude([]string{"remove", "job-seeking"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if c, _ = config.Load(); len(c.Daily.Excludes) != 1 || c.Daily.Excludes[0] != "求职|interview" {
		t.Errorf("remove left the wrong rules: %v", c.Daily.Excludes)
	}
	// unknown rule → exit-2
	if err := dailyExclude([]string{"remove", "nope"}); err == nil {
		t.Error("removing an absent rule must error")
	}
	// and the cmd-side compile honors what's stored
	reps, err := compileConfigExcludes(c)
	if err != nil || len(reps) != 1 {
		t.Errorf("compileConfigExcludes: %v, %d", err, len(reps))
	}
	if reps2, err := compileConfigExcludes(nil); err != nil || reps2 != nil {
		t.Errorf("nil config = no excludes: %v, %v", reps2, err)
	}
	// a POISONED stored pattern must fail collect loudly (never leak)
	if err := os.WriteFile(seed, []byte(`{"endpoint":"https://x","profile":{"fields":{}},"daily":{"excludes":["("]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ = config.Load()
	if _, err := compileConfigExcludes(c); err == nil {
		t.Error("poisoned exclude pattern must fail compilation loudly")
	}
}

func TestParseTotal(t *testing.T) {
	cases := map[string]float64{
		"":     8,
		"8":    8,
		"10":   10,
		"10h":  10,
		"10H":  10,
		"7.5":  7.5,
		"10小时": 10,
		" 9 ":  9,
	}
	for in, want := range cases {
		got, err := parseTotal(in)
		if err != nil || got != want {
			t.Errorf("parseTotal(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "0h", "-4", "abc", "十"} {
		if got, err := parseTotal(in); err == nil {
			t.Errorf("parseTotal(%q) = %v; want error", in, got)
		}
	}
}

func TestResolveRange(t *testing.T) {
	fromMs, toMs, days, err := resolveRange("", "2026-09-11", "2026-09-13")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 3 || days[0] != "2026-09-11" || days[2] != "2026-09-13" {
		t.Errorf("days = %v", days)
	}
	d0, _ := time.ParseInLocation("2006-01-02", "2026-09-11", cnZone())
	if fromMs != d0.UnixMilli() {
		t.Errorf("fromMs = %d", fromMs)
	}
	if toMs != d0.UnixMilli()+4*86_400_000 { // last day start + 2d window padding
		t.Errorf("toMs padding = %d", toMs)
	}
	if _, _, days, _ = resolveRange("2026-09-11", "", ""); len(days) != 1 || days[0] != "2026-09-11" {
		t.Errorf("single day = %v", days)
	}
	if _, _, _, err = resolveRange("", "2026-09-13", ""); err == nil {
		t.Error("from without to must error")
	}
	if _, _, _, err = resolveRange("", "2026-09-13", "2026-09-11"); err == nil {
		t.Error("from after to must error")
	}
	if _, _, _, err = resolveRange("", "2026/09/13", "2026-09-14"); err == nil {
		t.Error("bad date must error")
	}
}

func TestEffectiveWindow(t *testing.T) {
	src := sources.Source{WindowStart: "22:00"} // night shift, end falls back to global
	w, err := effectiveWindow("", src, &config.Config{Daily: config.Daily{WindowEnd: "06:00"}})
	if err != nil || w.Start != "22:00" || w.End != "06:00" {
		t.Errorf("source+global mix = %+v, %v", w, err)
	}
	w, err = effectiveWindow("", sources.Source{}, &config.Config{Daily: config.Daily{WindowStart: "10:00"}})
	if err != nil || w.Start != "10:00" || w.End != "22:00" {
		t.Errorf("partial global = %+v, %v", w, err)
	}
	w, err = effectiveWindow("", sources.Source{}, nil)
	if err != nil || w != (sources.Window{Start: "09:00", End: "22:00"}) {
		t.Errorf("no config = %+v, %v", w, err)
	}
	if w, err = effectiveWindow("08:00-23:00", src, nil); err != nil || w.Start != "08:00" {
		t.Errorf("flag must win: %+v, %v", w, err)
	}
	if _, err = effectiveWindow("bad", sources.Source{}, nil); err == nil {
		t.Error("bad flag window must error")
	}
	if _, err = effectiveWindow("", sources.Source{WindowStart: "25:00"}, nil); err == nil {
		t.Error("bad source edge must error")
	}
}

func TestGroupFitRowsByDate(t *testing.T) {
	dated, dates, undated, err := groupFitRowsByDate([]record.FitRow{
		{Content: "a", Raw: 2, Date: "2026-09-12"},
		{Content: "b", Raw: 3, Date: "2026-09-11"},
		{Content: "c", Raw: 1, Date: "2026-09-11"},
	})
	if err != nil || len(undated) != 0 {
		t.Fatalf("grouping: %v %v", err, undated)
	}
	if strings.Join(dates, ",") != "2026-09-11,2026-09-12" {
		t.Errorf("dates not sorted: %v", dates)
	}
	if len(dated["2026-09-11"]) != 2 || len(dated["2026-09-12"]) != 1 {
		t.Errorf("groups wrong: %+v", dated)
	}
	if _, _, _, err = groupFitRowsByDate([]record.FitRow{{Content: "a", Raw: 1}, {Content: "b", Raw: 1, Date: "2026-09-11"}}); err == nil {
		t.Error("mixed dated/undated must error")
	}
	if _, _, _, err = groupFitRowsByDate([]record.FitRow{{Content: "a", Raw: 1, Date: "9/11"}}); err == nil {
		t.Error("bad date must error")
	}
	if _, dates, undated, err = groupFitRowsByDate([]record.FitRow{{Content: "a", Raw: 1}}); err != nil || len(dates) != 0 || len(undated) != 1 {
		t.Errorf("undated passthrough: %v %v %v", dates, undated, err)
	}
}
