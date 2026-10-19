# Headroom workspace

Three components, one repo, each independently buildable:

- `sysq/`   — Go. Read-only system query engine: catalog, Starlark scripts, recipes, sampler, history, HTTP. **Never mutates anything.**
- `sysact/` — Go. Confirmed mutation runner with a YAML action catalog. **Never runs without a confirmation record.**
- `headroom/` — Swift/SwiftUI menu bar app + `HeadroomKit` package. UI, assistant orchestrator, YAML config. Contains no system-reading or mutating code.

## Read these before writing code
1. `docs/sysq-design.md` — the engine, language, recipe model. §23 lists ten non-negotiables.
2. `docs/headroom-spec.md` — product behavior.
3. `docs/implementation.md` — fixed tech choices, YAML shapes, build order (M0–M9), MUSTs vs developer latitude.
4. `docs/separation-of-concerns.md` — layer rules, what the UI may know, config surface.
5. `docs/ui-design.md` — layouts and interaction rules.
Each component directory has its own CLAUDE.md with local rules.

## Working rules
- Follow `docs/implementation.md §6` milestone order. Finish a milestone (its "done when" test passes) before starting the next. Update `TASKS.md` as you go.
- Anything marked **MUST** in the docs, or listed in `sysq-design.md §23`, is not negotiable. If a task seems to require violating one, stop and explain instead of working around it.
- Where the docs are silent, choose, and record the choice in `DECISIONS.md` (one line each). Don't ask about things the docs leave to you.
- Safety guarantees get tests first: sandbox write-denial, argv-only substitution, `sysact` refusing execution without a confirmation, `ConfirmationToken` having no public initializer.
- Small commits per milestone step, messages naming the milestone (`M1: sugar lowering for pipelines`).
- Never add a raw-shell tool, an auto-approve flag, or a way to declare recipe cost/sensitivity. These are the three most likely "helpful" mistakes.
- This machine is an Apple Silicon Mac, single user. Fixtures must come from real command output on this machine; capture them with the fixture tool you build in M0.
- Keep the docs current: if implementation reveals a spec gap, amend the relevant doc in `docs/` in the same commit and note it in `DECISIONS.md`.

## Conventions
- Go: standard layout per component (`cmd/`, `internal/`), `go test ./...` must pass, `golangci-lint` clean.
- Swift: `HeadroomKit` is a Swift package with its own tests; the app target holds only views and app lifecycle.
- YAML files have a JSON Schema committed next to their loader; invalid config never crashes, it falls back to last-good.
- User data under `~/Library/Application Support/{sysq,sysact,Headroom}/`.
