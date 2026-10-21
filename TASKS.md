# Milestones (from docs/implementation.md §6)

- [x] M0 sysq core: catalog loader, executor + seatbelt, fixture tool, 12 primitives, Table, `describe`/`run` CLI — done when fixtures pass and sandbox write-denial test passes
  - fixtures/goldens pass; sandbox render tests pass on Linux; the darwin write/network/excluded-read denial tests (`internal/exec/sandbox_darwin_test.go`) run on the Mac and in macOS CI
  - on the Mac: `sysq fixture capture --all` then `go test ./internal/parsers -update` to replace synthetic fixtures
- [x] M1 Starlark + sugar + operators + fuel; `explain` — done when sugar goldens pass and the design §16 script runs
  - `sysq --fixtures builtin run -f sysq/tests/golden/sugar/design_16.sysq`; `sysq explain -f ...` shows the lowered Starlark and plan
- [x] M2 Recipe loader, derived metadata, generated CLI, starter library — done when `sysq health overview` works
  - `sysq --fixtures builtin health overview` (live on the Mac: `sysq health overview`); every shipped recipe runs against fixtures in `go test ./internal/recipes`
- [x] M3 sysqd: sampler, history, HTTP, clients — done when curl can read history and subscribe
  - `sysq/scripts/curl-demo.sh` against a running sysqd; `go test ./internal/daemon` does the same in-process
- [x] M4 Monitoring primitives (design §24) + Headroom group recipes — done when every group in headroom-spec §4 has live recipes
  - `go test ./internal/recipes -run Headroom` runs every groups.yaml binding (headline, detail, consumers, sparkline, attention) and every modes.yaml recipe
  - needs the Mac: `ane.activity`/`thermal.pressure` need a sudoers rule for powermetrics; `ai.ollama_ps`/`ai.lmstudio_ps` need enabling in policy.yaml
- [x] M5 sysact: catalog, prepare/execute, confirmation requirement — done when tests prove execute fails without confirmation
  - `cd sysact && go test ./...` (confirm_required_test.go was committed before the server)
- [x] M6 Headroom: config loader, GroupStore, small/large/drill-in views — done when the app shows live data from YAML groups
  - HeadroomKit: config (schemas, last-good fallback, FSEvents reload), clients (sysq HTTP+SSE, admin socket, sysact JSON lines, Ollama), services (LaunchAgents, host clients, schedules write-through), GroupStore, view models, key-group selector
  - Verified here: `swift test --package-path headroom/HeadroomKit` (GroupStore against a fake sysq: shared feeds, rates, thresholds→status, polling fallback, view models)
  - On the Mac (the app target is macOS-only and CI runners don't start): `HL_FROM_SOURCE=1 ./install.sh` or `headroom/scripts/build-app.sh`, then the panel shows live groups
- [x] M7 Actions panel, Storage Explorer, Graphs
  - ActionStore + ActionsPanel (item.profile from groups.yaml, prefilled catalog actions by tier, recipes with Run / Copy as CLI / Explain), Storage Explorer (volumes, folders/files/types, filters, squarified treemap, Optimize suggestions, per-item Trash… through the confirmation), Graphs (Swift Charts, ranges from retention.yaml, event markers, hover values, "top consumers at" via consumers_at)
  - Tests: treemap layout, explorer bindings; sysq's TestHeadroomGroupBindings runs every explorer/overview/item_profile/preflight recipe
- [x] M8 Assistant (tools + text fallback), confirmation flow, logging, summary history — done when the headroom-spec §8.9 example turn reproduces
  - `OrchestratorTests.testExampleTurnEndToEnd`: fake Ollama replays the §8.9 tool calls and answer; the action goes through the one sheet (delay enforced), executes with a token, the result is fed back and a second answer follows; an injection attempt in a table cell stays data; log and summaries recorded
  - Text-loop, budget, stop, malformed-response and unknown-action tests; `scripts/check-confirmation-token.sh` proves no other module can construct a ConfirmationToken
- [x] M9 API pairing UI, settings editor, notifications, performance pass — done when idle < 1% CPU, < 60 MB RSS combined
  - Settings tabs edit the YAML files (validated before save, "Edit file…", live external edits), pairing approve/revoke, launch at login, Update button; notifications with cooldown/quiet hours (tested)
  - Performance design: sampling slows with no view open / on battery (reported to sysqd), hidden groups at hidden rates, detail feeds only while a view is open, shared feeds, sysqd as a low-priority background LaunchAgent
  - Measure on the Mac: `headroom/scripts/perf-check.sh 120` with no Headroom window open

# After M9: UI pass and bug hunt (2026-10-21)

- [x] Panel drops down from an NSStatusItem (left click panel, right click menu), laid out like super-mac-tts-stt-service: status header, mode chips, key groups, window tiles, consumers, setup line, bottom bar (version/Update, Open at login, Quit)
- [x] Settings → Permissions (notifications, powermetrics sudoers line from sysqd `sudo_rules`, optional FDA / Finder automation / network primitives / login item); `SetupStore` tested
- [x] Hidden main menu so ⌘C/⌘V/⌘W/⌘Q work in windows; notifications shown while a window is frontmost
- [ ] On the Mac: build (`HL_FROM_SOURCE=1 ./install.sh`) and check the panel, right-click menu, Permissions tab and Update
