---
name: log-labor
description: >-
  报工 / labor logging on the 任务工时详细 smartsheet through the log-labor
  CLI (WeCom 智能表格 webhook). Use when the user asks to 记工时/报工/log
  labor/add a row to 任务工时详细, or asks to set this CLI up. Live-proved
  webhook facts inside; the CLI enforces them — never hand-craft webhook
  requests when the CLI is installed.
---

# log-labor — 报工 via the 任务工时详细 smartsheet webhook (dev)

## Commands

```bash
log-labor add -c "完成登录页联调" -h 3                  # today, 默认状态 进行中 (或表内首个状态)
log-labor add -c "修复导出崩溃" -h 2 --status 已完成 --due 2026-09-20 --blocker "无"
log-labor update --record-id R --status 已完成          # closure = UPDATE, never a new row
log-labor doctor                                       # config + key health, no writes
log-labor doctor --write-sample                        # one marked sample row (asks nothing)
log-labor config set default_status 已完成              # every add starts 已完成
log-labor config set due_mirror off                    # 预计完成时间 mirrors 日期 (default on)
log-labor config set sheet "项目工时表"                 # retarget: same key, new sheet TITLE
log-labor upgrade                                      # self-update (npm i -g / install.sh)
log-labor skill install                                # (re)install this skill for detected agents
```

Flags: `-c/--content` · `-h/--hours` (2.5 / 1h / 2小时) · `--date` (YYYY-MM-DD,
default today +08) · `--status` (状态, 已完成, 调休, 进行中, 待排期, 已合并, 已关闭) · `--person` (corp id)
· `--proposer` · `--blocker` · `--due` · `--link R1[,R2]` · `--yes`
(skip preview confirm) · `--dry-run`.

## The agent flow (报工 on demand)

1. Derive facts from session evidence: 需求内容 = ONE concise sentence on
   the actual work; hours from the user or a stated estimate — if hours
   are ambiguous, ASK, don't guess.
2. FIRST PASS = the real command, BARE: `log-labor add -c … -h …` with
   data flags ONLY. Never `--dry-run`, never `--yes`, never `--person`
   on this pass. In a non-interactive shell the confirm can't read
   stdin: the CLI exits 2 with "re-run with --yes".
3. ON EXIT 2: STOP. Reply with the preview table (需求内容 / 工时 /
   状态 — status visible) ending in `请确认是否提交`, and run nothing
   else. `--yes` is ONLY for after an explicit user OK — never as a
   self-confirm. `--dry-run` is for humans previewing; agents must not
   substitute it for this gate.
4. Check exit 0 and report the `record_id` back with the summary.
5. Rework/closure later = `update --record-id`, never a second row.
6. NEVER guess `--person` from git config, env, or repo hints. If the
   configured person is rejected (40031 invalid userid), ask the user
   for the correct corp id. Config changes go through
   `log-labor config set`, never hand-editing `config.json`.
7. `skill install` re-renders this skill from the CLI's embedded
   template. It skips copies it cannot stamp (hand-edited ones) unless
   `--force`, and `--dry-run` previews targets. Skill fixes belong in
   the repo template (log-labor/internal/skill/SKILL.md.tmpl), never
   in installed copies.

## Defaults (add-path speedups)

`add` fills these WITHOUT flags (update never applies defaults —
unset optionals stay untouched there):

- 预计完成时间 mirrors the row's 日期 (on by default; disable with
  `config set due_mirror off`); `--due` always wins.
- `config set default_status 已完成` — the usual 文本, e.g. for
  teams that only log finished work.
- `config set default_proposer <corp-id>` — 提出人 when it is
  always the same person.

Agent speed rules (live-proved during the 9/11–9/13 backfills):

- ONE confirm per batch — present the whole fitted table (日期/文本/
  需求内容/工时) a single time; never re-confirm row by row.
- Fit once with `--date` + `--status` instead of editing rows after
  writing; later corrections while record_ids are known = `update
  --record-id`, never a second add.
- Backfilling several days in one sitting: `daily collect --from D1
  --to D2` prints one section per day; sessions alive on several days
  produce one row PER DAY, so double-counting cannot happen — draft
  per day and fit all days in ONE `daily fit` call (every row carries
  its `"date"`). The script owns all time math; never hand-compute
  times, never re-attribute a session across days yourself.
- Missing record_ids (older rows) = hand-edit in the sheet UI; the
  local write-journal that would fix this is ticketed in `docs/tickets/`.
- Upgrades are EXPLICIT: `log-labor upgrade` (npm-managed installs →
  `npm i -g @yyx462/log-labor@latest`, else the install.sh one-liner);
  skills re-render on the next command after. A once-a-day stderr line
  may announce a newer release (notify-only; LOG_LABOR_NO_UPDATE_CHECK=1
  silences) — relay it to the user, don't act on it unasked.

