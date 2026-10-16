// Package sources — pluggable session-store collectors for
// `log-labor daily collect`. Built-ins auto-detect when their default
// store exists; anything else is registered by the agent at setup
// (see skill: "Session sources — setup for any agent").
package sources

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Session — one agent session in the digest, already clipped to ONE
// day's work window. A session alive across several days yields one
// row per day; Span is the session's whole-life length (for the ≥72h
// long-session rule), not the row's.
type Session struct {
	Source string `json:"source"` // tag printed in the digest
	Dir    string `json:"dir"`
	Title  string `json:"title"`
	Start  int64  `json:"start"`          // ms epoch, in-window
	End    int64  `json:"end"`            // ms epoch, in-window
	Msgs   int    `json:"msgs"`           // in-window messages only
	Day    string `json:"day"`            // window-day "2006-01-02" (+08)
	Span   int64  `json:"-"`              // whole-session length ms (72h rule)
	GStart int64  `json:"-"`              // whole-session first message ms (warning display)
	File   string `json:"file,omitempty"` // source jsonl (title fallbacks)
}

// LongSession — a session whose whole-life span reaches this can't be
// attributed to specific days honestly; it is excluded with a warning
// instead of polluting the digest.
const LongSession = 72 * time.Hour

// cst — the sheet's timezone, fixed +08 (matches cmd's cnZone).
var cst = time.FixedZone("CST", 8*3600)

// Window — one harness's daily work window in +08 wall clock
// ("09:00"–"22:00" default; end ≤ start means the window crosses
// midnight, e.g. a night-shift "22:00"–"06:00"). Messages outside the
// window don't count; a message belongs to the day whose window
// contains it.
type Window struct{ Start, End string }

// DefaultWindow — 09:00–22:00 +08.
func DefaultWindow() Window { return Window{Start: "09:00", End: "22:00"} }

// ParseWindow — "09:00-22:00" → Window; validates both HH:MM ends.
func ParseWindow(s string) (Window, error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return Window{}, fmt.Errorf("bad window %q — want START-END, e.g. 09:00-22:00", s)
	}
	w := Window{Start: strings.TrimSpace(a), End: strings.TrimSpace(b)}
	return w, w.Valid()
}

// Valid — both ends are HH:MM.
func (w Window) Valid() error {
	for _, p := range []string{w.Start, w.End} {
		if _, err := time.ParseInLocation("15:04", p, cst); err != nil {
			return fmt.Errorf("bad window edge %q — want HH:MM", p)
		}
	}
	return nil
}

func parseHM(p string) int64 {
	t, _ := time.ParseInLocation("15:04", p, cst)
	h, m, _ := t.Clock()
	return int64(h)*3_600_000 + int64(m)*60_000
}

// offsetMs — window start as ms-of-day; subtracting it from any
// timestamp maps window start to 00:00 of the window-day.
func (w Window) offsetMs() int64 { return parseHM(w.Start) }

// lenMs — window length; end == start means a full 24h day.
func (w Window) lenMs() int64 {
	d := parseHM(w.End) - parseHM(w.Start)
	if d <= 0 {
		d += 24 * 3_600_000
	}
	return d
}

// windowDay — the day key ("2006-01-02", +08) whose window contains
// ms; ok=false when ms falls outside every window (e.g. 03:00 under
// the default 09:00–22:00 window).
func (w Window) windowDay(ms int64) (string, bool) {
	shifted := time.UnixMilli(ms - w.offsetMs()).In(cst) // window start → 00:00
	tod := int64(shifted.Hour())*3_600_000 + int64(shifted.Minute())*60_000 +
		int64(shifted.Second())*1_000 + int64(shifted.Nanosecond())/1_000_000
	if tod >= w.lenMs() {
		return "", false
	}
	return shifted.Format("2006-01-02"), true
}

