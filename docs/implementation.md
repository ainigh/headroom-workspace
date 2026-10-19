# Implementation Guide — sysq, sysact, Headroom

References: `sysq-design.md` (engine, language, recipes, non-negotiables §23) and `headroom-spec.md` (product behavior). This guide says what to build, in what order, and which decisions are fixed versus the developer's. Where this guide is silent, the developer chooses; where it says **MUST**, there is no latitude.

---

## 1. Components and boundaries

```
┌─────────────────────────────── Headroom.app (Swift/SwiftUI) ───────────────────────────────┐
│  UI (menu bar, panels, graphs, explorer)   Assistant orchestrator   Config loader (YAML)   │
└──────────────┬──────────────────────────────────┬────────────────────────────┬─────────────┘
               │ local HTTP / unix socket          │                            │
┌──────────────▼──────────────┐   ┌───────────────▼──────────────┐   ┌─────────▼────────────┐
│ sysqd (Go binary)           │   │ sysact (Go binary)            │   │ Ollama (external)     │
│ engine · catalog · recipes  │   │ action catalog · confirm gate │   │ chat + tool calling   │
│ sampler · history · HTTP    │   │ argv runner · audit           │   │                       │
└─────────────────────────────┘   └───────────────────────────────┘   └───────────────────────┘
```

Four processes, three repos (`sysq`, `sysact`, `headroom`). `sysq` and `sysact` **MUST** be buildable, testable, and usable with no Headroom present.

## 2. Fixed technology decisions

| Decision | Choice | Why fixed |
|---|---|---|
| sysq / sysact language | Go | single static binary, `starlark-go`, straightforward `exec` with argv, easy seatbelt wrapping |
| Script runtime | `go.starlark.net` | sysq §17; deterministic, no I/O, step limits |
| Subprocess sandbox | `sandbox-exec` with per-primitive `.sb` profiles; fallback: dedicated unprivileged macOS user | sysq §6.1, §23.3 |
| History store | SQLite (WAL) | sysq §25 |
| Headroom UI | Swift + SwiftUI, `MenuBarExtra` + `NSPanel` for the floating panel | native menu bar, low idle cost |
| Headroom ↔ sysqd transport | sysq HTTP on loopback (and optionally unix socket) | one surface for UI, API clients, assistant |
| Headroom ↔ sysact transport | unix socket, JSON | not network-reachable |
| Assistant backend | Ollama `/api/chat` with tools; text-loop fallback | headroom-spec §8.6 |
| Configuration | YAML files, hot-reloaded via FSEvents | headroom-spec §12 |

Developer's choice: Go module layout, YAML library, SwiftUI vs AppKit for specific views, charting library, test frameworks, CI.

## 3. sysq

### 3.1 Catalog (primitives)
One YAML file per primitive:
```yaml
name: proc.top
doc: Top processes by cpu or mem (second sample of top).
params:
  by:  { type: enum, values: [cpu, mem], default: cpu }
  n:   { type: int, default: 20, max: 200 }
cost: moderate            # cheap | moderate | expensive
sensitivity: personal     # public | personal | secret
network: false
argv: ["/usr/bin/top", "-l", "2", "-n", "{n}", "-o", "{by}", "-stats", "pid,command,cpu,mem,power"]
parser: top_table         # name of a Go parser
timeout: 5s
schema:
  - { name: pid, type: pid }
  - { name: command, type: str }
  - { name: cpu, type: float, unit: "%" }
  - { name: mem, type: bytes }
sandbox: read-only-default   # profile name
```
- **MUST**: `{param}` placeholders substitute into a single argv element, never split, never shell-quoted. Params are validated against `type`/`values`/`max`/path allowlist before substitution.
- **MUST**: a primitive with `network: true` uses a sandbox profile that allows outbound and is disabled by default.
- **MUST**: every primitive has a fixture (`tests/fixtures/<name>.txt`) and a golden parsed table.
- Developer's choice: parser implementation style (hand-written vs table-driven), fixture capture tooling.

### 3.2 Executor
- Build argv, wrap with `sandbox-exec -f <profile>`, apply timeout, cap stdout bytes, run, parse, attach `meta`.
- Seatbelt profile baseline:
  ```
  (version 1) (deny default)
  (allow process-exec (literal "<binary>")) (allow process-fork)
  (allow file-read*) (deny file-read* (subpath "<each excluded root>"))
  (allow file-write* (literal "/dev/null") (subpath "<scratch tmp>"))
  (deny network*)
  (allow sysctl-read) (allow mach-lookup) (allow iokit-open)   ; trimmed per primitive
  ```
- **MUST**: CI runs the full catalog under the sandbox and fails if any primitive attempts a write or a network connection (detect via seatbelt denials in the unified log, or `fs_usage` in a dedicated test VM).
- **MUST**: sudo-requiring primitives (`powermetrics`) are only enabled when a sudoers rule for the exact binary and flags exists; sysq checks with `sudo -n -l` at startup and otherwise marks them unavailable.

