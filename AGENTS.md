# AutoWecom-plugin

Extension repo for the AutoWecom stack. Core (AutoWecom-core: broker +
manager + brain) owns I/O and secrets; this repo owns what models and
humans consume: **skills, CLIs, manifests, docs**. Rebuilt 2026-09-11 —
the pre-reset catalog/stub generation is gone; log-labor is the first
plugin of the new generation.

## Rules

- **No keys, no secrets in this tree. Ever.** A plugin may SHIP code that
  performs I/O (e.g. the log-labor CLI writes webhooks as the USER's
  credential) — but the mounted tree must never carry a credential, and
  the copy consumed by the brain must never need one (brain stays keyless,
  core ADR-0004).
- **Core handler first, then the catalog row.** One `catalog.yaml` row =
  one capability the broker will actually serve; rows without handlers
  404 by design. `send_*` and admin surfaces are never rows.
- **Mount contract:** this repo is mounted read-only into the stack
  (`PLUGIN_DIR`, brain sees `/app/plugin`). Same clone serves dev
  (`PLUGIN_DIR=../AutoWecom-plugin/master` from the core worktree) and
  server (auto-deployed checkout).
- **Pin contract:** tags `plugin-x.y.z`; core's deploy pins `PLUGIN_REF`
  (default `origin/master` / the auto-deployed checkout). Stamped,
  idempotent installs — see `log-labor/core/configure.sh`.

## Releases (log-labor)

- Release = the GitHub Actions workflow: tag `log-labor-vX.Y.Z`, push it
  to BOTH remotes (`origin` Gitea AND `github`) — Gitea's commit status
  stays "pending" forever, watch the GitHub run. npm publishes as
  `@yyx462/*`. Bump the pinned fallback in `install.sh`/`install.ps1`
  when tagging (dynamic latest-resolution falls back to the pin).
- CI npm publish is BROKEN (NPM_TOKEN → ENEEDAUTH, Gitea issue #45):
  when the run fails there, publish from the repo with
  `log-labor/npm/publish.sh vX.Y.Z`, each `npm publish` under a pty
  (`script -q <log> npm publish --access public`) so npm's 2FA web-auth
  prints its URL — ONE browser approval, the session carries for the
  remaining packages. npm publish is ASYNC: `+ pkg@ver` prints before
  the package is visible on the registry (minutes) — a 404 right after
  is NOT a publish failure.

## Layout

    catalog.yaml          model-tool registry (rows ONLY with live core handlers)
    log-labor/            first plugin: 报工 CLI + agent skill
      plugin.json         manifest (api, name, entry, env contract)
      core/configure.sh   entry: env|plan|install|health (driven by core deploy.sh)
      cmd/, internal/     Go CLI source (module root = log-labor/)
      skill/              SKILL.md.tmpl (embedded source) + rendered SKILL.md
      install.sh          standalone installer (Gitea raw/release)

## Landing changes (gwt)

This repo is gwt-managed — NEVER hand-branch inside master/. Flow:
`gwt spawn <name>` (name becomes the dir AND branch), work under that
path, `gwt submit-pr "<title>" "<body>"` (opens the Gitea PR via
$GITEA_TOKEN), merge, `gwt remove <name>` / `gwt reap-all`.
Push fails with keychain `-25308` → `gwt doctor`. A commit stranded on
a branch whose PR already merged → `gwt audit` (STRANDED) → `gwt recover
<branch>` opens a NEW PR from the same branch — do NOT cherry-pick by
hand. Session-start dashboard: `gwt context` (auto after
`gwt session install --app opencode`).

## Adding a plugin

1. New sibling dir with `plugin.json` (`"api": 1`) and an entry script
   speaking the verb protocol: `env` (required env names, one per line),
   `plan`, `install`, `health` — all idempotent, all loud on failure.
2. Anything the brain should know goes in `skills/…` (SKILL.md) or a
   catalog row AFTER its core handler lands.
3. Register nothing by hand in core: core's deploy walks `PLUGIN_DIR/*/`
   generically.
