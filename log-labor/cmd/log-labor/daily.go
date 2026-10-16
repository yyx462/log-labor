package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/config"
	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/record"
	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/sources"
)

// cmdDaily — `daily collect|sources|fit|set-window`: the end-of-day
// ritual. Collect digests the requested days' agent sessions across
// every registered or built-in store, clipped to each harness's work
// window (default 09:00–22:00 +08); fit drafts them into records
// summing to exactly the day's total — 8h default, any user-set cap
// via --total, several days in one shot via per-row "date".
func cmdDaily(args []string) error {
	if len(args) == 0 {
		return exitError{code: 2, msg: "daily needs a verb: collect|sources|fit|set-window"}
	}
	switch args[0] {
	case "collect":
		return dailyCollect(args[1:])
	case "sources":
		return dailySources(args[1:])
	case "fit":
		return dailyFit(args[1:])
	case "set-window":
		return dailySetWindow(args[1:])
	case "exclude":
		return dailyExclude(args[1:])
	default:
		return exitError{code: 2, msg: fmt.Sprintf("unknown daily verb %q — want collect|sources|fit|set-window|exclude", args[0])}
	}
}

// cnZone — Asia/Shanghai as a fixed +8 zone (sheet's timezone).
func cnZone() *time.Location { return time.FixedZone("CST", 8*3600) }

// resolveDay — --date (default today, +08) → start-of-day ms epoch.
func resolveDay(day string) (int64, string, error) {
	if day == "" {
		day = time.Now().In(cnZone()).Format("2006-01-02")
	}
	t0, err := time.ParseInLocation("2006-01-02", day, cnZone())
	if err != nil {
		return 0, "", fmt.Errorf("bad --date %q: %w", day, err)
	}
	return t0.UnixMilli(), day, nil
}

// resolveRange — the requested days as an ordered list: --from/--to
// (inclusive) or --date (single). fromMs = first day 00:00 +08; toMs
// padded one extra day past the end so a cross-midnight window on the
// last day still sees its early-morning tail (rows for padded days
// are dropped by CollectAll).
func resolveRange(date, from, to string) (fromMs, toMs int64, days []string, err error) {
	if from == "" && to == "" {
		f, day, err := resolveDay(date)
		return f, f + 86_400_000, []string{day}, err
	}
	if from == "" || to == "" {
		return 0, 0, nil, exitError{code: 2, msg: "--from and --to go together (or use --date for one day)"}
	}
	f0, _, err := resolveDay(from)
	if err != nil {
		return 0, 0, nil, err
	}
	t0, _, err := resolveDay(to)
	if err != nil {
		return 0, 0, nil, err
	}
	if t0 < f0 {
		return 0, 0, nil, exitError{code: 2, msg: "--from is after --to"}
	}
	for d := f0; d <= t0; d += 86_400_000 {
		days = append(days, time.UnixMilli(d).In(cnZone()).Format("2006-01-02"))
	}
	return f0, t0 + 2*86_400_000, days, nil
}

// effectiveWindow — flag > per-source override > config global >
// built-in default (09:00–22:00 +08). A single overridden edge falls
// back to the global/default edge for the other side.
func effectiveWindow(flagVal string, s sources.Source, c *config.Config) (sources.Window, error) {
	if flagVal != "" {
		return sources.ParseWindow(flagVal)
	}
	var gStart, gEnd string
	if c != nil {
		gStart, gEnd = c.Daily.WindowStart, c.Daily.WindowEnd
	}
	w := sources.Window{
		Start: s.WindowStart,
		End:   s.WindowEnd,
	}
	if w.Start == "" {
		w.Start = gStart
	}
	if w.End == "" {
		w.End = gEnd
	}
	if w.Start == "" {
		w.Start = sources.DefaultWindow().Start
	}
	if w.End == "" {
		w.End = sources.DefaultWindow().End
	}
	return w, w.Valid()
}

// dailySourcesFor — registered sources + built-ins not shadowed by a
// registration with the same path.
func dailySourcesFor(c *config.Config) []sources.Source {
	out := sources.BuiltIns()
	var keep []sources.Source
	for _, b := range out {
		shadowed := false
		for _, r := range c.Sources {
			if r.Path == b.Path {
				shadowed = true
				break
			}
		}
		if !shadowed {
			keep = append(keep, b)
		}
	}
	return append(keep, c.Sources...)
}

