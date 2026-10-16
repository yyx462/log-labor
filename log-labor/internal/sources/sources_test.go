package sources

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// msOf — "+08:00" fixture timestamps → ms epoch.
func msOf(t *testing.T, s string) int64 {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts.UnixMilli()
}

func TestParseWindow(t *testing.T) {
	w, err := ParseWindow("09:00-22:00")
	if err != nil || w.Start != "09:00" || w.End != "22:00" {
		t.Fatalf("ParseWindow: %v %+v", err, w)
	}
	if got := (Window{Start: "09:00", End: "22:00"}).lenMs(); got != 13*3_600_000 {
		t.Errorf("lenMs default = %d", got)
	}
	if got := (Window{Start: "22:00", End: "06:00"}).lenMs(); got != 8*3_600_000 {
		t.Errorf("lenMs night = %d", got)
	}
	if got := (Window{Start: "09:00", End: "09:00"}).lenMs(); got != 24*3_600_000 {
		t.Errorf("lenMs full-day = %d", got)
	}
	for _, bad := range []string{"9-22", "0900-2200", "09:00", "09:00-", "-22:00", "aa:bb-cc:dd"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("ParseWindow(%q) must error", bad)
		}
	}
}

func TestWindowDay(t *testing.T) {
	def := Window{Start: "09:00", End: "22:00"}
	cases := []struct {
		at   string
		day  string
		want bool
	}{
		{"2026-09-11T08:59:00+08:00", "", false},
		{"2026-09-11T09:00:00+08:00", "2026-09-11", true},
		{"2026-09-11T21:59:00+08:00", "2026-09-11", true},
		{"2026-09-11T22:00:00+08:00", "", false},
		{"2026-09-12T02:00:00+08:00", "", false}, // deep-night tail belongs to no day
	}
	for _, c := range cases {
		day, ok := def.windowDay(msOf(t, c.at))
		if ok != c.want || (ok && day != c.day) {
			t.Errorf("default windowDay(%s) = %q,%v; want %q,%v", c.at, day, ok, c.day, c.want)
		}
	}
	// night shift: 22:00–06:00 — an 01:00 message belongs to the PREVIOUS day
	night := Window{Start: "22:00", End: "06:00"}
	day, ok := night.windowDay(msOf(t, "2026-09-12T01:00:00+08:00"))
	if !ok || day != "2026-09-11" {
		t.Errorf("night windowDay(9/12 01:00) = %q,%v; want 2026-09-11,true", day, ok)
	}
	if _, ok := night.windowDay(msOf(t, "2026-09-11T21:00:00+08:00")); ok {
		t.Error("21:00 is outside the night window")
	}
}