// Source — one registered or built-in session store.
type Source struct {
	Name   string `json:"name"`   // digest tag (opencode, claude, …)
	Format string `json:"format"` // opencode-sqlite | jsonl-claude | jsonl-generic
	Path   string `json:"path"`   // db file or directory root

	// this harness's own work window (HH:MM +08), overriding the
	// config's global daily window — e.g. a night-shift
	// "22:00"–"06:00". Either edge set alone: the other falls back to
	// the global window's edge.
	WindowStart string `json:"window_start,omitempty"`
	WindowEnd   string `json:"window_end,omitempty"`

	// jsonl-generic only: which JSON keys hold the signal.
	TimestampField string `json:"timestamp_field,omitempty"`
	CwdField       string `json:"cwd_field,omitempty"`
	SessionField   string `json:"session_field,omitempty"`
	TitleField     string `json:"title_field,omitempty"`
}

// KnownFormats — formats `daily sources add` accepts.
var KnownFormats = []string{"opencode-sqlite", "jsonl-claude", "jsonl-codex", "jsonl-generic"}

// DefaultOpencodeDB — opencode's standard sqlite store.
func DefaultOpencodeDB() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// DefaultClaudeDir — Claude Code's per-project JSONL store.
func DefaultClaudeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// DefaultCodexDir — OpenAI Codex CLI rollout store.
func DefaultCodexDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

// BuiltIns — the verified defaults, included only when their store
// exists on this machine.
func BuiltIns() []Source {
	var out []Source
	if _, err := os.Stat(DefaultOpencodeDB()); err == nil {
		out = append(out, Source{Name: "opencode", Format: "opencode-sqlite", Path: DefaultOpencodeDB()})
	}
	if st, err := os.Stat(DefaultClaudeDir()); err == nil && st.IsDir() {
		out = append(out, Source{Name: "claude", Format: "jsonl-claude", Path: DefaultClaudeDir()})
	}
	if st, err := os.Stat(DefaultCodexDir()); err == nil && st.IsDir() {
		out = append(out, Source{Name: "codex", Format: "jsonl-codex", Path: DefaultCodexDir()})
	}
	return out
}

// Collect — dispatch by format. win clips messages to one harness's
// work window; [fromMs, toMs) is the padded fetch range (day-aligned).
func Collect(s Source, win Window, fromMs, toMs int64) ([]Session, error) {
	switch s.Format {
	case "opencode-sqlite":
		return CollectOpencode(s.Path, win, fromMs, toMs)
	case "jsonl-claude":
		return CollectClaudeJSONL(s.Path, win, fromMs, toMs)
	case "jsonl-codex":
		return CollectCodexJSONL(s.Path, win, fromMs, toMs)
	case "jsonl-generic":
		return CollectGenericJSONL(s, win, fromMs, toMs)
	default:
		return nil, fmt.Errorf("unknown format %q (want one of %s)", s.Format, strings.Join(KnownFormats, "|"))
	}
}

// CollectAll — run every source with ITS effective window (resolved by
// the caller), merge, drop rows for days outside the requested set,
// apply the exclusion rules (personal work: never reaches the sheet),
// apply the ≥72h long-session rule (warn once per session), sort by
// start. Per-source errors degrade to a warning line, never abort.
func CollectAll(srcs []Source, win func(Source) Window, fromMs, toMs int64, days []string, excludes []*regexp.Regexp) ([]Session, []string) {
	want := map[string]bool{}
	for _, d := range days {
		want[d] = true
	}
	var out []Session
	var warns []string
	longSeen := map[string]bool{}
	excluded := 0
	for _, s := range srcs {
		w := win(s)
		ss, err := Collect(s, w, fromMs, toMs)
		if err != nil {
			warns = append(warns, fmt.Sprintf("[%s] skipped: %v", s.Name, err))
			continue
		}
		for i := range ss {
			ss[i].Source = s.Name
			if !want[ss[i].Day] {
				continue // fetch-range padding, not a requested day
			}
			if MatchExcluded(excludes, ss[i]) {
				excluded++
				continue
			}
			if time.Duration(ss[i].Span)*time.Millisecond >= LongSession {
				key := fmt.Sprintf("%s|%s|%s|%d", s.Name, ss[i].Dir, ss[i].Title, ss[i].Span)
				if !longSeen[key] {
					longSeen[key] = true
					warns = append(warns, fmt.Sprintf(
						"长会话 (≥72h) 已剔除，无法按天归属 — 请拆分会话: [%s] %s→%s %s",
						s.Name,
						time.UnixMilli(ss[i].GStart).In(cst).Format("01-02 15:04"),
						time.UnixMilli(ss[i].GStart+ss[i].Span).In(cst).Format("01-02 15:04"),
						ss[i].Title))
				}
				continue
			}
			out = append(out, ss[i])
		}
	}
	if excluded > 0 {
		warns = append(warns, fmt.Sprintf("已按排除规则过滤 %d 个会话 (daily exclude)", excluded))
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start < out[b].Start })
	return out, warns
}