func dailyCollect(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	fromMs, toMs, days, err := resolveRange(f.val("date"), f.val("from"), f.val("to"))
	if err != nil {
		return exitError{code: 2, msg: err.Error()}
	}
	var c *config.Config
	var all []sources.Source
	if db := f.val("db"); db != "" { // legacy override: opencode only
		all = append(all, sources.Source{Name: "opencode", Format: "opencode-sqlite", Path: db})
	} else {
		c, err = config.Load()
		if err == nil {
			all = dailySourcesFor(c)
		} else {
			all = sources.BuiltIns()
		}
	}
	if len(all) == 0 {
		return exitError{code: 2, msg: "no session stores found — register one: `log-labor daily sources add`"}
	}
	excludes, err := compileConfigExcludes(c)
	if err != nil {
		return exitError{code: 2, msg: err.Error()}
	}
	ss, warns := sources.CollectAll(all, func(s sources.Source) sources.Window {
		w, err := effectiveWindow(f.val("window"), s, c)
		if err != nil {
			w = sources.DefaultWindow()
		}
		return w
	}, fromMs, toMs, days, excludes)
	perDay := map[string][]sources.Session{}
	projects := map[string]bool{}
	for _, r := range ss {
		perDay[r.Day] = append(perDay[r.Day], r)
		projects[r.Dir] = true
	}
	for _, d := range days {
		fmt.Printf("== %s (窗口 %s) ==\n", d, effectiveWindowLabel(f.val("window"), c))
		rows := perDay[d]
		if len(rows) == 0 {
			fmt.Println("  (无会话)")
			continue
		}
		for _, r := range rows {
			start := time.UnixMilli(r.Start).In(cnZone()).Format("15:04")
			end := time.UnixMilli(r.End).In(cnZone()).Format("15:04")
			dur := (r.End - r.Start) / 60_000 // ms → minutes
			title := r.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Printf("  [%s] %s–%s  %4dm  %3d msgs  %s  %s\n",
				r.Source, start, end, dur, r.Msgs, r.Dir, title)
		}
	}
	for _, w := range warns {
		fmt.Println("  ⚠ " + w)
	}
	names := make([]string, 0, len(projects))
	for p := range projects {
		names = append(names, filepath.Base(p))
	}
	sort.Strings(names)
	fmt.Printf("%d sessions · %d sources · projects: %s\n", len(ss), len(all), strings.Join(names, ", "))
	fmt.Println("draft records per day, then one-shot multi-day fit:")
	fmt.Printf("  echo '[{\"date\":\"%s\",\"content\":\"…\",\"hours\":2.5},…]' | log-labor daily fit\n", days[0])
	return nil
}

// effectiveWindowLabel — human-readable window for the section header
// (the --window override applies to every source; per-source overrides
// are visible in `daily sources list`).
func effectiveWindowLabel(flagVal string, c *config.Config) string {
	if flagVal != "" {
		return flagVal
	}
	if c != nil && c.Daily.WindowStart != "" && c.Daily.WindowEnd != "" {
		return c.Daily.WindowStart + "–" + c.Daily.WindowEnd + " +08"
	}
	return "09:00–22:00 +08 默认"
}