### 3.3 Table type and operators
- `Table{Schema []Col; Rows [][]Value; Meta}`; `Value` is a tagged union over sysq §12.1 types; nested `table` values are `*Table`.
- Implement operators from sysq §13 as methods on `*Table`, each pure, each with unit tests over small tables.
- Hard caps (defaults, configurable): 5 000 rows, 2 MB per table, nesting depth 4.
- Developer's choice: columnar vs row storage internally.

### 3.4 Starlark embedding
- Native types: `Table`, `Expr`, `Duration`, `Bytes`, `Path`, `Timestamp`.
- Builtins: catalog namespaces, `emit`, `snapshot`, `watch`, `history`, `expr`, `now`, `ago`, `humanize`, `try`, `recipes.*`.
- Fuel: `thread.SetMaxExecutionSteps`; cost ledger charged on primitive call; both from budget params on `run`.
- **MUST**: `load()` restricted to the recipe directory; no other builtins beyond Starlark's universe.

### 3.5 Sugar pass
- Tokenizer + lowering in Go, input sysq-script, output Starlark source.
- **MUST**: lowering is a pure text transform; `explain` returns the lowered source; golden tests cover every sugar form in sysq §18.
- Predicates lower to `expr("...")` producing an `Expr` AST; the planner pushes supported AST shapes into primitives that accept predicates (`find`, `log show`, `mdfind`, `sqlite`).
- Developer's choice: parser technology (hand-rolled recursive descent is fine).

### 3.6 Recipes
- Loader reads `recipes/**/*.star`, evaluates the `recipe(...)` header, builds the dependency graph, rejects cycles, and runs `explain` on each body to derive cost, sensitivity, primitives used, and output schema.
- **MUST**: declared metadata limited to sysq §23.5; a recipe attempting to declare `cost` or `sensitivity` fails to load.
- `proposed/` is loaded for `explain`/review only, never callable.
- CLI subcommands generated from recipe params (`cobra` or similar); output renderers: table, json, csv, md.

### 3.7 Sampler, history, HTTP
- `sysqd` reads `schedules.yaml`; samples via the same engine; writes to SQLite with downsampling tiers (5 s / 1 min / 10 min); retention per schedule.
- `history()`, `history.events()`, `history.narrative()`, `history.consumers_at()` implemented as primitives over the store.
- HTTP per sysq §26; `chi`/`net/http`; SSE for subscribe/alerts.
- **MUST**: loopback bind only; per-client bearer token; client table with `max_sensitivity`, `allow_scripts` (default false), `origins`; first-contact requests return 401 with a pairing code the host app approves.
- Caching: per `(primitive, params)` with per-primitive TTL; shared across UI, API, assistant.

### 3.8 Audit
Append-only JSONL at `~/Library/Application Support/sysq/audit/`: caller id, script or recipe, params, primitives executed, duration, rows/bytes returned, sensitivity, truncation.

## 4. sysact

- Catalog YAML per action: `name, doc, params(schema), tier, argv, undo_argv?, requires_admin, protected_paths_check`.
- Initial catalog: `app.quit`, `app.force_quit`, `ai.ollama_unload`, `ai.ollama_stop`, `fs.trash`, `cache.clear` (per allowlisted app cache dir), `mem.purge` (admin), `spotlight.rebuild` (admin), `disk.verify`, `tm.start_backup`.
- Protocol over unix socket: `prepare(name, params)` → validated plan with effect statement; `execute(plan_id, confirmations[])`.
- **MUST**: `execute` requires at least one confirmation record signed by the host; `sysact` rejects any call lacking one. There is no flag, env var, or config that bypasses this.
- **MUST**: `fs.trash` uses `osascript`/Finder trash (recoverable), never `rm`. Paths under protected roots are refused at `prepare`.
- **MUST**: argv only, same rules as sysq §3.1.
- Admin actions use `AuthorizationExecuteWithPrivileges` replacement (`SMJobBless`/`osascript with administrator privileges`) — developer's choice, but the password prompt is macOS's, never a Headroom text field.
- Audit JSONL in the sysq format, separate directory.

## 5. Headroom

### 5.1 Config loader
- Loads `groups.yaml`, `layout.yaml`, `thresholds.yaml`, `modes.yaml`, `sampling.yaml`, `assistant.yaml`, `api.yaml`, `notifications.yaml`, `retention.yaml` from Application Support; ships defaults inside the bundle; copies defaults on first run.
- Validates against JSON Schemas (committed in the repo); invalid file → keep last good config, show a non-blocking banner.
- Hot reload on change; publishes a new immutable `AppConfig` value.
- **MUST**: adding a resource group, changing a threshold, rebinding a mode, or editing the system prompt requires no code change.
- Beyond the group bindings, groups.yaml names the `overview` recipe (large-view header), the `item_profile` recipe (Actions panel, assistant item scope) and the Storage Explorer's recipes (`explorer:` with the names of their root/min/age parameters); assistant.yaml names the `preflight_recipe` run before a model loads. sampling.yaml's `narrative` and `events` pass through to sysqd's schedules.yaml.