// CompileExcludes — compile the config's exclude regexes; one bad
// pattern fails the WHOLE set (callers abort loudly — a silently
// ignored rule could leak personal sessions onto the work sheet).
func CompileExcludes(patterns []string) ([]*regexp.Regexp, error) {
	var reps []*regexp.Regexp
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("bad exclude pattern %q: %v", p, err)
		}
		reps = append(reps, re)
	}
	return reps, nil
}

// MatchExcluded — a session is excluded when ANY pattern matches its
// project dir basename, full dir, or title.
func MatchExcluded(excludes []*regexp.Regexp, s Session) bool {
	base := filepath.Base(s.Dir)
	for _, re := range excludes {
		if re.MatchString(base) || (s.Dir != "" && re.MatchString(s.Dir)) || re.MatchString(s.Title) {
			return true
		}
	}
	return false
}

// CollectOpencode — sqlite via the sqlite3 CLI (zero Go deps,
// read-only). Two grouped queries: per-(session, window-day) in-window
// buckets over the fetch range, plus whole-life MIN/MAX per session
// (the 72h rule needs the true span, not just what the range sees).
// ms epoch in/out.
//
// Schema generations: opencode ≥ 2.0 renamed session→session_v2 and
// message→session_message (same db file), and its migration COPIES the
// old rows across — v2 is a superset, so reading both would
// double-count. v2 wins when present; v1 (session + message) is the
// pre-2.0 fallback. session_message rows also carry harness noise
// (system/synthetic/idle/compaction) that v1's message table never
// held — only real user/assistant rows count.
func CollectOpencode(db string, win Window, fromMs, toMs int64) ([]Session, error) {
	if _, err := os.Stat(db); err != nil {
		return nil, fmt.Errorf("no store at %s", db)
	}
	msgs, sess, noise := "message", "session", ""
	if hasV2Tables(db) {
		msgs, sess = "session_message", "session_v2"
		noise = " AND m.type IN ('user','assistant')"
	}
	// windowDay(ms) in SQL: shift back by window start AND +8h zone
	// (SQLite % is UTC-anchored and C-style), take the +08 calendar
	// day; double-mod keeps negative dividends from leaking through.
	q := fmt.Sprintf(`SELECT MIN(m.time_created) AS t0, MAX(m.time_created) AS t1,
		COUNT(*) AS msgs, m.session_id AS sid, s.directory AS dir, s.title AS title,
		date((m.time_created - %d + 28800000)/1000, 'unixepoch') AS day
		FROM %s m JOIN %s s ON s.id = m.session_id
		WHERE m.time_created >= %d AND m.time_created < %d%s
		  AND ((m.time_created - %d + 28800000) %% 86400000 + 86400000) %% 86400000 < %d
		GROUP BY m.session_id, day ORDER BY t0;`,
		win.offsetMs(), msgs, sess, fromMs, toMs, noise, win.offsetMs(), win.lenMs())
	noiseWhere := ""
	if noise != "" {
		noiseWhere = " WHERE type IN ('user','assistant')"
	}
	spanQ := fmt.Sprintf(`SELECT session_id AS sid, MIN(time_created) AS g0, MAX(time_created) AS g1
		FROM %s%s GROUP BY session_id;`, msgs, noiseWhere)
	rowsJSON, err := exec.Command("sqlite3", "-json", "-readonly", db, q).Output()
	if err != nil {
		return nil, sqliteErr("collect", err)
	}
	spanJSON, err := exec.Command("sqlite3", "-json", "-readonly", db, spanQ).Output()
	if err != nil {
		return nil, sqliteErr("spans", err)
	}
	var rows []struct {
		T0    int64  `json:"t0"`
		T1    int64  `json:"t1"`
		Msgs  int    `json:"msgs"`
		Sid   string `json:"sid"`
		Dir   string `json:"dir"`
		Title string `json:"title"`
		Day   string `json:"day"`
	}
	if err := unmarshalJSONRows(rowsJSON, &rows); err != nil {
		return nil, fmt.Errorf("parse sqlite output: %v", err)
	}
	var spans []struct {
		Sid string `json:"sid"`
		G0  int64  `json:"g0"`
		G1  int64  `json:"g1"`
	}
	if err := unmarshalJSONRows(spanJSON, &spans); err != nil {
		return nil, fmt.Errorf("parse sqlite spans: %v", err)
	}
	type gspan struct{ start, span int64 }
	g := map[string]gspan{}
	for _, sp := range spans {
		g[sp.Sid] = gspan{start: sp.G0, span: sp.G1 - sp.G0}
	}
	ss := make([]Session, 0, len(rows))
	for _, r := range rows {
		gs := g[r.Sid]
		ss = append(ss, Session{Dir: r.Dir, Title: r.Title, Start: r.T0, End: r.T1,
			Msgs: r.Msgs, Day: r.Day, Span: gs.span, GStart: gs.start})
	}
	return ss, nil
}