func TestCollectClaudeJSONL(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "-Users-x-projA", "a.jsonl"),
		`{"type":"summary","summary":"Fixing the webhook","leafUuid":"x"}
{"type":"user","timestamp":"2026-09-11T10:00:00+08:00","cwd":"/Users/x/projA","sessionId":"s1","message":{"content":"hello"}}
{"type":"assistant","timestamp":"2026-09-11T10:10:00+08:00","cwd":"/Users/x/projA","sessionId":"s1","message":{"content":[{"type":"text","text":"hi"}]}}
`)
	ss, err := CollectClaudeJSONL(root, DefaultWindow(), msOf(t, "2026-09-10T00:00:00+08:00"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("want 1 session, got %d: %+v", len(ss), ss)
	}
	s := ss[0]
	if s.Msgs != 2 || s.Title != "Fixing the webhook" || s.Dir != "/Users/x/projA" {
		t.Errorf("wrong session: %+v", s)
	}
	if s.Start != msOf(t, "2026-09-11T10:00:00+08:00") || s.End != msOf(t, "2026-09-11T10:10:00+08:00") {
		t.Errorf("times: %d..%d", s.Start, s.End)
	}
	if s.Day != "2026-09-11" || s.Span != 10*60_000 {
		t.Errorf("day/span: %+v", s)
	}
	// out-of-window messages never reach the bucket, but do stretch Span
	mustWrite(t, filepath.Join(root, "-Users-x-projA", "b.jsonl"),
		`{"type":"user","timestamp":"2026-09-11T02:00:00+08:00","cwd":"/Users/x/projA","sessionId":"s2","message":{"content":"deep night"}}
{"type":"assistant","timestamp":"2026-09-11T11:00:00+08:00","cwd":"/Users/x/projA","sessionId":"s2","message":{"content":[{"type":"text","text":"day work"}]}}
`)
	ss, err = CollectClaudeJSONL(root, DefaultWindow(), msOf(t, "2026-09-10T00:00:00+08:00"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("want 2 sessions, got %d: %+v", len(ss), ss)
	}
	for _, x := range ss {
		if x.Title == "deep night" && (x.Msgs != 1 || x.Start != x.End) {
			t.Errorf("02:00 message must not count toward the day: %+v", x)
		}
	}
}

func TestCollectGenericJSONL(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "s1.jsonl"),
		"{\"ts\": 1789092000, \"project\": \"/p/A\", \"sid\": \"s1\", \"what\": \"epoch seconds\"}\n"+
			"{\"ts\": 1789092600000, \"project\": \"/p/A\", \"sid\": \"s1\", \"what\": \"epoch ms\"}\n")
	mustWrite(t, filepath.Join(root, "s2.jsonl"),
		"{\"ts\": \"2026-09-11T11:50:00+08:00\", \"project\": \"/p/B\", \"sid\": \"s2\", \"what\": \"rfc3339\"}\n")
	s := Source{Name: "g", Format: "jsonl-generic", Path: root,
		TimestampField: "ts", CwdField: "project", SessionField: "sid", TitleField: "what"}
	fullDay := Window{Start: "00:00", End: "00:00"} // 24h: everything counts
	ss, err := CollectGenericJSONL(s, fullDay, t0msOf(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(ss))
	}
	byID := map[string]Session{}
	for _, x := range ss {
		byID[x.Dir] = x
	}
	a, b := byID["/p/A"], byID["/p/B"]
	if a.Msgs != 2 || a.Start != t0msOf() || a.End != t1msOf() {
		t.Errorf("epoch session wrong: %+v", a)
	}
	if b.Msgs != 1 || b.Title != "rfc3339" {
		t.Errorf("rfc session wrong: %+v", b)
	}
	if _, err := CollectGenericJSONL(Source{Format: "jsonl-generic", Path: root}, fullDay, t0msOf(), 0); err == nil {
		t.Error("missing --timestamp-field must error")
	}
}

// t0msOf/t1msOf — the legacy fixtures (2026-09-11 10:00/10:10 +08;
// kept for the epoch-seconds/ms parse checks).
func t0msOf() int64 {
	ts, _ := time.Parse(time.RFC3339, "2026-09-11T10:00:00+08:00")
	return ts.UnixMilli()
}
func t1msOf() int64 {
	ts, _ := time.Parse(time.RFC3339, "2026-09-11T10:10:00+08:00")
	return ts.UnixMilli()
}