## Hard facts (live-proved 2026-09-11, enforced by the CLI)

- Sheet: **任务工时详细**; keys are WRITE credentials — never print the key,
  never commit it (`~/.config/log-labor/config.json`, 0600).
- The profile (field ids + statuses) comes from the sheet's 接收外部数据
  示例数据 (智能表格 → 右上角文档操作 → 接收外部数据) via
  `log-labor profile import` — the console sample is ground truth; never
  hand-edit field ids in config.json. Roles the sheet doesn't map simply
  disable their flags. NOTE: `profile import` is for loading a DIFFERENT
  sheet's field-id profile — it is NOT how you change the sheet TITLE
  (that's `config set sheet`, see the quick-start block above).
- 人员 takes the CORP userid (`ying.yuxiang` pinyin form). `woa-…`
  bot-namespace ids and numeric ids are rejected **atomically** (40031) —
  a bad user value kills the whole request, nothing is written.
- ONE op per request (mixing add+update → 40058). Rate caps: 3000 rows/min
  per sheet, 10000/min per doc.
- The webhook is write-only: no read, no delete. Deleting the 可删除 test
  rows happens in the sheet UI by the human.
- Value shapes: text = plain string · number = bare · date_time =
  ms-epoch STRING (00:00 +08) · single_select = option text · user =
  corp id · link = record ids.

## End-of-day batch (Σ = 8h default, any --total)

When the user asks to 总结今天 / 报工总结 / close out the day — or runs
parallel agent sessions all day and reports at once — or asks "help me
log today to 10 hours" (帮我把今天报成 10 小时):

1. `log-labor daily collect [--date D | --from D1 --to D2]` — per-day
   digest of agent sessions: project, in-window span, message count,
   title. ALL time filtering is the script's job: every day is clipped
   to its work window (default 09:00–22:00 +08; `daily set-window`
   changes the default, `--window HH:MM-HH:MM` overrides once, and a
   source's own `--window-start/--window-end` at `sources add` wins
   for that harness). Sessions running ≥72h are EXCLUDED with a ⚠
   line — tell the user to split such sessions instead of guessing.
   Never compute times yourself and never dig raw timestamps out of
   session stores; trust the printed rows.
2. Draft 2–6 records grouping the digest into real work themes with
   HONEST raw hours; ask the user when the digest is ambiguous. Never
   invent work that is not in the digest (or that the user rejects).
3. Fit them to the day's total — pipe a JSON array to
   `log-labor daily fit --date <day>`:
   `echo '[{"content":"…","hours":2.5},…]' | log-labor daily fit --date 2026-09-11`
   (or bare args: `log-labor daily fit "内容"=2.5 …`). Multi-day
   backfill = ONE call, a "date" on every row:
   `echo '[{"date":"2026-09-11","content":"…","hours":2.5},…]' | log-labor daily fit`
   — each day is fitted to the total separately and every printed add
   line carries the right `--date`. The fit scales proportions, snaps
   to 0.5h, and lands Σ on EXACTLY the total; it prints the fitted
   table (with its 文本 column) plus ready `log-labor add` lines.
   Total = 8.0h unless the user set the day's cap: `--total 10`
   (accepts 10 / 10h / 10小时) — "log today to 10 hours" is collect →
   draft → `daily fit --total 10` → add, nothing else. `--status 已完成`
   threads one status into every printed add line; without it both
   table and lines mean 进行中.
4. Show the fitted table to the user — WITH the 文本 (状态) column, not
   just content+hours — and confirm status BEFORE writing: same-day
   logs default 进行中; backfills of past days usually want 已完成
   (`daily fit --status 已完成` regenerates the add lines). After OK,
   run each add line. Cite which digest session each record came from.

Hours corrections after insert: the webhook only WRITES — no read, no
delete. `log-labor update --record-id R -h <h>` fixes a row while R is
still known from its add output; without R the user edits the row by
hand in the sheet UI. Mention this ONLY when the user asks to change
an already-logged row — never repeat it on every 报工.

## Another sheet, same shape (interim)

CHECK FIRST which case the user is in — they are different operations:

- **Sheet TITLE changed / same key + same shape** (rename, or pointing at
  the same webhook under a new title): do NOT ask the user which case it
  is — act on the common case and let doctor arbitrate:
  1. `log-labor config set sheet "<new title>"`
  2. `log-labor doctor` — key probe PASS = done, report it; key probe
     FAIL = it is a different-key sheet → treat as the DIFFERENT-sheet
     case below.
  Do NOT copy, back up, jq-inspect, or `profile import` anything, and do
  NOT stop mid-way to ask rename-vs-new-sheet — doctor answers that.
- **A DIFFERENT sheet (own webhook key + field ids)** — TRIGGER: the user
  pastes a webhook URL/key that is not the configured one (「报到我们组的
  表，webhook 是这个：…」). Do NOT add anything yet, and do NOT read a
  doctor PASS as approval — doctor probes the CONFIGURED key only; it
  says nothing about the new sheet. The configured profile's field ids
  may not exist there, and a wrong-id write fails ONLY at the webhook
  (the local preview cannot catch it — the confirm gate stops first).
  Prove the pairing BEFORE any user add: `log-labor doctor
  --write-sample` (marked 0.5h probe row; human deletes it). errcode
  2022004 "field not exists" = the profile does not belong to that
  sheet — do NOT retry and do NOT hand-edit field ids: show the user
  the doctor output (masked key + sheet), ask for the new sheet's
  接收外部数据 示例数据, then `log-labor profile import` it, re-probe,
  and only then add. (Interim alternative: the jq dance below.)

The config holds ONE sheet (key + field-id profile). To point the CLI
at a same-shaped sibling sheet (identical column titles and statuses,
different field ids + webhook key) until native profiles land:

1. `cp ~/.config/log-labor/config.json{,.bak}` — restore = copy back.
2. jq-swap `.key` and every `.profile.fields.<role>.id` in
   `~/.config/log-labor/config.json` (match roles by column TITLE from
   the sibling sheet's 接收外部数据 schema sample); keep the file 0600.
3. `log-labor doctor` — the key probe MUST pass before any add.

Native multi-sheet support (`log-labor profile add|use`, a `--profile`
flag, schema-JSON import that derives the field map by itself) is
ticketed under `docs/tickets/` — do NOT grow the jq dance.

## Session sources — setup for any agent

`daily collect` auto-detects the verified built-ins: opencode
(~/.local/share/opencode/opencode.db), Claude Code (~/.claude/projects),
and Codex (~/.codex/sessions). Any OTHER harness with durable line-JSON
session logs needs a one-time registration — the agent does this itself:

1. Probe: does this harness write session files anywhere under
   `~/.<agent>/`? Look for sqlite stores or `*.jsonl` with per-line
   timestamps (and ideally a session id + project/cwd).
2. Claude-shaped JSONL (timestamp/cwd/sessionId per line):
   `log-labor daily sources add --name <harness> --format jsonl-claude \
      --path <dir>`
   Any other line-JSON shape — declare the field names:
   `log-labor daily sources add --name <harness> --format jsonl-generic \
      --path <dir> --timestamp-field ts --cwd-field project \
      --session-field sid --title-field what`
   (opencode, Claude Code and Codex are built-in; no registration
   needed. The sqlite family is opencode-only — other sqlite stores are
   NOT supported yet.)
3. Verify BEFORE trusting it:
   `log-labor daily sources test --name <harness> [--date D | --from D1 --to D2]`
   → session count + sample rows (already window-clipped). Then
   `daily collect` — every digest line is tagged with the source name.
4. Harness with its OWN schedule (night shifts etc.): give it a
   private work window at registration —
   `log-labor daily sources add … --window-start 22:00 --window-end 06:00`
   (end ≤ start = crosses midnight; a single edge falls back to the
   global window). The global default for every harness:
   `log-labor daily set-window --start 09:00 --end 22:00`. Check who
   has what via `daily sources list`.
5. Harnesses with NO durable transcripts (Cursor, Trae): those sessions
   can't be collected — ask the user what they did there and merge it
   into the same fit.
6. Personal work that must NEVER reach the sheet (求职, job hunting,
   private repos): register regex rules and they drop from EVERY
   digest — matched against project dir basename, full dir, and title:
   `log-labor daily exclude add 'job-seeking|leetcode' '求职|interview'`
   List: `daily exclude list`; drop a rule: `daily exclude remove <re>`.
   A matched session costs a one-line ⚠ notice, nothing more. Draft fit
   rows ONLY from what survived the filter — the sheet is work labor.

Long sessions are an anti-pattern: a session alive ≥72h cannot be
attributed to days honestly, so collect EXCLUDES it with a ⚠ line —
tell the user to start fresh sessions (or at least touch them daily)
instead of arguing with the digest. Never invent a source's contents;
if `test` returns 0 sessions for a day the user says they worked, say
so and ask.

## Onboarding a coworker (张三)

```bash
curl -fsSL https://raw.githubusercontent.com/yyx462/AutoWecom-plugin/master/log-labor/install.sh | sh
# Windows PowerShell: irm https://raw.githubusercontent.com/yyx462/AutoWecom-plugin/master/log-labor/install.ps1 | iex
log-labor init            # key + corp id (zhang.san form) + sheet name
# ONLY when the sheet differs from the team default (other fields/statuses):
log-labor profile import  # paste 接收外部数据 → 示例数据, re-derives the profile
log-labor doctor --write-sample   # one marked row proves the pipeline
log-labor skill install   # this skill, for every agent on the machine
```

If `log-labor` is missing: re-run the one-liner (fetches the release
binary) or build from source (`cd log-labor && go build ./cmd/log-labor`).
