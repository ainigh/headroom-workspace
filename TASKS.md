# Milestones (from docs/implementation.md §6)

- [x] M0 sysq core: catalog loader, executor + seatbelt, fixture tool, 12 primitives, Table, `describe`/`run` CLI — done when fixtures pass and sandbox write-denial test passes
  - fixtures/goldens pass; sandbox render tests pass on Linux; the darwin write/network/excluded-read denial tests (`internal/exec/sandbox_darwin_test.go`) run on the Mac and in macOS CI
  - on the Mac: `sysq fixture capture --all` then `go test ./internal/parsers -update` to replace synthetic fixtures
- [x] M1 Starlark + sugar + operators + fuel; `explain` — done when sugar goldens pass and the design §16 script runs
  - `sysq --fixtures builtin run -f sysq/tests/golden/sugar/design_16.sysq`; `sysq explain -f ...` shows the lowered Starlark and plan
- [ ] M2 Recipe loader, derived metadata, generated CLI, starter library — done when `sysq health overview` works
- [ ] M3 sysqd: sampler, history, HTTP, clients — done when curl can read history and subscribe
- [ ] M4 Monitoring primitives (design §24) + Headroom group recipes — done when every group in headroom-spec §4 has live recipes
- [ ] M5 sysact: catalog, prepare/execute, confirmation requirement — done when tests prove execute fails without confirmation
- [ ] M6 Headroom: config loader, GroupStore, small/large/drill-in views — done when the app shows live data from YAML groups
- [ ] M7 Actions panel, Storage Explorer, Graphs
- [ ] M8 Assistant (tools + text fallback), confirmation flow, logging, summary history — done when the headroom-spec §8.9 example turn reproduces
- [ ] M9 API pairing UI, settings editor, notifications, performance pass — done when idle < 1% CPU, < 60 MB RSS combined