func TestCollectAllWindowAndLeaks(t *testing.T) {
	root := t.TempDir()
	mk := func(name, sid string, msgs ...string) {
		mustWrite(t, filepath.Join(root, name), strings.Join(msgs, "\n")+"\n")
	}
	// s1: one session alive on 9/11 AND 9/13 (the 9/11-report leak shape):
	// rows must split per day — the 9/11 row ends at 9/11's last message.
	mk("s1.jsonl", "s1",
		`{"ts":"2026-09-11T09:57:00+08:00","project":"/p/A","sid":"s1","what":"day work"}`,
		`{"ts":"2026-09-11T21:00:00+08:00","project":"/p/A","sid":"s1","what":"day work"}`,
		`{"ts":"2026-09-13T10:00:00+08:00","project":"/p/A","sid":"s1","what":"later days"}`)
	// long: alive 9/8→9/13 (≥72h) — excluded with a warning
	mk("long.jsonl", "long",
		`{"ts":"2026-09-08T10:00:00+08:00","project":"/p/B","sid":"long","what":"eternal"}`,
		`{"ts":"2026-09-11T15:00:00+08:00","project":"/p/B","sid":"long","what":"eternal"}`,
		`{"ts":"2026-09-13T10:00:00+08:00","project":"/p/B","sid":"long","what":"eternal"}`)
	// night owl: only messages at 02:00 — outside every default window
	mk("night.jsonl", "night",
		`{"ts":"2026-09-11T02:00:00+08:00","project":"/p/C","sid":"night","what":"insomnia"}`)
	g := Source{Name: "g", Format: "jsonl-generic", Path: root,
		TimestampField: "ts", CwdField: "project", SessionField: "sid", TitleField: "what"}
	from := msOf(t, "2026-09-11T00:00:00+08:00")
	out, warns := CollectAll([]Source{g, {Name: "broken", Format: "opencode-sqlite", Path: "/nonexistent/db"}},
		func(Source) Window { return DefaultWindow() },
		from, from+3*86_400_000, []string{"2026-09-11"}, nil)
	if len(warns) != 2 {
		t.Fatalf("want broken-source + long-session warnings, got %v", warns)
	}
	if !strings.Contains(warns[0], "长会话") || !strings.Contains(warns[0], "eternal") {
		t.Errorf("long-session warning wrong: %q", warns[0])
	}
	if !strings.Contains(warns[1], "[broken]") {
		t.Errorf("broken source must degrade to a warning: %q", warns[1])
	}
	if len(out) != 1 {
		t.Fatalf("want exactly the 9/11 row of s1, got %+v (warns %v)", out, warns)
	}
	r := out[0]
	if r.Day != "2026-09-11" || r.Source != "g" {
		t.Errorf("row tags wrong: %+v", r)
	}
	if r.Start != msOf(t, "2026-09-11T09:57:00+08:00") || r.End != msOf(t, "2026-09-11T21:00:00+08:00") {
		t.Errorf("9/11 row must end at 9/11's own last message, not bleed into 9/13: %+v", r)
	}
	if r.Msgs != 2 || r.Span < 48*3_600_000 {
		t.Errorf("msgs/span wrong (span must span the real 9/11→9/13 life): %+v", r)
	}
	// the same session CAN appear on 9/13 when that day is requested
	out, _ = CollectAll([]Source{g}, func(Source) Window { return DefaultWindow() },
		from, from+3*86_400_000, []string{"2026-09-11", "2026-09-13"}, nil)
	if len(out) != 2 {
		t.Fatalf("want one row per day for s1, got %+v", out)
	}
	if out[0].Day != "2026-09-11" || out[1].Day != "2026-09-13" {
		t.Errorf("per-day rows wrong: %+v / %+v", out[0], out[1])
	}
}

func TestKnownFormats(t *testing.T) {
	known := map[string]bool{}
	for _, f := range KnownFormats {
		known[f] = true
	}
	for _, want := range []string{"opencode-sqlite", "jsonl-claude", "jsonl-generic"} {
		if !known[want] {
			t.Errorf("%s missing from KnownFormats", want)
		}
	}
	if _, err := Collect(Source{Format: "jsonl-claude", Path: "/nope"}, DefaultWindow(), 0, 0); err == nil || strings.Contains(err.Error(), "unknown format") {
		t.Errorf("known format should pass dispatch: %v", err)
	}
	if _, err := Collect(Source{Format: "bogus", Path: "/nope"}, DefaultWindow(), 0, 0); err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Error("bogus format must error with unknown format")
	}
}

// fixtureDB — build a tiny opencode-style sqlite store via the real
// sqlite3 CLI. family "v1" = session+message, "v2" =
// session_v2+session_message; rows is raw SQL appended after the schema.
func fixtureDB(t *testing.T, family, rows string) string {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not available")
	}
	db := filepath.Join(t.TempDir(), "opencode.db")
	schema := map[string]string{
		"v1": `CREATE TABLE session (id text PRIMARY KEY, directory text NOT NULL, title text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL);`,
		// v2: title nullable, session_message carries a type column
		"v2": `CREATE TABLE session_v2 (id text PRIMARY KEY, directory text NOT NULL, title text, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL, type text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL);`,
	}[family]
	cmd := exec.Command("sqlite3", db)
	cmd.Stdin = strings.NewReader(schema + rows)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture db: %v\n%s", err, out)
	}
	return db
}

