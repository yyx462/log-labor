// log-labor — 报工 CLI for the team labor smartsheet (WeCom 智能表格
// webhook). Pure stdlib; the webhook contract is live-proved — see
// internal/webhook and skill/SKILL.md.tmpl.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/config"
	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/skillinstall"
)

var version = config.Version

// autoSkillRefresh — one-step upgrades: `npm i -g` (or the curl
// installer) is the only step a user needs; the first config-using
// command after a CLI update re-renders any agent skills whose stamp
// predates this build. Silent unless something actually refreshed; dev
// builds and config-less machines do nothing.
func autoSkillRefresh() {
	c, err := config.MustLoad()
	if err != nil {
		return
	}
	names, err := skillinstall.RefreshStale(c)
	if err != nil || len(names) == 0 {
		return
	}
	fmt.Printf("skills refreshed → %s (%s)\n", config.Version, strings.Join(names, ", "))
}

// staleNotice — notify-only update nudge, the industry thumb for
// global CLIs (npm, ng, vercel print; they never self-mutate): at most
// once a day, ≤3s, ask the npm registry what `latest` is; if it is
// strictly newer than this build, print ONE stderr line pointing at
// the explicit `log-labor upgrade` verb. Opt out with
// LOG_LABOR_NO_UPDATE_CHECK=1; dev builds skip (no comparable version).
func staleNotice() {
	if os.Getenv("LOG_LABOR_NO_UPDATE_CHECK") != "" || config.Version == "dev" {
		return
	}
	dir, err := config.Dir()
	if err != nil {
		return
	}
	stamp := filepath.Join(dir, "lastcheck")
	if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
		return
	}
	_ = os.WriteFile(stamp, []byte(time.Now().Format(time.RFC3339)), 0o600)
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://registry.npmjs.org/@yyx462/log-labor/latest")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var doc struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&doc) != nil || !newerVersion(doc.Version, config.Version) {
		return
	}
	fmt.Fprintf(os.Stderr, "log-labor: v%s available (this: %s) — `log-labor upgrade`\n", doc.Version, config.Version)
}

// newerVersion — a strictly newer than b, semver core only
// (major.minor.patch); anything unparsable → false (never nag on junk).
func newerVersion(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// versionParts — "v1.2.3-rc1" → [1 2 3]; nil when not 3 clean ints.
func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

// preflight — same hook everywhere: refresh stale agent skills, then
// (cheaply, daily) nudge about newer releases.
func preflight() {
	autoSkillRefresh()
	staleNotice()
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	if err := dispatch(args); err != nil {
		fmt.Fprintf(os.Stderr, "log-labor: %v\n", err)
		if ec, ok := err.(exitError); ok {
			os.Exit(ec.code)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`log-labor ` + version + ` — 报工 into your WeCom 智能表格 labor sheet

  log-labor init                                  configure key + corp id (+ sample row)
  log-labor add -c "内容" -h 2.5 [--date --status --person
                    --proposer --blocker --due --link] [--yes] [--dry-run]
  log-labor update --record-id R [same field flags]
  log-labor doctor [--write-sample]               config + key health, no writes
  log-labor config get|set|list|path [key] [value]   # + default_status|default_proposer|due_mirror
  log-labor profile import [file]                 sheet fields/statuses from 接收外部数据 → 示例数据
  log-labor daily collect [--date D | --from D1 --to D2] [--window HH:MM-HH:MM]
                                  digest agent sessions per day, clipped to the work window
  log-labor daily sources list|add|remove|test    registry of session stores for collect
  log-labor daily set-window --start 09:00 --end 22:00   # global work window default
  log-labor daily exclude list|add|remove        regexes dropped from every digest (personal work)
  log-labor daily fit "内容"=2.5 …|--total N      fit drafts to sum exactly N (default 8)
  log-labor upgrade                               # self-update: npm i -g … / install.sh one-liner
  log-labor version

Exit codes: 0 ok · 1 WeCom error · 2 usage/config.
Key + corp id live in ~/.config/log-labor/config.json (0600).
`)
}

func dispatch(args []string) error {
	switch args[0] {
	case "init":
		return cmdInit(args[1:])
	case "add":
		preflight()
		return cmdAdd(args[1:])
	case "update":
		preflight()
		return cmdUpdate(args[1:])
	case "doctor":
		preflight()
		return cmdDoctor(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "profile":
		return cmdProfile(args[1:])
	case "skill":
		return cmdSkill(args[1:])
	case "daily":
		preflight()
		return cmdDaily(args[1:])
	case "upgrade":
		return cmdUpgrade(args[1:])
	case "version", "--version", "-v":
		fmt.Println("log-labor " + version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return exitError{code: 2, msg: fmt.Sprintf("unknown command %q", args[0])}
	}
}