### 5.2 Data layer
- `SysqClient`: typed wrapper over sysq HTTP; `runRecipe`, `run`, `describe`, `explain`, `history`, `subscribe`.
- Headroom keeps sysqd and sysact running as per-user LaunchAgents (binaries inside the app bundle) and registers its own two sysqd clients (`headroom-ui`, `headroom-assistant`) over the admin socket at each launch; tokens stay in memory.
- `GroupStore`: for each configured group, subscribes to headline/detail/consumer recipes at the rate from `sampling.yaml` (adjusted for view open / battery), holds the latest `GroupState{headline, status, sparkline, metrics, consumers}`.
- **MUST**: UI code never calls `SysqClient` directly; it observes `GroupStore` and related stores (see `separation-of-concerns.md`).

### 5.3 UI
Per `ui-design.md`. SwiftUI views bound to view models; a `GroupBox` view renders any group from its `GroupState` without knowing which group it is.

### 5.4 Assistant orchestrator
- Builds the request per headroom-spec §8.9; calls Ollama `/api/chat` with tools; executes tool calls through `SysqClient`; enforces per-conversation fuel, call count, and wall-clock caps; parses the five sections; routes `headroom.request_action` into the confirmation flow.
- Text-loop fallback for models without tool support: detect from Ollama model capabilities or a config flag.
- **MUST**: action execution path is `AssistantOrchestrator → ConfirmationFlow (one modal sheet with a timed-disabled confirm button) → SysactClient.execute(plan, confirmation)`. No other path to `SysactClient.execute` exists in the app; enforce by making `execute` require a `ConfirmationToken` value that only `ConfirmationFlow` can construct.
- **MUST**: the assistant's sysq client token is created with `max_sensitivity` from `assistant.yaml` and `allow_scripts: true`; it never receives `secret`.
- Logging per headroom-spec §8.10 to SQLite; `SummaryHistory` store with deletion that also purges from any cached context.

### 5.5 HTTP API
Headroom does not implement an API server. It configures sysqd (`api.yaml` → sysqd client table and exposed recipe set) and provides the pairing UI for new clients.

## 6. Build order

| Milestone | Deliverable | Done when |
|---|---|---|
| M0 | sysq core: catalog loader, executor + seatbelt, 12 primitives, `Table`, `describe`/`run` CLI | all fixtures pass; sandbox write-test passes |
| M1 | Starlark + sugar + operators + fuel; `explain` | sugar goldens pass; §16 worked example runs |
| M2 | Recipe loader + derived facts + generated CLI; starter library (sysq §20) | `sysq health overview` works in a terminal |
| M3 | sysqd: sampler, history, HTTP, clients | a curl script can read history and subscribe |
| M4 | Monitoring primitives (sysq §24) and Headroom group recipes | every group in headroom-spec §4 has live recipes |
| M5 | sysact with initial catalog and confirmation protocol | tests prove `execute` fails without confirmation |
| M6 | Headroom: config loader, GroupStore, small/large/drill-in views | app shows live data from YAML-defined groups |
| M7 | Actions panel, Storage Explorer, Graphs | |
| M8 | Assistant (tools + fallback), confirmation flow, logging, summary history | example turn in headroom-spec §8.9 reproduces end to end |
| M9 | API pairing UI, settings editor, notifications, performance pass | Headroom + sysqd under 1 % CPU idle, < 60 MB RSS combined |

## 7. Testing requirements

- sysq: fixture/golden tests per primitive; operator unit tests; sugar goldens; sandbox denial test in CI; fuzz the sugar tokenizer and param validator.
- sysact: table-driven tests that every catalog entry refuses protected paths and refuses execution without confirmation.
- Headroom: snapshot tests for `GroupBox` across statuses/modes; orchestrator tests with a fake Ollama replaying tool-call sequences, including malformed responses and injection attempts in table cells.
- End-to-end: scripted scenario "idle Ollama model" producing the §8.9 turn.

## 8. Developer latitude (explicit)
Free to choose: Go package structure, YAML/schema libraries, charting, SQLite wrapper, how `top`/`ps` columns are parsed, SSE vs WebSocket for the UI's own subscription, caching strategy details, whether sysq and sysact share a Go module for argv/sandbox/audit code (recommended), animation and polish, logging framework.

Not free to change: anything marked MUST here, sysq §23, headroom-spec §13.