func TestCollectOpencodeV2AndFallback(t *testing.T) {
	ten := msOf(t, "2026-09-11T10:00:00+08:00") // in-window
	tenTen := msOf(t, "2026-09-11T10:10:00+08:00")
	sep10 := msOf(t, "2026-09-10T00:00:00+08:00") // fetch range start

	msg := func(id, sid, typ string, at int64) string {
		return fmt.Sprintf("INSERT INTO session_message VALUES ('%s','%s','%s',%d,%d);\n", id, sid, typ, at, at)
	}
	db := fixtureDB(t, "v2",
		fmt.Sprintf("INSERT INTO session_v2 VALUES ('ses_a','/p/A','V2 schema fix',%d,%d);\n", ten, tenTen)+
			// real work: one user + one assistant…
			msg("m1", "ses_a", "user", ten)+msg("m2", "ses_a", "assistant", tenTen)+
			// …plus harness noise v1 never had — must not count
			msg("m3", "ses_a", "system", msOf(t, "2026-09-11T10:05:00+08:00"))+
			msg("m4", "ses_a", "idle", msOf(t, "2026-09-11T10:06:00+08:00")))
	ss, err := CollectOpencode(db, DefaultWindow(), sep10, sep10+3*86_400_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("want 1 session, got %d: %+v", len(ss), ss)
	}
	s := ss[0]
	if s.Msgs != 2 || s.Title != "V2 schema fix" || s.Dir != "/p/A" || s.Day != "2026-09-11" {
		t.Errorf("v2 session wrong (noise must not count): %+v", s)
	}
	if s.Start != ten || s.End != tenTen || s.Span != 10*60_000 {
		t.Errorf("v2 times/span wrong: %+v", s)
	}

	// v1-only store (pre-2.0 opencode): same shape, no type column
	db1 := fixtureDB(t, "v1",
		fmt.Sprintf("INSERT INTO session VALUES ('ses_b','/p/B','V1 fallback',%d,%d);\n", ten, tenTen)+
			fmt.Sprintf("INSERT INTO message VALUES ('n1','ses_b',%d,%d);\n", ten, ten)+
			fmt.Sprintf("INSERT INTO message VALUES ('n2','ses_b',%d,%d);\n", tenTen, tenTen))
	ss, err = CollectOpencode(db1, DefaultWindow(), sep10, sep10+3*86_400_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].Msgs != 2 || ss[0].Title != "V1 fallback" {
		t.Fatalf("v1 fallback broken: %+v err=%v", ss, err)
	}

	// THE regression: a day with zero rows must come back empty, not
	// as a parse error (sqlite3 -json prints nothing for a zero-row
	// grouped query — this killed the 2026-10-12+ auto-daily runs).
	ss, err = CollectOpencode(db, DefaultWindow(), msOf(t, "2026-09-12T00:00:00+08:00"), msOf(t, "2026-09-13T00:00:00+08:00"))
	if err != nil {
		t.Fatalf("zero-row day must not error: %v", err)
	}
	if len(ss) != 0 {
		t.Fatalf("zero-row day must return no sessions, got %+v", ss)
	}
}

func TestCompileExcludesAndMatch(t *testing.T) {
	reps, err := CompileExcludes([]string{"job-seeking", `求职|interview`})
	if err != nil || len(reps) != 2 {
		t.Fatalf("CompileExcludes: %v, %d", err, len(reps))
	}
	if _, err := CompileExcludes([]string{"ok", "("}); err == nil || !strings.Contains(err.Error(), `bad exclude pattern "("`) {
		t.Errorf("bad pattern must fail the whole set loudly: %v", err)
	}
	if _, err := CompileExcludes(nil); err != nil {
		t.Errorf("no patterns = no excludes, not an error: %v", err)
	}

	work := Session{Dir: "/Users/x/projects/NWi", Title: "Reduce noisy ruff output"}
	if MatchExcluded(reps, work) {
		t.Error("work session must survive the filter")
	}
	// basename hit …
	if !MatchExcluded(reps, Session{Dir: "/Users/x/projects/job-seeking", Title: "anything"}) {
		t.Error("project basename match must exclude")
	}
	// … title hit (personal content under an innocent dir) …
	if !MatchExcluded(reps, Session{Dir: "/Users/x/projects/NWi", Title: "AI 工程师求职面试准备"}) {
		t.Error("title match must exclude")
	}
	// … and a clean session with an empty dir is kept (never a blanket match)
	if MatchExcluded(reps, Session{Title: "fix webhook retry"}) {
		t.Error("clean session with empty dir must survive")
	}
	if MatchExcluded(nil, Session{Dir: "/p/job-seeking", Title: "求职"}) {
		t.Error("nil excludes = filter off")
	}
}

