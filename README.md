# Headroom workspace

A menu bar app for the Mac that shows what's using your Mac, helps you fix it, and tells your other
tools how much room is left — built on two small engines that keep reading and changing strictly
apart.

| Component | What it is |
|---|---|
| **`sysq/`** (Go) | A read-only query engine: a catalog of macOS commands as frozen argv templates, a small query language (sysq-script, lowered to Starlark), a recipe library, a sampler with history, and a local HTTP API (`sysqd`, `127.0.0.1:11435`). It never changes anything. |
| **`sysact/`** (Go) | A confirmed mutation runner: a YAML catalog of actions (quit an app, unload a model, move to Trash, …) that runs one only with a host-signed confirmation of that exact plan. |
| **`headroom/`** (Swift) | The menu bar app (SwiftUI) and `HeadroomKit` (config, clients, stores, assistant, confirmation flow). It contains no system-reading or mutating code of its own. |

Design: [`docs/`](docs) — start with `sysq-design.md` and `headroom-spec.md`.

## Install

Paste this into Terminal (it installs Homebrew's `gh` if needed and signs you in to GitHub, since
the repository may be private):

```bash
/bin/bash -c 'for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do [ -x "$b" ] && eval "$("$b" shellenv)"; done; command -v gh >/dev/null || brew install gh; gh auth status >/dev/null 2>&1 || gh auth login --web --git-protocol https -h github.com; gh api -H "Accept: application/vnd.github.raw" repos/ainigh/headroom-workspace/contents/install.sh | bash'
```

It takes the latest release GitHub built; if there isn't one, it downloads the source and builds it
on your Mac (that needs Apple's command line tools with Swift 5.9 or newer, and Go — it runs
`brew install go` when Go is missing). It installs into `~/Applications/Headroom.app` and opens it:
look for the gauge in the menu bar. `HL_BRANCH=name` follows another branch; `HL_FROM_SOURCE=1`
always builds.

**Update**: the panel's bottom bar shows the version; when the branch has something newer it shows
**Update** (GitHub's build when there is one for that exact commit, otherwise it builds that commit
here). The right-click menu and Settings → General have it too. Running the install line again
works too. Nothing depends on GitHub Actions working.

**Permissions**: the panel shows "things left to set up" until they're done; Settings →
Permissions lists each one, whether it's in place, and where to grant it: notifications, the
powermetrics sudoers rule (with the exact line to add), and the optional ones (Full Disk Access for
sysqd, Finder automation for Trash, local model servers, open at login).

## Using it

- Click the gauge in the menu bar (or ⌃⌥H anywhere) and the panel drops down: how the Mac is
  doing, the mode (View / Optimize / Deep Dive / Repair), the four key groups, the app's windows
  (Overview, Graphs, Storage, Ask AI, History), the top consumers, and the bottom bar (version and
  **Update**, **Open at login**, **Quit**). Click elsewhere and it closes; the pin keeps it open
  and on top. Click a group to drill in; `⋯` on a consumer opens its Actions panel.
- Right-click the gauge for the menu: mode, the windows, pause sampling, updates, settings, quit.
- Headroom starts `sysqd` and `sysact` as LaunchAgents from inside the app
  (`~/Library/LaunchAgents/com.ainigh.headroom.*.plist`), so history keeps recording while the app
  is closed. Logs: `~/Library/Logs/Headroom/`.
- **Ask AI** needs [Ollama](https://ollama.com) running locally; pick the model in Settings →
  Assistant. It reads through sysq and can only *request* an action, which you confirm.
- Everything is YAML under `~/Library/Application Support/Headroom/config/`; Settings edits the
  same files and changes made in an editor apply at once.
- Idle cost check (no Headroom window open): `headroom/scripts/perf-check.sh 120`.

## Build from source

```bash
headroom/scripts/build-app.sh            # on a Mac: headroom/build/Headroom.app and Headroom.zip
UNIVERSAL=0 headroom/scripts/build-app.sh  # this Mac's chip only (Command Line Tools are enough)
```

The bundle carries `sysq`, `sysqd` and `sysact` in `Contents/Resources/bin` and the default
configuration in `Contents/Resources/config`. From a terminal:

```bash
~/Applications/Headroom.app/Contents/Resources/bin/sysq health overview
~/Applications/Headroom.app/Contents/Resources/bin/sysq describe
```

## Tests

```bash
(cd sysq && go test ./...)        # on macOS this includes the sandbox write/network denial tests
(cd sysact && go test ./...)      # execute refuses anything without a valid confirmation
swift test --package-path headroom/HeadroomKit   # runs anywhere, Linux too
```

sysq ships recorded fixtures (`sysq --fixtures builtin …`), so everything above runs without a Mac
except the sandbox tests. The fixtures in `sysq/tests/fixtures` are written from each command's
documented output format; replace them with your Mac's real output:
`sysq fixture capture --all && (cd sysq && go test ./internal/parsers -update)`.

## Where things live

| | |
|---|---|
| `~/Library/Application Support/Headroom/config/` | groups, layout, thresholds, modes, sampling, assistant, API, prompts (YAML, hot-reloaded) |
| `~/Library/Application Support/sysq/` | `policy.yaml`, `schedules.yaml`, `clients.yaml`, `history.db`, your recipes, snapshots, audit log |
| `~/Library/Application Support/sysact/` | `protected_paths.yaml`, the host key, audit log |
| `~/Library/Logs/Headroom/update.log` | what updates did |

## What needs your Mac (once)

- **powermetrics** (Neural Engine activity, thermal pressure, per-cluster power) needs a narrow
  sudoers rule for the exact binary and flags; without it those metrics show as unavailable. sysq
  checks with `sudo -n -l` at startup. Add with `sudo visudo -f /etc/sudoers.d/sysq`:

  ```
  yourname ALL=(root) NOPASSWD: /usr/bin/powermetrics --samplers cpu_power\,gpu_power\,ane_power -n 1 -i 1000, /usr/bin/powermetrics --samplers thermal -n 1 -i 1000
  ```

  (`sysq describe ane.activity` and `sysq describe thermal.pressure` show the exact arguments.)
- **Loaded local models** (`ai.ollama_ps`, `ai.lmstudio_ps`) talk to the model servers on
  localhost, so they are network primitives and stay off until you enable them by name in
  `~/Library/Application Support/sysq/policy.yaml`:

  ```yaml
  version: 1
  network_primitives: [ai.ollama_ps, ai.lmstudio_ps]
  ```

  They run under a loopback-only sandbox. Without them, Local AI still reads the model runner
  processes and model files.

## Safety

- **Reading is free, changing is confirmed, and they are different programs.** sysq can only run
  catalog commands with fixed flags, as argv (never through a shell), inside a seatbelt sandbox
  that denies writes and network. Nothing it can express changes the system.
- **Every change goes through sysact, and sysact runs nothing without a confirmation** of that
  exact plan, signed by Headroom after you press Confirm on a sheet whose button stays disabled
  for a moment. There is no flag, setting or environment variable that skips it. Moving to Trash
  goes through Finder (recoverable); protected locations are refused outright; admin actions use
  macOS's own password dialog.
- **No raw shell anywhere** — not for the assistant, not in the UI, not in the API. The assistant
  can query freely through sysq and can only *request* one catalog action per answer.
- Everything stays on this Mac: the API listens on 127.0.0.1 only, and the assistant uses a local
  Ollama model.