func sqliteErr(what string, err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("sqlite3 %s: %s", what, strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("sqlite3 %s failed: %v", what, err)
}

// hasV2Tables — does this db carry the opencode ≥ 2.0 schema
// (session_v2 + session_message)? One cheap sqlite_master read; any
// surprise (no sqlite3, locked db, odd output) reads as "v1".
func hasV2Tables(db string) bool {
	out, err := exec.Command("sqlite3", "-json", "-readonly", db,
		`SELECT COUNT(*) AS n FROM sqlite_master WHERE type='table'
		 AND name IN ('session_v2','session_message');`).Output()
	var rows []struct {
		N int `json:"n"`
	}
	if err != nil || unmarshalJSONRows(out, &rows) != nil || len(rows) == 0 {
		return false
	}
	return rows[0].N == 2
}

// unmarshalJSONRows — sqlite3 -json prints NOTHING (not []) for a
// zero-row grouped query; empty output must read as "no rows", not a
// parse error. (This exact quirk silently killed the 2026-10-12+
// auto-daily runs: every source "skipped" → generic fallback row.)
func unmarshalJSONRows(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// CollectClaudeJSONL — Claude Code's per-project store: <root>/<munged-
// cwd>/*.jsonl, line-JSON with timestamp (RFC3339), cwd, sessionId.
// Title: first "summary" line, else first user message text.
func CollectClaudeJSONL(root string, win Window, fromMs, _ int64) ([]Session, error) {
	summaries := map[string]string{} // file → first summary line
	ss, err := walkJSONL(root, win, fromMs, func(o map[string]any, acc *acc) {
		if t, _ := o["type"].(string); t == "summary" {
			if _, ok := summaries[acc.File]; !ok {
				summaries[acc.File], _ = o["summary"].(string)
			}
			return
		}
		ms, ok := parseTime(o["timestamp"])
		if !ok {
			return
		}
		acc.see(win, ms)
		if t2, _ := o["type"].(string); acc.Title == "" && t2 == "user" {
			acc.Title = claudeUserText(o["message"])
		}
	}, func(o map[string]any) string {
		s, _ := o["sessionId"].(string)
		return s
	}, func(o map[string]any) string {
		s, _ := o["cwd"].(string)
		return s
	})
	if err != nil {
		return nil, err
	}
	for i := range ss { // file summary (Claude's own distillation) wins; user text is the fallback
		if summary := summaries[ss[i].File]; summary != "" {
			ss[i].Title = summary
		}
	}
	return ss, nil
}

// codexBoilerplate — Codex injects context as fake user messages;
// none of these belong in a digest title.
var codexBoilerplate = []string{
	"<environment_context", "<permissions", "# AGENTS.md",
	"<image", "<user_instructions", "<turn_context",
}

func isRealUserText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, p := range codexBoilerplate {
		if strings.HasPrefix(s, p) {
			return false
		}
	}
	return true
}

// codexUserText — first real user ask from a rollout line:
// event_msg/user_message.payload.message, or response_item/user
// content[].input_text.
func codexUserText(o map[string]any) string {
	t, _ := o["type"].(string)
	p, _ := o["payload"].(map[string]any)
	if p == nil {
		return ""
	}
	switch t {
	case "event_msg":
		if pt, _ := p["type"].(string); pt == "user_message" {
			if m, _ := p["message"].(string); isRealUserText(m) {
				return truncate(m)
			}
		}
	case "response_item":
		if pt, _ := p["type"].(string); pt == "message" {
			if role, _ := p["role"].(string); role == "user" {
				if cs, ok := p["content"].([]any); ok {
					for _, c := range cs {
						cm, _ := c.(map[string]any)
						if tm, _ := cm["type"].(string); tm == "input_text" {
							if x, _ := cm["text"].(string); isRealUserText(x) {
								return truncate(x)
							}
						}
					}
				}
			}
		}
	}
	return ""
}

// CollectCodexJSONL — OpenAI Codex CLI rollout store (~/.codex/sessions):
// line-JSON {timestamp, type, payload}; one rollout file = one session
// (session_meta carries uuid + cwd, but every line is file-scoped, so
// the session key is per-file). Title: first real user message —
// boilerplate context injections never win.
func CollectCodexJSONL(root string, win Window, fromMs, _ int64) ([]Session, error) {
	return walkJSONL(root, win, fromMs, func(o map[string]any, acc *acc) {
		ms, ok := parseTime(o["timestamp"])
		if !ok {
			return
		}
		acc.see(win, ms)
		if acc.Title == "" {
			acc.Title = codexUserText(o)
		}
	}, func(o map[string]any) string {
		return "" // per-file: one rollout = one session
	}, func(o map[string]any) string {
		if p, ok := o["payload"].(map[string]any); ok {
			if c, _ := p["cwd"].(string); c != "" {
				return c
			}
		}
		return ""
	})
}

// CollectGenericJSONL — declared-field line-JSON: the escape hatch for
// "special agents". Timestamp accepts epoch seconds/ms (number or
// numeric string) or RFC3339.
func CollectGenericJSONL(s Source, win Window, fromMs, _ int64) ([]Session, error) {
	if s.TimestampField == "" {
		return nil, fmt.Errorf("jsonl-generic needs --timestamp-field")
	}
	return walkJSONL(s.Path, win, fromMs, func(o map[string]any, acc *acc) {
		ms, ok := parseTime(o[s.TimestampField])
		if !ok {
			return
		}
		acc.see(win, ms)
		if acc.Title == "" && s.TitleField != "" {
			acc.Title, _ = o[s.TitleField].(string)
		}
	}, func(o map[string]any) string {
		if s.SessionField == "" {
			return "" // one anonymous session per file
		}
		v, _ := o[s.SessionField].(string)
		return v
	}, func(o map[string]any) string {
		if s.CwdField == "" {
			return ""
		}
		v, _ := o[s.CwdField].(string)
		return v
	})
}

// acc — per (file, session) accumulator: whole-life span (the 72h
// rule) plus one in-window bucket per day (bacc).
type acc struct {
	Start, End int64 // global first/last message ms
	Msgs       int   // global message count
	Title      string
	Dir        string
	File       string // source jsonl (for file-scoped fallbacks)
	buckets    map[string]*bacc
}

// bacc — one window-day's in-window span + message count.
type bacc struct {
	Start, End int64
	Msgs       int
}

// see — record one message: global span always; the day bucket only
// when the window contains it (messages between midnight and 09:00
// under the default window simply don't count toward any day).
func (a *acc) see(w Window, ms int64) {
	if a.Start == 0 || ms < a.Start {
		a.Start = ms
	}
	if ms > a.End {
		a.End = ms
	}
	a.Msgs++
	day, ok := w.windowDay(ms)
	if !ok {
		return
	}
	if a.buckets == nil {
		a.buckets = map[string]*bacc{}
	}
	b := a.buckets[day]
	if b == nil {
		b = &bacc{Start: ms, End: ms}
		a.buckets[day] = b
	}
	if ms < b.Start {
		b.Start = ms
	}
	if ms > b.End {
		b.End = ms
	}
	b.Msgs++
}

// rows — one Session per day bucket; Span/GStart carry the whole-life
// values CollectAll needs for the 72h rule.
func (a *acc) rows() []Session {
	out := make([]Session, 0, len(a.buckets))
	for day, b := range a.buckets {
		out = append(out, Session{Dir: a.Dir, Title: a.Title, Start: b.Start, End: b.End,
			Msgs: b.Msgs, Day: day, Span: a.End - a.Start, GStart: a.Start, File: a.File})
	}
	return out
}

// walkJSONL — recurse root for *.jsonl, group lines by sessionKey
// (falling back to the file path when the store has no session ids),
// letting onLine read store-specific fields. Files' ModTime earlier
// than fromMs are skipped unread; sessions started BEFORE the range
// still count for days they were alive on (no start-day cutoff).
func walkJSONL(root string, win Window, fromMs int64,
	onLine func(o map[string]any, a *acc),
	sessionKey func(o map[string]any) string,
	cwd func(o map[string]any) string,
) ([]Session, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("no store at %s", root)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	accs := map[string]*acc{}
	var order []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if fi, err := d.Info(); err != nil || fi.ModTime().UnixMilli() < fromMs {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		perFile := map[string]*acc{}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var o map[string]any
			if json.Unmarshal([]byte(line), &o) != nil {
				continue
			}
			k := sessionKey(o)
			if _, ok := perFile[k]; !ok {
				perFile[k] = &acc{File: path}
			}
			a := perFile[k]
			onLine(o, a)
			if a.Dir == "" {
				a.Dir = cwd(o)
			}
		}
		for k, a := range perFile {
			if a.Msgs == 0 {
				continue
			}
			id := path + "\x00" + k
			accs[id] = a
			order = append(order, id)
		}
		return nil
	})
	var out []Session
	for _, id := range order {
		out = append(out, accs[id].rows()...)
	}
	return out, nil
}

// parseTime — epoch seconds/ms (number or numeric string) or RFC3339.
func parseTime(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return normEpoch(int64(x)), true
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return normEpoch(n), true
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, x); err == nil {
				return t.UnixMilli(), true
			}
		}
	}
	return 0, false
}

func normEpoch(n int64) int64 {
	if n < 1_000_000_000_000 { // seconds
		return n * 1000
	}
	return n
}

// claudeUserText — message.content is a string or [{type:text,text:…}].
func claudeUserText(msg any) string {
	m, ok := msg.(map[string]any)
	if !ok {
		return ""
	}
	switch c := m["content"].(type) {
	case string:
		return truncate(c)
	case []any:
		for _, e := range c {
			if em, ok := e.(map[string]any); ok {
				if t, _ := em["text"].(string); t != "" {
					return truncate(t)
				}
			}
		}
	}
	return ""
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 60 {
		r := []rune(s)
		s = string(r[:60]) + "…"
	}
	return s
}