func TestCollectAllExcludes(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, lines ...string) {
		mustWrite(t, filepath.Join(root, name), strings.Join(lines, "\n")+"\n")
	}
	day := "2026-09-11"
	mk("work.jsonl",
		`{"ts":"2026-09-11T10:00:00+08:00","project":"/p/NWi","sid":"w1","what":"real work"}`)
	mk("personal.jsonl",
		`{"ts":"2026-09-11T11:00:00+08:00","project":"/p/job-seeking/leetcode","sid":"p1","what":"drill"}`,
		`{"ts":"2026-09-11T12:00:00+08:00","project":"/p/NWi","sid":"p2","what":"求职简历项目筛选"}`)
	g := Source{Name: "g", Format: "jsonl-generic", Path: root,
		TimestampField: "ts", CwdField: "project", SessionField: "sid", TitleField: "what"}
	from := msOf(t, day+"T00:00:00+08:00")
	reps, err := CompileExcludes([]string{"job-seeking|leetcode|求职"})
	if err != nil {
		t.Fatal(err)
	}
	out, warns := CollectAll([]Source{g}, func(Source) Window { return DefaultWindow() },
		from, from+86_400_000, []string{day}, reps)
	if len(out) != 1 || out[0].Title != "real work" {
		t.Fatalf("excludes must drop the 2 personal sessions, keep work: %+v", out)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "已按排除规则过滤 2 个会话") {
			found = true
		}
	}
	if !found {
		t.Errorf("dropped count must be reported once: %v", warns)
	}
	// filter off → everything back
	out, _ = CollectAll([]Source{g}, func(Source) Window { return DefaultWindow() },
		from, from+86_400_000, []string{day}, nil)
	if len(out) != 3 {
		t.Fatalf("nil excludes must keep all 3 sessions, got %d", len(out))
	}
}

func TestCollectCodexJSONL(t *testing.T) {
	root := t.TempDir()
	day := "2026/09/11"
	// faithful rollout shape: session_meta → boilerplate user → real user → assistant
	mustWrite(t, filepath.Join(root, day, "rollout-20260911T100000-abc.jsonl"),
		`{"timestamp":"2026-09-11T10:00:00+08:00","type":"session_meta","payload":{"id":"019e-abc","timestamp":"2026-09-11T10:00:00+08:00","cwd":"/Users/x/projC","originator":"codex_cli_rs","cli_version":"1.0"}}
{"timestamp":"2026-09-11T10:00:01+08:00","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<user_instructions>/tmp/AGENTS.md</user_instructions>"}]}}
{"timestamp":"2026-09-11T10:00:05+08:00","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"修复 nightly 测试"}]}}
{"timestamp":"2026-09-11T10:30:00+08:00","type":"event_msg","payload":{"type":"user_message","message":"顺便跑一下 CI"}}
`)
	// a rollout with no session_meta: dir falls back to empty, title from event_msg
	mustWrite(t, filepath.Join(root, day, "rollout-20260911T120000-def.jsonl"),
		`{"timestamp":"2026-09-11T12:00:00+08:00","type":"event_msg","payload":{"type":"user_message","message":"调试 airflow dag"}}`)

	ss, err := CollectCodexJSONL(root, DefaultWindow(), msOf(t, "2026-09-10T00:00:00+08:00"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("want 2 sessions, got %d: %+v", len(ss), ss)
	}
	byTitle := map[string]Session{}
	for _, s := range ss {
		byTitle[s.Title] = s
		if s.Day != "2026-09-11" {
			t.Errorf("day tag wrong: %+v", s)
		}
	}
	main, ok := byTitle["修复 nightly 测试"]
	if !ok {
		t.Fatalf("boilerplate title must lose to real user text: %+v", ss)
	}
	if main.Dir != "/Users/x/projC" || main.Msgs != 4 || main.Start != msOf(t, "2026-09-11T10:00:00+08:00") {
		t.Errorf("meta session wrong: %+v", main)
	}
	def, ok := byTitle["调试 airflow dag"]
	if !ok || def.Dir != "" {
		t.Errorf("meta-less session wrong: %+v", def)
	}
}