// dailySources — list|add|remove|test: the agent-driven registry that
// teaches log-labor about this machine's session stores. `add` takes
// optional --window-start/--window-end (HH:MM +08): THIS harness's own
// work window, overriding the global daily window — night shifts etc.
func dailySources(args []string) error {
	if len(args) == 0 {
		return exitError{code: 2, msg: "sources needs a verb: list|add|remove|test"}
	}
	c, err := config.MustLoad()
	if err != nil {
		return err
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		for _, b := range dailySourcesFor(c) {
			w, err := effectiveWindow("", b, c)
			label := "(默认)"
			if err == nil && (b.WindowStart != "" || b.WindowEnd != "") {
				label = w.Start + "–" + w.End + " 覆盖"
			}
			fmt.Printf("  %-12s %-15s %-18s %s\n", b.Name, b.Format, label, b.Path)
		}
		if len(c.Sources) == 0 {
			fmt.Println("(registered: none — built-ins above; add with `daily sources add`)")
		}
		if c.Daily.WindowStart != "" || c.Daily.WindowEnd != "" {
			w, err := effectiveWindow("", sources.Source{}, c)
			if err == nil {
				fmt.Printf("global window: %s–%s +08 (set via `daily set-window`)\n", w.Start, w.End)
			}
		}
		return nil
	case "add":
		f, err := parseFlags(rest)
		if err != nil {
			return err
		}
		s := sources.Source{
			Name:           f.val("name"),
			Format:         f.val("format"),
			Path:           f.val("path"),
			WindowStart:    f.val("window-start"),
			WindowEnd:      f.val("window-end"),
			TimestampField: f.val("timestamp-field"),
			CwdField:       f.val("cwd-field"),
			SessionField:   f.val("session-field"),
			TitleField:     f.val("title-field"),
		}
		if s.Name == "" || s.Format == "" || s.Path == "" {
			return exitError{code: 2, msg: "add needs --name, --format, --path"}
		}
		ok := false
		for _, k := range sources.KnownFormats {
			if s.Format == k {
				ok = true
			}
		}
		if !ok {
			return exitError{code: 2, msg: fmt.Sprintf("unknown format %q — want %s", s.Format, strings.Join(sources.KnownFormats, "|"))}
		}
		if s.WindowStart != "" || s.WindowEnd != "" {
			if _, err := effectiveWindow("", s, nil); err != nil {
				return exitError{code: 2, msg: err.Error() + " (--window-start/--window-end take HH:MM)"}
			}
		}
		if p, err := expandHome(s.Path); err == nil {
			s.Path = p
		}
		if _, err := os.Stat(s.Path); err != nil {
			return exitError{code: 2, msg: fmt.Sprintf("no store at %s", s.Path)}
		}
		replaced := false
		for i, old := range c.Sources {
			if old.Name == s.Name {
				c.Sources[i] = s
				replaced = true
			}
		}
		if !replaced {
			c.Sources = append(c.Sources, s)
		}
		if err := c.Save(); err != nil {
			return err
		}
		fmt.Printf("saved (%s: %s @ %s", s.Name, s.Format, s.Path)
		if s.WindowStart != "" || s.WindowEnd != "" {
			w, err := effectiveWindow("", s, c)
			if err == nil {
				fmt.Printf(", window %s–%s", w.Start, w.End)
			}
		}
		fmt.Println(").")
		return nil
	case "remove":
		f, err := parseFlags(rest)
		if err != nil {
			return err
		}
		name := f.val("name")
		kept := c.Sources[:0]
		for _, s := range c.Sources {
			if s.Name != name {
				kept = append(kept, s)
			}
		}
		if len(kept) == len(c.Sources) {
			return exitError{code: 2, msg: fmt.Sprintf("no registered source named %q", name)}
		}
		c.Sources = kept
		return c.Save()
	case "test":
		f, err := parseFlags(rest)
		if err != nil {
			return err
		}
		name := f.val("name")
		srcs := dailySourcesFor(c)
		var target *sources.Source
		for i := range srcs {
			if srcs[i].Name == name {
				target = &srcs[i]
			}
		}
		if target == nil {
			return exitError{code: 2, msg: fmt.Sprintf("no source named %q — see `daily sources list`", name)}
		}
		fromMs, toMs, days, err := resolveRange(f.val("date"), f.val("from"), f.val("to"))
		if err != nil {
			return exitError{code: 2, msg: err.Error()}
		}
		win, err := effectiveWindow(f.val("window"), *target, c)
		if err != nil {
			return exitError{code: 2, msg: err.Error()}
		}
		excludes, xcErr := compileConfigExcludes(c)
		if xcErr != nil {
			return exitError{code: 2, msg: xcErr.Error()}
		}
		ss, warns := sources.CollectAll([]sources.Source{*target},
			func(sources.Source) sources.Window { return win }, fromMs, toMs, days, excludes)
		for _, w := range warns {
			fmt.Println("  ⚠ " + w)
		}
		fmt.Printf("[%s] %s: %d sessions · window %s–%s +08 · %s\n",
			target.Name, target.Format, len(ss), win.Start, win.End, strings.Join(days, ","))
		for _, r := range ss {
			fmt.Printf("  %s %s–%s  %3d msgs  %s  %s\n", r.Day,
				time.UnixMilli(r.Start).In(cnZone()).Format("15:04"),
				time.UnixMilli(r.End).In(cnZone()).Format("15:04"), r.Msgs, r.Dir, r.Title)
		}
		return nil
	default:
		return exitError{code: 2, msg: fmt.Sprintf("unknown sources verb %q — want list|add|remove|test", verb)}
	}
}

// compileConfigExcludes — the config's exclude regexes, compiled.
// nil config → no excludes. A bad stored pattern fails loud: the whole
// point of the filter is that personal work never leaks onto the sheet.
func compileConfigExcludes(c *config.Config) ([]*regexp.Regexp, error) {
	if c == nil {
		return nil, nil
	}
	return sources.CompileExcludes(c.Daily.Excludes)
}

