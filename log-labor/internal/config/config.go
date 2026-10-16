// Package config — log-labor configuration: ~/.config/log-labor/config.json
// (0600). Holds the sheet webhook key (a write credential — never log it),
// the user's corp userid, and the sheet profile (field-id map + statuses).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"git.sh.nint.com/ying.yuxiang/AutoWecom-plugin/log-labor/internal/sources"
	"os"
	"path/filepath"
	"runtime"
)

// Version is stamped at build time (-ldflags "-X …/config.Version=v…").
var Version = "dev"

// Field — one smartsheet column in the profile.
type Field struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"` // text|number|date_time|single_select|user|two_way_link_records
}

// Profile — everything needed to address ONE sheet's fields.
type Profile struct {
	SheetName  string           `json:"sheet_name"`
	Statuses   []string         `json:"statuses"`
	Fields     map[string]Field `json:"fields"`      // keyed by role: person/date/status/content/link/hours/due/proposer/blocker
	FieldOrder []string         `json:"field_order"` // display + preview order
}

// Defaults — write-path defaults for add, set via
// `log-labor config set default_status|default_proposer|due_mirror …`.
// Update NEVER applies them: unset optionals stay untouched there.
type Defaults struct {
	Status    string `json:"status,omitempty"`     // default 文本 (must exist in profile statuses — validated at write)
	Proposer  string `json:"proposer,omitempty"`   // default 提出人
	DueMirror *bool  `json:"due_mirror,omitempty"` // 预计完成时间 mirrors 日期 on add; nil = true
}

// MirrorDue — tri-state booleans in JSON default-false would lie about
// "on by default", so the on-disk flag is a pointer and missing = on.
func (d Defaults) MirrorDue() bool { return d.DueMirror == nil || *d.DueMirror }

// Daily — the global default work window for `daily collect` (+08),
// plus the exclusion rules: regexes matched against each session's
// project dir (full path AND basename) and title; any match drops the
// session from every digest — personal work never reaches the sheet.
// Empty strings mean the built-in default 09:00–22:00; a Source may
// override either edge per harness (night shifts etc.).
type Daily struct {
	WindowStart string   `json:"window_start,omitempty"` // HH:MM, default 09:00
	WindowEnd   string   `json:"window_end,omitempty"`   // HH:MM, default 22:00
	Excludes    []string `json:"excludes,omitempty"`     // regexes; see `daily exclude`
}

// Config — the on-disk document.
type Config struct {
	Endpoint  string           `json:"endpoint"`             // webhook base URL (key appended per call)
	Key       string           `json:"key"`                  // sheet webhook key — WRITE CREDENTIAL
	Person    string           `json:"person"`               // caller's corp userid (e.g. zhang.san)
	Mode      string           `json:"mode"`                 // direct (default) | core (broker endpoint; vNext)
	BrokerURL string           `json:"broker_url,omitempty"` // core mode only
	Profile   Profile          `json:"profile"`
	Defaults  Defaults         `json:"defaults,omitempty"` // add-path defaults (see Defaults)
	Sources   []sources.Source `json:"sources,omitempty"`  // session stores for daily collect
	Daily     Daily            `json:"daily,omitempty"`    // default work window for daily collect
}

// Dir — config directory (~/.config/log-labor, honor $XDG_CONFIG_HOME;
// %AppData%\log-labor on windows, honoring os.UserConfigDir).
func Dir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "log-labor"), nil
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "log-labor"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "log-labor"), nil
}

// Path — the config file.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// DefaultProfile — the team labor sheet (任务工时详细). Field ids are stable
// (from the webhook console 示例数据); the KEY is the secret, not the map.
func DefaultProfile() Profile {
	f := func(id, title, typ string) Field { return Field{ID: id, Title: title, Type: typ} }
	return Profile{
		SheetName: "任务工时详细",
		Statuses:  []string{"状态", "已完成", "调休", "进行中", "待排期", "已合并", "已关闭"},
		Fields: map[string]Field{
			"person":   f("fQLSz5", "人员", "user"),
			"date":     f("f3Wcc3", "日期", "date_time"),
			"status":   f("fFtUk3", "文本", "single_select"),
			"content":  f("fuVenY", "需求内容", "text"),
			"link":     f("fzGuQV", "关联", "two_way_link_records"),
			"hours":    f("fjKRQJ", "预计花费工时", "number"),
			"due":      f("fEF4fy", "预计完成时间", "date_time"),
			"proposer": f("fEyXeR", "提出人", "text"),
			"blocker":  f("f1QYcV", "卡点", "text"),
		},
		FieldOrder: []string{"date", "status", "person", "content", "link", "hours", "due", "proposer", "blocker"},
	}
}

// Default — a config with the default profile; key/person empty until init.
func Default() *Config {
	return &Config{
		Endpoint: "https://qyapi.weixin.qq.com/cgi-bin/wedoc/smartsheet/webhook",
		Mode:     "direct",
		Profile:  DefaultProfile(),
	}
}

// Load — read + parse; os.ErrNotExist wraps when absent (callers decide
// whether that's fatal).
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if c.Profile.Fields == nil {
		c.Profile = DefaultProfile()
	}
	if c.Endpoint == "" {
		c.Endpoint = Default().Endpoint
	}
	if c.Mode == "" {
		c.Mode = "direct"
	}
	return c, nil
}

// MustLoad — Load, or exit-2 style error mentioning `log-labor init`.
func MustLoad() (*Config, error) {
	c, err := Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no config yet — run `log-labor init` first")
	}
	return c, err
}

// Save — write 0600 (the key lives here).
func (c *Config) Save() error {
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p, err := Path()
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}
