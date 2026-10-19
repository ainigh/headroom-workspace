# sysq (Go)

Read-only system query engine. See `../docs/sysq-design.md` (all parts) and `../docs/implementation.md §3`.

## Invariants (tests enforce these)
- Primitives are YAML argv templates; `{param}` fills exactly one argv element after validation. No `sh -c`. No shell quoting.
- Every subprocess runs under `sandbox-exec` with a profile in `sandbox/` that denies writes and network. CI test: run the whole catalog, assert no seatbelt denials of file-write/network and no files created.
- Starlark via `go.starlark.net`; `load()` restricted to `recipes/`; step limit + cost ledger on every run.
- Sugar lowers to Starlark text; `explain` returns the lowered text; every form in design §18 has a golden test.
- Recipe metadata: declaring `cost` or `sensitivity` is a load error. They are derived from `explain`.
- Hard caps: 5000 rows, 2 MB per table, nesting depth 4, per-primitive timeouts.
- HTTP binds 127.0.0.1 only; per-client token; `POST /v1/run` off by default per client.

## Layout to create
```
cmd/sysq           CLI (generated subcommands from recipes)
cmd/sysqd          daemon: sampler, history, HTTP
internal/catalog   YAML loader + validation
internal/exec      argv runner, sandbox wrapper, timeouts, caps
internal/parsers   one parser per output style; fixture-driven
internal/table     Table, Value, operators
internal/script    starlark embedding, builtins, fuel
internal/sugar     tokenizer + lowering, goldens
internal/plan      explain, pushdown, cost, sensitivity
internal/recipes   loader, dependency graph, derived metadata, proposed/
internal/history   sqlite store, downsampling, history primitives
internal/httpapi   routes, SSE, clients
internal/audit     JSONL
catalog/           primitives (*.yaml)
recipes/           starter library (design §20)
sandbox/           seatbelt profiles
tests/fixtures, tests/golden
```
`catalog/examples/` and `recipes/examples/` contain one seed file each; match their shape.

## Start with M0
Build the fixture capture tool first (`sysq fixture capture <primitive>`), then the first 12 primitives: sys.info, sys.memory, sys.cpu, disk.volumes, disk.usage, fs.list, proc.list, proc.top, net.listeners, net.interfaces, svc.launchd, log.system.