// dailyExclude — list|add|remove: regex rules dropping sessions from
// EVERY digest (matched against project dir basename, full dir, and
// title). The point: personal work (求职 etc.) never reaches the
// sheet. One bad pattern poisons collect — `remove` accepts the raw
// pattern to delete it.
func dailyExclude(args []string) error {
	if len(args) == 0 {
		return exitError{code: 2, msg: "exclude needs a verb: list|add|remove"}
	}
	c, err := config.MustLoad()
	if err != nil {
		return err
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list":
		if len(c.Daily.Excludes) == 0 {
			fmt.Println("(no exclude rules — every collected session reaches the sheet; add with `daily exclude add`)")
			return nil
		}
		for _, p := range c.Daily.Excludes {
			fmt.Println("  " + p)
		}
		return nil
	case "add":
		f, err := parseFlags(rest)
		if err != nil {
			return err
		}
		pats := append(f.args, f.val("re"))
		pats = append(pats, f.val("pattern"))
		pats = nonEmpty(pats)
		if len(pats) == 0 {
			return exitError{code: 2, msg: "add needs at least one regex (bare arg or --re)"}
		}
		for _, p := range pats {
			if _, err := regexp.Compile(p); err != nil {
				return exitError{code: 2, msg: fmt.Sprintf("bad regex %q: %v", p, err)}
			}
		}
		added := 0
		for _, p := range pats {
			dup := false
			for _, old := range c.Daily.Excludes {
				if old == p {
					dup = true
					break
				}
			}
			if !dup {
				c.Daily.Excludes = append(c.Daily.Excludes, p)
				added++
			}
		}
		if err := c.Save(); err != nil {
			return err
		}
		fmt.Printf("saved %d rule(s) — matched sessions (project basename, dir, or title) are dropped from every digest; list: `daily exclude list`\n", added)
		return nil
	case "remove":
		f, err := parseFlags(rest)
		if err != nil {
			return err
		}
		pats := append(f.args, f.val("re"))
		kept := c.Daily.Excludes[:0]
		removed := 0
		for _, old := range c.Daily.Excludes {
			drop := false
			for _, p := range nonEmpty(pats) {
				if old == p {
					drop = true
					break
				}
			}
			if drop {
				removed++
			} else {
				kept = append(kept, old)
			}
		}
		if removed == 0 {
			return exitError{code: 2, msg: "no such rule — see `daily exclude list`"}
		}
		c.Daily.Excludes = kept
		return c.Save()
	default:
		return exitError{code: 2, msg: fmt.Sprintf("unknown exclude verb %q — want list|add|remove", verb)}
	}
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// dailySetWindow — the global default work window for `daily collect`
// (per-harness overrides live on the Source, single-shot via collect
// --window). Both edges required; end ≤ start = crosses midnight.
func dailySetWindow(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	start, end := f.val("start"), f.val("end")
	if start == "" || end == "" {
		return exitError{code: 2, msg: "set-window needs --start HH:MM and --end HH:MM (e.g. --start 09:00 --end 22:00)"}
	}
	if err := (sources.Window{Start: start, End: end}).Valid(); err != nil {
		return exitError{code: 2, msg: err.Error()}
	}
	c, err := config.MustLoad()
	if err != nil {
		return err
	}
	c.Daily.WindowStart, c.Daily.WindowEnd = start, end
	if err := c.Save(); err != nil {
		return err
	}
	kind := "默认"
	if end <= start {
		kind = "跨午夜"
	}
	fmt.Printf("global work window saved: %s–%s +08 (%s)。单次覆盖用 collect --window；按 harness 覆盖用 sources add --window-start/--window-end。\n", start, end, kind)
	return nil
}

func expandHome(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// parseTotal — the user-defined day total: "10", "10h", "10小时";
// empty → 8 (one working day). Must be > 0.
func parseTotal(v string) (float64, error) {
	if v == "" {
		return 8, nil
	}
	norm, err := record.NormalizeHours(v)
	if err != nil {
		return 0, fmt.Errorf("bad --total %q (want hours, e.g. 10 / 10h / 10小时)", v)
	}
	f, _ := strconv.ParseFloat(norm, 64)
	if f <= 0 {
		return 0, fmt.Errorf("--total must be > 0, got %q", v)
	}
	return f, nil
}

// fitStatus — the 文本 (状态) column shown in the fitted table and
// threaded into the printed add lines. Empty --status → 进行中, the
// same default `add` itself applies, so the column never lies.
func fitStatus(f *flags) (shown, flag string) {
	shown = f.val("status")
	if shown == "" {
		shown = "进行中"
		return shown, ""
	}
	return shown, " --status " + shown
}

// groupFitRowsByDate — multi-day fit: rows with a "date" field are
// grouped per day (each fitted to the total separately). Dated and
// undated rows never mix; dates must parse and are returned sorted.
func groupFitRowsByDate(rows []record.FitRow) (dated map[string][]record.FitRow, dates []string, undated []record.FitRow, err error) {
	dated = map[string][]record.FitRow{}
	anyDated, anyUndated := false, false
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Date != "" {
			anyDated = true
			if _, e := time.ParseInLocation("2006-01-02", r.Date, cnZone()); e != nil {
				return nil, nil, nil, fmt.Errorf("row %q has bad date %q (want YYYY-MM-DD)", r.Content, r.Date)
			}
		} else {
			anyUndated = true
		}
		if r.Date != "" && !seen[r.Date] {
			seen[r.Date] = true
			dates = append(dates, r.Date)
		}
		dated[r.Date] = append(dated[r.Date], r)
		if r.Date == "" {
			undated = append(undated, r)
		}
	}
	if anyDated && anyUndated {
		return nil, nil, nil, exitError{code: 2, msg: `rows mix "date" and no-"date" — give every row a date (or none)`}
	}
	sort.Strings(dates)
	return dated, dates, undated, nil
}

// dailyFit — input: drafted records as `daily fit "内容"=2.5 "内容"=1`
// bare args, or a JSON array [{"date":…, "content":…, "hours":…}] on
// stdin ("date" on EVERY row = multi-day one-shot; each day is fitted
// to --total separately). Output: the fitted table(s) (Σ = --total,
// default 8, per day; one 文本 column so the user confirms status
// BEFORE writing) + ready-to-run `log-labor add` lines. Never writes
// to the sheet by itself.
func dailyFit(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	total, err := parseTotal(f.val("total"))
	if err != nil {
		return exitError{code: 2, msg: err.Error()}
	}
	var rows []record.FitRow
	if len(f.args) > 0 {
		for _, a := range f.args {
			i := strings.LastIndex(a, "=")
			if i <= 0 {
				return exitError{code: 2, msg: fmt.Sprintf("arg %q is not 内容=hours", a)}
			}
			var h float64
			if _, err := fmt.Sscanf(a[i+1:], "%f", &h); err != nil {
				return exitError{code: 2, msg: fmt.Sprintf("arg %q has bad hours", a)}
			}
			rows = append(rows, record.FitRow{Content: strings.TrimSpace(a[:i]), Raw: h})
		}
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			return exitError{code: 2, msg: "give records as args (内容=hours) or a JSON array on stdin"}
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return exitError{code: 2, msg: fmt.Sprintf("stdin is not a records JSON array: %v", err)}
		}
	}
	shown, statusFlag := fitStatus(f)
	day := f.val("date")
	dated, dates, undated, err := groupFitRowsByDate(rows)
	if err != nil {
		return exitError{code: 2, msg: err.Error()}
	}
	printFit := func(label string, group []record.FitRow, dateFlag string) error {
		fitted, err := record.FitToDay(group, total)
		if err != nil {
			return err
		}
		fmt.Printf("== 报工草稿 %s (fit to %.1fh) ==\n", label, total)
		sum := 0.0
		for i, r := range fitted {
			sum += r.Fitted
			fmt.Printf("  %d. %-4.1fh → %-4.1fh  [%s]  %s\n", i+1, r.Raw, r.Fitted, shown, r.Content)
		}
		fmt.Printf("Σ = %.1fh\n", sum)
		fmt.Println("-- insert with --yes after the user confirms:")
		for _, r := range fitted {
			fmt.Printf("log-labor add -c %q -h %g%s%s --yes\n", r.Content, r.Fitted, dateFlag, statusFlag)
		}
		return nil
	}
	if len(dates) > 0 {
		for _, d := range dates {
			if err := printFit(d, dated[d], " --date "+d); err != nil {
				return exitError{code: 2, msg: fmt.Sprintf("%s: %v", d, err)}
			}
		}
		return nil
	}
	dateFlag := ""
	if day != "" {
		dateFlag = " --date " + day
	}
	if err := printFit("", undated, dateFlag); err != nil {
		return exitError{code: 2, msg: err.Error()}
	}
	return nil
}
