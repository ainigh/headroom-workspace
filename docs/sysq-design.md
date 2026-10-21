# sysq — A Read-Only Query Language for Your Mac

A design for letting a model interrogate a single-user Mac (Apple Silicon, 64 GB) as if it were a database: the model composes queries, a local runtime executes predefined primitives, and only shaped results come back. Nothing the model can express is capable of mutating the system, so per-request approval is unnecessary.

---

## 1. Core principles

1. **The runtime guarantees read-only, not the model.** Safety lives in the executor: a fixed catalog, argv-only invocation, OS-level sandboxing. The model's intent is irrelevant.
2. **The model never writes shell.** It writes structured queries against named primitives. There is no "raw command" escape hatch.
3. **Heavy lifting happens locally.** Filtering, sorting, aggregation, joins and diffs run in the runtime. The model receives the answer, not the haystack.
4. **Everything is typed and discoverable.** Every primitive declares its output schema, parameters, cost and sensitivity. The model can ask `describe()` and learn the language.
5. **The system can speak first.** Beyond answering questions, a rules/baseline layer surfaces what needs attention.

---

## 2. Architecture

```
┌────────────┐   JSON query    ┌────────────────┐   argv (no shell)   ┌──────────────┐
│   Model    │ ──────────────▶ │  sysq runtime  │ ───────────────────▶│ seatbelt-    │
│ (via MCP)  │ ◀────────────── │  planner +     │ ◀───────────────────│ sandboxed    │
└────────────┘   JSON result   │  operators     │   stdout (json)     │ primitives   │
                               └───────┬────────┘                     └──────────────┘
                                       │
                            ┌──────────┴──────────┐
                            │ catalog · cache ·   │
                            │ baselines · audit   │
                            └─────────────────────┘
```

**Layers**

| Layer | Responsibility |
|---|---|
| Transport | MCP server (preferred) or CLI (`sysq query '<json>'`). Model sees a small tool set: `describe`, `query`, `watch`, `attention`. |
| Planner | Validates the query against the catalog, type-checks params, estimates cost, rejects anything out of policy. |
| Primitives | Named verbs bound to specific Mac commands with fixed flags and a parser into JSON. |
| Operators | Pure functions over JSON (filter, select, sort, limit, group, agg, join, diff, pluck). |
| Sandbox | `sandbox-exec` seatbelt profile: deny `file-write*`, deny `network-outbound`, allow read of scoped roots. Plus `ulimit`/timeouts. |
| State | Result cache, snapshot store for diffs/baselines, append-only audit log. |

---

## 3. Primitive catalog (what the model can read)

Each primitive = `{ name, description, params(schema), returns(schema), cost, sensitivity, command_template, parser }`.

### 3.1 Hardware & system
| Primitive | Backed by |
|---|---|
| `sys.info` | `sw_vers`, `uname -a`, `sysctl hw.*`, `system_profiler SPHardwareDataType -json` |
| `sys.uptime` | `uptime`, `sysctl kern.boottime` |
| `sys.memory` | `vm_stat`, `sysctl hw.memsize`, `memory_pressure` |
| `sys.cpu` | `sysctl -n machdep.cpu.brand_string`, `top -l 1` header, load averages |
| `sys.thermal` | `pmset -g therm`, `sysctl machdep.xcpm.cpu_thermal_level` |
| `sys.power` | `pmset -g batt`, `pmset -g`, `system_profiler SPPowerDataType -json` |
| `sys.displays` / `sys.usb` / `sys.bluetooth` | `system_profiler SP*DataType -json` |

### 3.2 Storage & filesystem
| Primitive | Backed by |
|---|---|
| `disk.volumes` | `df -h`, `diskutil list`, `diskutil info -plist` |
| `disk.usage(path, depth≤3)` | `du -d N` with time budget |
| `fs.list(path, glob?)` | `ls -la` / `find -maxdepth` |
| `fs.stat(path)` | `stat -f` |
| `fs.search(query, scope?)` | `mdfind` (Spotlight) |
| `fs.meta(path)` | `mdls -plist` |
| `fs.read(path, max_bytes, head/tail)` | `head`/`tail` — **text only, capped, redaction applied** |
| `fs.hash(path)` | `shasum -a 256` |
| `fs.large(path, min_size, limit)` | `find -size +N` |
| `fs.recent(path, since)` | `find -newermt` |
| `tm.status` | `tmutil status`, `tmutil listbackups` |

### 3.3 Processes & services
| Primitive | Backed by |
|---|---|
| `proc.list` | `ps -axo pid,ppid,user,%cpu,%mem,rss,start,command` |
| `proc.top(n, by)` | `top -l 2 -o cpu/mem` (second sample) |
| `proc.info(pid)` | `ps -p`, `lsof -p` (files, sockets) |
| `proc.tree` | derived from `ps` ppid |
| `svc.launchd` | `launchctl list`, `launchctl print` |
| `svc.login_items` | `osascript -e 'tell app "System Events" to get login items'`, `~/Library/LaunchAgents` listing |
| `svc.cron` | `crontab -l` |

### 3.4 Network
| Primitive | Backed by |
|---|---|
| `net.interfaces` | `ifconfig`, `networksetup -listallhardwareports` |
| `net.wifi` | `networksetup -getairportnetwork`, `wdutil info` |
| `net.listeners` | `lsof -iTCP -sTCP:LISTEN -P -n` |
| `net.connections` | `lsof -i -P -n`, `netstat -an` |
| `net.dns` | `scutil --dns`, `networksetup -getdnsservers` |
| `net.routes` | `netstat -rn` |
| `net.firewall` | `/usr/libexec/ApplicationFirewall/socketfilterfw --getglobalstate` |

### 3.5 Software & packages
| Primitive | Backed by |
|---|---|
| `apps.installed` | `system_profiler SPApplicationsDataType -json`, `/Applications` scan |
| `pkg.brew` | `brew list --versions`, `brew outdated` (**no `brew update`**) |
| `pkg.npm_global` / `pkg.pip` / `pkg.cargo` | respective `list` commands |
| `dev.tools` | `xcode-select -p`, versions of node/python/go/rustc/docker |
| `docker.ps` / `docker.images` | `docker ps -a --format json` (if present) |
| `git.status(path)` / `git.log(path, n)` / `git.branches(path)` | read-only git (**never `fetch`/`pull`**) |
| `git.repos(root)` | `find -name .git -maxdepth N` |

### 3.6 Logs & events
| Primitive | Backed by |
|---|---|
| `log.system(since, predicate?, limit)` | `log show --last --style json --predicate` |
| `log.crashes(since)` | `~/Library/Logs/DiagnosticReports` listing + headers |
| `log.file(path, tail_n)` | `tail -n` on allowlisted log dirs |
| `sys.updates` | `softwareupdate --history` (history only; `--list` is network, opt-in) |

### 3.7 Preferences & config
| Primitive | Backed by |
|---|---|
| `prefs.read(domain, key?)` | `defaults read` / `defaults export` |
| `prefs.plist(path)` | `plutil -p` |
| `sec.status` | `csrutil status`, `fdesetup status`, `spctl --status`, Gatekeeper |
| `users.list` | `dscl . list /Users`, `who` |

### 3.8 App data (opt-in, high sensitivity)
| Primitive | Backed by |
|---|---|
| `db.sqlite(path, sql)` | `sqlite3 -readonly -json` on allowlisted DBs; SQL parsed and restricted to `SELECT` |
| `browser.history(since)` / `browser.tabs` | Safari/Chrome DBs via `db.sqlite` or `osascript` |
| `cal.events(range)` / `notes.list` | `osascript` (read verbs only) |

---

## 4. Operators (how the model shapes results)

Operators are pure, run in the runtime, and are the only way to combine primitives.

| Operator | Purpose |
|---|---|
| `filter(expr)` | Row predicate. Small expression language: comparisons, `and/or/not`, `contains`, `matches(regex)`, `in`. |
| `select(fields...)` | Project columns. |
| `sort(field, dir)` | Order rows. |
| `limit(n, offset?)` | Cap rows (hard max enforced regardless). |
| `group(field) → agg(count/sum/avg/min/max)` | Aggregation. |
| `join(left, right, on)` | Combine two primitives (e.g. `proc.list` ⋈ `net.listeners` on pid). |
| `pluck(path)` | Drill into nested JSON. |
| `diff(snapshot_a, snapshot_b)` | Added / removed / changed rows. |
| `summarize(field, buckets)` | Histograms, percentiles. |
| `map(template)` | Light reshaping into a model-friendly format. |

### Query shape
```json
{
  "from": "proc.list",
  "join": { "with": "net.listeners", "on": "pid" },
  "filter": "cpu > 20 or port in [80, 443, 5432]",
  "select": ["pid", "command", "cpu", "port"],
  "sort": { "by": "cpu", "dir": "desc" },
  "limit": 10
}
```

Pipelines can nest: `{ "from": {query}, ... }` — but depth and total cost are capped.

---

## 5. Model-facing tools (the surface area)

| Tool | Behavior |
|---|---|
| `describe(primitive?)` | Returns catalog or one primitive's full schema. The model learns the language from this, not from docs. |
| `query(q)` | Executes one query/pipeline. Returns `{ data, meta: { rows, truncated, cost_ms, cached, sensitivity } }`. |
| `snapshot(name, q)` | Persist a query result for later `diff`. |
| `watch(q, interval, until)` | Repeated sampling (bounded) for trends — e.g. memory over 60 s. |
| `attention()` | Runs the rules engine and returns current findings. |
| `explain(q)` | Dry run: what commands would execute, estimated cost, sensitivity classes touched. |

---

## 6. Safety model

### 6.1 Structural (cannot be bypassed by query content)
- **Argv-only execution.** No `sh -c`, no string interpolation. Parameters are validated against a schema (type, enum, regex, path-within-allowlist) and passed as discrete arguments.
- **Flag freezing.** Each primitive's flags are fixed in the template. Dangerous flags (`find -exec/-delete`, `du --output`, `git` write verbs, `defaults write`, `sqlite3` non-SELECT) are not expressible.
- **Seatbelt profile** for every subprocess:
  - `(deny file-write*)` except `/dev/null` and a scratch tmp dir
  - `(deny network-outbound)` (opt-in exception for explicitly network primitives like `softwareupdate --list`)
  - `(allow file-read*)` restricted to configured roots
- **Denylist of pseudo-read-only commands** that actually mutate or phone home: `git fetch/pull`, `brew update/upgrade`, `softwareupdate --install`, `tmutil startbackup`, `mdutil`, `killall`, anything under `sudo`.
- **No privilege escalation.** Runs as the user. Primitives needing root (`powermetrics`, `fs_usage`) are either excluded or allowed via a narrow NOPASSWD sudoers rule for that exact binary + flags, decided once by the human.

### 6.2 Resource limits
- Per-call timeout (default 10 s; `du`/`find`/`log show` get a time budget and return partial + `truncated: true`).
- Output caps (rows and bytes) enforced before the model sees anything.
- Path scoping: allowed roots (e.g. `~`, `/Applications`, `/usr/local`, `/opt/homebrew`), with explicit excludes (`~/.ssh`, `~/Library/Keychains`, `~/.gnupg`, `~/.aws`, `~/.config/gh`, 1Password dirs).
- Concurrency cap and rate limiting.
- Cost classes (`cheap` / `moderate` / `expensive`) with a per-session budget so a model can't accidentally thrash the disk.

### 6.3 Privacy
- Sensitivity classes on every primitive and on derived rows: `public` (hardware, versions), `personal` (file names, process names, history), `secret` (file contents).
- **Redaction pass** on all `fs.read`, `log.*`, and `prefs.*` output: regexes for API keys, tokens, private keys, passwords in URLs, credit-card patterns.
- `secret`-class primitives disabled by default; enabled per-session by the human, never by the model.
- Everything stays on the machine; the runtime itself has no outbound network.

### 6.4 Audit
- Append-only log: timestamp, query, commands executed, duration, bytes returned, sensitivity touched.
- `sysq audit --last 1h` for human review.

---

## 7. Attention layer (surfacing what matters)

Rules are declarative and live in a YAML file the human owns. Each rule is a query + condition + severity + message.

```yaml
- id: disk_nearly_full
  query: { from: disk.volumes, filter: "mounted and used_pct > 90" }
  severity: high
  message: "{{name}} is {{used_pct}}% full"

- id: cpu_hog
  query: { from: proc.top, filter: "cpu > 50" }
  sustain: 10m
  severity: medium

- id: new_login_item
  query: { from: svc.login_items }
  diff_against: baseline
  on: added
  severity: high

- id: unexpected_listener
  query: { from: net.listeners, filter: "not port in allowlist" }
  severity: high

- id: failed_launchd
  query: { from: svc.launchd, filter: "last_exit != 0" }
  severity: medium

- id: timemachine_stale
  query: { from: tm.status, filter: "hours_since_last_backup > 48" }
  severity: medium
```

**Baselines**: `sysq baseline capture` snapshots stable sets (login items, launch agents, listeners, installed apps, brew packages, kernel extensions). `attention()` diffs against them so drift is visible. The model can also create temporary snapshots to answer "what changed since this morning?"

---

## 8. Example model interactions

**"Why is my fan running?"**
1. `proc.top(by=cpu)` → `sys.thermal` → `sys.power`
2. Join and filter → "`com.apple.photoanalysisd` at 180 % CPU for 14 min; thermal level 2; on battery."

**"What's eating my disk?"**
1. `disk.volumes` → `disk.usage(~, depth=2)` sorted desc limit 15 → `fs.large(~, >1GB)`
2. Returns top directories and files, never the full tree.

**"Did anything change after that install?"**
1. `diff(snapshot("pre"), snapshot("post"))` over `svc.launchd`, `svc.login_items`, `net.listeners`, `apps.installed`.

**"Is this port 5432 thing mine?"**
1. `net.listeners` ⋈ `proc.list` on pid → `proc.info(pid)` → `pkg.brew` lookup for `postgresql`.

---

## 9. Implementation sketch

- **Language**: Python (fast to write, rich parsing) or Go (single binary, easy to sandbox). Python + `uv` is fine for a single user.
- **Repo layout**
  ```
  sysq/
    catalog/        # one YAML per primitive: template, params, parser, schema, cost, sensitivity
    parsers/        # plist/json/table → dict
    operators.py
    planner.py      # validation + cost + policy
    sandbox/        # seatbelt .sb profiles
    rules.yaml
    server.py       # MCP server
    cli.py
    audit/
  ```
- **Parsing**: prefer `-json` / `-plist` → `plistlib`; fall back to column parsers for `ps`, `df`, `lsof`.
- **Expression language**: tiny, deliberately not Turing-complete; implement with a parser like `lark` or reuse a safe subset of `jq`/`jmespath`.
- **MCP**: expose the six tools in §5; the model never sees bash.
- **Testing**: every primitive gets a fixture of real output + a golden parsed result; a test that runs the whole catalog under the seatbelt and asserts zero write syscalls (`fs_usage`-based check during CI).

---

## 10. Phasing

| Phase | Scope |
|---|---|
| 0 | Catalog format, planner, argv executor, seatbelt profile, 10 primitives (sys/disk/proc/net basics), `query` + `describe`. |
| 1 | Full catalog, operators incl. join/diff, caching, audit log, MCP server. |
| 2 | Snapshots, baselines, rules engine, `attention()`, `watch()`. |
| 3 | Opt-in sensitive primitives with redaction, sudo-narrow primitives, a small local dashboard of findings. |

---

## 11. Open decisions

- Expression language: custom vs. jmespath subset.
- Whether `fs.read` exists at all in the default profile, or only metadata.
- Seatbelt is officially deprecated by Apple but still functional; fallback is a dedicated low-privilege macOS user running the executor.
- Cache TTLs per primitive (hardware: hours; processes: seconds).
- How the model requests elevation of a sensitivity class — probably it can't; it asks the human in prose, the human flips a flag.

---
---

# Part II — Tables as the universal value, and a script layer

## 12. The table model

Every primitive, operator and script expression produces a **Table**. Nothing else crosses the model boundary.

```json
{
  "schema": [
    { "name": "pid",     "type": "pid" },
    { "name": "command", "type": "str" },
    { "name": "cpu",     "type": "float", "unit": "%" },
    { "name": "open_files", "type": "table", "schema": [ ... ] }
  ],
  "rows": [
    [412, "postgres", 2.1, { "rows": [...], "schema": [...] }]
  ],
  "meta": {
    "rows": 1, "truncated": false,
    "source": ["proc.top", "proc.info"],
    "cost_ms": 84, "sensitivity": "personal",
    "dropped_by_limit": 19,
    "snapshot_ts": "2026-10-19T09:12:03-07:00"
  }
}
```

### 12.1 Type system
| Type | Notes |
|---|---|
| `int`, `float`, `bool`, `str` | scalars |
| `ts` | ISO-8601 with offset; comparable, subtractable → `duration` |
| `duration` | seconds internally, rendered humanly |
| `bytes` | integer bytes, rendered humanly, `unit` kept in schema |
| `path` | normalized absolute path; carries `within_allowlist` flag |
| `pid`, `port`, `uid` | ints with domain meaning, so `join on pid` is type-checked |
| `json` | opaque nested value (for raw plist blobs) |
| `table` | a nested table → enables arbitrary nesting |
| `enum<...>` | fixed vocabularies (e.g. launchd state) |

Scalars are 1×1 tables. Lists of scalars are N×1 tables. This keeps every operator total: there is no "wrong shape" to pass.

### 12.2 Nesting and flattening
- `nest(group_by: field, as: name)` — collapse rows sharing `field` into a `table`-typed column.
- `unnest(column)` — explode a `table` column back into rows, repeating the parent fields.
- Primitives may return nested tables natively (`proc.info` → `open_files`, `sockets`; `disk.volumes` → `partitions`).
- Dot-access drills into nested tables: `proc.info(412).open_files | filter kind == "REG"`.

### 12.3 Provenance
`meta.source` lists every primitive that contributed; `meta.lineage` (optional, `explain` mode) is the operator tree. This lets the model, or the human, see why a number is what it is.

---

## 13. Operators (revised as a closed algebra)

All operators are `Table → Table` (or `Table × Table → Table`) and run locally.

| Operator | Signature |
|---|---|
| `filter expr` | row predicate |
| `select a, b as c, expr as d` | projection + computed columns |
| `sort by field [asc|desc], ...` | |
| `limit n [offset m]` | hard cap still enforced |
| `distinct [fields]` | |
| `group by f, g { count(), sum(x), avg(x), min, max, p50, p95, any, all, list(x) }` | aggregation; `list(x)` yields a `table` column |
| `join T2 on field` / `left join` / `anti join` | typed join keys |
| `union T2`, `except T2`, `intersect T2` | set ops |
| `diff T2 [on key]` | → rows with `change ∈ {added, removed, changed}` plus before/after |
| `nest` / `unnest` | see §12.2 |
| `pivot` / `unpivot` | reshaping |
| `window f over (partition by g order by h)` | running totals, lag, rank — useful on `watch` samples |
| `sample n` | random rows |
| `head n` / `tail n` | |

Expression language inside `filter`/`select`: arithmetic, comparisons, `and/or/not`, `in`, `contains`, `matches`, `startswith`, `coalesce`, `now()`, `ago(duration)`, `humanize(bytes|duration)`, `basename(path)`, `dirname(path)`, string functions. Pure, total, no side effects. A `ts` compared with a `duration` `d` compares against `now() - d`, exactly as `ago(d)`: `modified < 7d` means "modified more than 7 days ago".

---

## 14. sysq-script: the mini language

### 14.1 Design constraints
- **Deterministic and total.** No recursion, no `while`, no unbounded loops. `for` iterates a table and is capped by that table's row count (which is itself capped).
- **Fuel-metered.** Every statement, operator and primitive call deducts fuel; primitives deduct by cost class. Budget per script and per session. Running out = clean abort with partial `emit`s returned.
- **No I/O except the catalog.** No file writes, no network, no `exec`. The language has no syntax for them.
- **Only `emit` returns data.** Intermediates never leave the machine unless emitted.

### 14.2 Syntax

```
# bindings
let hogs = proc.top(by: "cpu", n: 20) | filter cpu > 30
let total_home = disk.usage("~", depth: 1) | group {} { sum(size) as bytes }

# scalars and tables mix freely
let threshold = 50
let big = hogs | filter cpu > threshold

# control flow
if count(big) > 0 {
  emit big
} else if count(hogs) > 0 {
  emit "Moderate load only" , hogs | limit 5
} else {
  emit "Idle"
}

# bounded iteration (row bound, fuel bound)
for p in big | limit 5 {
  let files = proc.info(p.pid).open_files | filter kind == "REG" and path startswith "~"
  emit { pid: p.pid, command: p.command, home_files: files | limit 10 }
}

# pure functions (no recursion)
fn listening_by(cmd) {
  net.listeners | join proc.list on pid | filter command contains cmd
}
emit listening_by("node")

# snapshots and time
let before = snapshot("launchd_morning")
let drift  = svc.launchd | diff before on label
if count(drift) > 0 { emit drift }

# watch produces a table with a `t` column; window ops work on it
let mem = watch(sys.memory, every: 2s, for: 30s)
emit mem | select t, pressure, free | window avg(free) over (order by t rows 5)
```

### 14.3 Semantics
- Pipelines (`|`) are left-to-right table transforms.
- Primitive calls are **lazy**: the planner can push `filter`/`select`/`limit` into the primitive (e.g. `find -newermt`, `log show --predicate`) before executing.
- Results are cached per script by `(primitive, params)`; calling `proc.list` twice in one script samples once unless `fresh:` is set.
- `emit` accepts a table, a scalar, or a record `{k: v}` whose values are tables → the response is an ordered list of emitted items, each a table with a label.
- Errors are values: a failed primitive yields an empty table with `meta.error`, so scripts don't crash mid-way; `assert`/`try` exist for the model to branch on.

### 14.4 Implementation choice
Rather than a bespoke parser, embed **Starlark** (Go or Python implementation): already deterministic, no recursion, no unbounded iteration, no I/O, easy to add a fuel meter. Expose `Table` as a native type with method-style operators (`t.filter(...)`, `t.join(...)`) and keep the pipeline syntax as sugar via a pre-pass, or accept method chaining directly. This is a weekend of work instead of a month.

### 14.5 Tool surface (revised)
| Tool | |
|---|---|
| `describe(primitive?)` | catalog and schemas, including each primitive's column types |
| `run(script, budget?)` | execute sysq-script, return emitted tables + meta |
| `query(q)` | convenience: a single pipeline (same engine) |
| `explain(script)` | plan, pushed-down predicates, estimated fuel, sensitivity touched — no execution |
| `snapshot(name, table)` / `snapshots()` | persistence for diffs |
| `attention()` | rules engine (rules are themselves sysq-scripts) |

---

## 15. Why this stays safe
- Tables are data; the only verbs are catalog primitives whose argv is fixed.
- The language cannot name a file to write, a host to contact, or a process to signal: those nouns don't exist in its grammar.
- Termination is guaranteed by construction (no recursion/while) and by fuel.
- Output volume is bounded by `limit` caps, byte caps, and the `emit`-only rule.
- The human still controls sensitivity classes and allowlisted roots; a script that touches a disabled class fails at `explain`/plan time, not mid-run.

## 16. Worked example: "something is slow"

```
let cpu  = proc.top(by: "cpu", n: 10)
let mem  = proc.top(by: "mem", n: 10)
let io   = disk.volumes | filter used_pct > 85
let swap = sys.memory | select swap_used, pressure
let hot  = sys.thermal

let culprits = cpu | union mem | distinct pid
             | join (net.connections | group by pid { count() as conns }) on pid left

emit { culprits: culprits | sort by cpu desc | limit 8,
       memory: swap, thermal: hot, full_volumes: io }

if any(cpu, cpu > 80) and hot.level >= 2 {
  emit "Likely thermal throttling from a CPU hog"
}
```
The model receives four small tables and one sentence — not `ps`, `top`, `df`, `vm_stat` and `lsof` dumps.

---
---

# Part III — Starlark embedding, syntactic sugar, and the recipe library

## 17. Embedding Starlark

- **Runtime**: `starlark-go` (single static binary, easy to seatbelt) or `starlark-rust`. Python's `starlark` port is fine for prototyping.
- **Native types added**: `Table`, `Expr` (expression AST), `Duration`, `Bytes`, `Path`, `Timestamp`.
- **Builtins added**: the primitive catalog (as namespaced callables `proc.top(...)`), `emit`, `snapshot`, `watch`, `expr`, `now`, `ago`, `humanize`, `recipes.*`.
- **Builtins removed/absent**: nothing in Starlark does I/O, so there is nothing to remove. `load()` is restricted to the recipe library path.
- **Fuel**: `thread.SetMaxExecutionSteps` (starlark-go) for interpretation steps, plus a separate cost ledger decremented by primitive calls (cheap=1, moderate=10, expensive=100 units). Both enforced.
- **Rows** are Starlark structs (`r.pid`, `r.cpu`); tables are iterable, so `for r in t:` is native and bounded by the table's row cap.

## 18. Sugar (desugars to Starlark before execution)

| Sugar | Lowers to | Why |
|---|---|---|
| `a \| filter cpu > 30 \| limit 5` | `a.filter(expr("cpu > 30")).limit(5)` | readable pipelines |
| `filter <pred>` / `select a, b as c, x*2 as y` / `sort by f desc` / `group by f { count() as n }` | `Expr` AST objects | pushdown into `find`, `log show --predicate`, `sqlite`, `mdfind`; static analysis for `explain` |
| `filter(lambda r: ...)` | native | escape hatch; opaque, no pushdown |
| `2s`, `30m`, `4h`, `1.5GB`, `512MB` | `Duration(2)`, `Bytes(...)` | typed literals |
| `~/Downloads` (unquoted path literal starting with `~` or `/`) | `Path("~/Downloads")` | paths are first-class and validated |
| `ago(2h)` | `now() - Duration(...)` | time filters |
| `t.cpu` | `t.col("cpu")` → N×1 table | column extraction |
| `t.cpu[0]`, `t.cpu.max()` | scalar | aggregate convenience |
| `t ?? other` | `t if count(t) > 0 else other` | fallbacks |
| `try proc.info(pid)` | returns empty table with `meta.error` instead of raising | error-as-value |
| `join T on pid` / `left join` / `anti join` | `.join(T, on="pid", how=...)` | |
| `diff snapshot("x") on label` | `.diff(snapshot("x"), on="label")` | |
| `f"{n} processes over {th}%"` | `"...".format(...)` | Starlark lacks f-strings |
| `emit name: table` | `emit(table, label="name")` | labeled outputs |
| `assert count(t) > 0, "msg"` | builtin | early exit with message |

Rules for sugar: every form must lower to valid Starlark with no new semantics; the lowered form is what `explain` shows, so nothing is hidden.

## 19. Recipes

### 19.1 What a recipe is
A Starlark function plus a metadata header, stored in `recipes/<group>/<name>.star`.

```python
# recipes/disk/hogs.star
recipe(
  name   = "disk.hogs",
  doc    = "Largest directories and files under a root.",
  params = {
    "root":   param(Path, default="~"),
    "depth":  param(int, default=2, max=3),
    "min":    param(Bytes, default=500MB),
    "limit":  param(int, default=15, max=50),
  },
  tags   = ["disk", "cleanup"],
  example= "disk.hogs(root=~/Library, min=1GB)",
)

def run(root, depth, min, limit):
  dirs  = disk.usage(root, depth=depth) | filter size > min | sort by size desc | limit limit
  files = fs.large(root, min_size=min, limit=limit)
  return { "directories": dirs, "files": files }
```

- **Declared**: name, params (typed, defaulted, bounded), docs, tags, example.
- **Derived at load time** by running `explain` on the body: primitives used, cost estimate, sensitivity class, output schema. Derived facts can't be misstated.
- Return value is a table or a record of tables; `emit` inside recipes is allowed but discouraged (return and let the caller decide).

### 19.2 Three ways to call one recipe
| Caller | Form |
|---|---|
| Model, in a script | `recipes.disk.hogs(root=~/Library) \| filter size > 2GB` |
| Model, as a tool | `run_recipe("disk.hogs", {root: "~/Library"})` |
| Human, in the terminal | `sysq disk hogs --root ~/Library --min 1GB` (auto-generated CLI with `--json`, `--csv`, `--md`, shell completion) |

Params map 1:1 to CLI flags; table output renders as aligned columns with humanized bytes/durations in the terminal, JSON for the model.

### 19.3 Composition
Recipes call primitives, operators and other recipes. The loader builds the dependency graph, rejects cycles, and computes cost/sensitivity transitively. A recipe that depends on a disabled sensitivity class is listed but marked unavailable.

### 19.4 Attention rules are recipes
```python
recipe(name="attention.disk_full", severity="high", when="used_pct > 90",
       message=f"{name} is {used_pct}% full", schedule="15m")
def run():
  return disk.volumes | filter mounted
```
`attention()` runs every recipe that declares `severity` and returns rows matching `when`. Baseline-diff rules add `baseline="launchd"`.

### 19.5 Curation loop
1. The model writes an ad-hoc script that proves useful.
2. It calls `propose_recipe(script, metadata)` → file lands in `recipes/proposed/`.
3. `sysq recipe review` shows the lowered Starlark, derived cost/sensitivity, and a dry run.
4. Human runs `sysq recipe promote <name>` → moves into the library, generates a fixture test from the dry run.
5. The model can only *propose*; promotion is always human. The library therefore stays small, trusted, and readable.

### 19.6 Discovery
- `describe()` returns primitives **and** recipes with the same schema, so the model sees one catalog.
- `recipes.search("port")` → tag/doc search.
- `sysq recipes` lists groups; `sysq recipe explain disk.hogs` shows lowered code, plan, and example output.

## 20. Starter recipe library

| Group | Recipe | Purpose |
|---|---|---|
| health | `health.overview` | one table each: cpu, memory, disk, thermal, battery, uptime |
| health | `health.why_slow` | top cpu/mem joined with connections, thermal, swap (the §16 script) |
| health | `health.why_fan` | cpu hogs + thermal level + power source |
| disk | `disk.hogs` | largest dirs/files under a root |
| disk | `disk.recent_big(since, min)` | large files modified recently (downloads, caches) |
| disk | `disk.caches` | sizes of known cache dirs (Xcode DerivedData, brew, npm, pip, Docker) |
| disk | `disk.duplicates(root)` | same size+hash groups, capped |
| proc | `proc.hogs` | sustained cpu/mem offenders using `watch` |
| proc | `proc.by_name(cmd)` | processes + their files/sockets |
| proc | `proc.zombies_orphans` | |
| net | `net.who_listens` | listeners joined to process + package origin |
| net | `net.talking_to(host\|port)` | connections filtered + owning process |
| net | `net.overview` | interfaces, wifi, dns, routes in one record |
| startup | `startup.items` | login items + launch agents/daemons + their binaries and signatures |
| startup | `startup.drift` | diff of the above vs baseline |
| software | `software.outdated` | brew/npm/pip outdated + macOS update history |
| software | `software.recent_installs(since)` | apps and packages added recently |
| dev | `dev.repos_dirty(root)` | git repos with uncommitted or unpushed work |
| dev | `dev.toolchain` | versions of everything |
| dev | `dev.node_modules_bloat(root)` | largest `node_modules` trees |
| logs | `logs.errors(since, process?)` | unified log errors grouped by process |
| logs | `logs.crashes(since)` | crash reports summarized |
| security | `security.posture` | SIP, FileVault, Gatekeeper, firewall, unsigned login items |
| security | `security.unexpected_listeners` | listeners not in allowlist |
| backup | `backup.status` | Time Machine freshness + last failures |
| changes | `changes.since(snapshot)` | diff of startup items, listeners, apps, packages vs a snapshot |

## 21. Repo layout (revised)

```
sysq/
  catalog/            primitives (YAML: argv template, parser, schema, cost, sensitivity)
  engine/
    starlark/         embedding, native types, builtins, fuel
    sugar/            tokenizer + lowering pass, with golden tests (sugar → starlark)
    planner/          pushdown, cost, sensitivity, explain
    exec/             argv runner + seatbelt profiles
  recipes/
    health/ disk/ proc/ net/ startup/ software/ dev/ logs/ security/ backup/ changes/
    proposed/         model-proposed, awaiting promotion
  rules.star          attention recipes (or inline in recipes/)
  server/             MCP tools: describe, run, run_recipe, explain, snapshot, attention, propose_recipe
  cli/                auto-generated subcommands from recipes
  tests/
    fixtures/         real command output per primitive
    golden/           parsed tables, lowered scripts, recipe outputs
```

## 22. Phasing (revised)

| Phase | Scope |
|---|---|
| 0 | starlark-go embedding, `Table` type, 10 primitives, argv runner, seatbelt, `run` + `describe`, fuel |
| 1 | Sugar pass with golden tests, pushdown for `find`/`log`/`sqlite`, full catalog, caching, audit |
| 2 | Recipe loader (metadata, derived facts, dependency graph), CLI generation, starter library |
| 3 | Snapshots/baselines, attention engine as recipes, `watch`, `propose_recipe` + review/promote flow |
| 4 | Opt-in sensitive primitives with redaction, narrow-sudo primitives, local findings dashboard |

---
---

# Part IV — Non-negotiables, and what sysq provides to host apps

## 23. Non-negotiables

These hold for every implementation and every future change. Anything that violates one is a bug, not a trade-off.

1. **The model never writes shell.** No raw-command tool exists in any surface sysq exposes.
2. **Primitives are argv templates with frozen flags.** Parameters are validated and passed as discrete arguments; no string interpolation into a shell.
3. **Every subprocess runs under a write-denying, network-denying sandbox.** Opt-in network primitives are individually allowlisted.
4. **Every sugar lowers to plain Starlark, and `explain` shows the lowered form.** No hidden semantics.
5. **Recipe cost and sensitivity are derived by analysis, never declared.** Declared fields are limited to name, docs, params, tags, examples, and rule metadata.
6. **Execution is total and fuel-metered.** No recursion, no unbounded loops, hard caps on rows, bytes, time, and cost.
7. **Only `emit`/return values leave the machine boundary**, and only after caps and redaction.
8. **Sensitivity classes are enabled by the human, never by the model.** A disabled class fails at plan time.
9. **Models propose recipes; humans promote them.**
10. **sysq itself is permanently read-only.** Mutation lives in a separate component with its own confirmation discipline (§27).

## 24. Additional primitives for monitoring hosts

| Primitive | Backed by | Notes |
|---|---|---|
| `gpu.stats` | `ioreg -r -c IOAccelerator` (utilization, in-use memory), `system_profiler SPDisplaysDataType -json` | Apple Silicon reports GPU busy % through IOAccelerator PerformanceStatistics |
| `ane.activity` | `powermetrics --samplers ane -n 1` | requires narrow sudo rule; otherwise unavailable |
| `cpu.cores` | `powermetrics --samplers cpu_power -n 1` (narrow sudo) or `top -l 2` + `sysctl hw.perflevel*` | P-core vs E-core split |
| `thermal.state` | `pmset -g therm`, `powermetrics --samplers thermal` (narrow sudo), `sysctl machdep.xcpm.cpu_thermal_level` | fan RPM only via sudo |
| `battery.state` | `pmset -g batt`, `ioreg -r -c AppleSmartBattery` | cycle count, health, wattage |
| `net.throughput` | `netstat -ib` sampled twice via `watch` | bytes/s per interface |
| `net.by_process` | `nettop -P -l 1 -J bytes_in,bytes_out` | per-process bandwidth |
| `proc.energy` | `top -l 2 -stats pid,command,power` | energy impact column |
| `ai.ollama` | `ollama ps`, `ollama list`, `~/.ollama/models` size | loaded models, memory, idle time |
| `ai.lmstudio` / `ai.mlx` | `lms ps` if present; `~/.cache/lm-studio`, `~/.cache/huggingface` sizes | |
| `ai.gpu_holders` | `gpu.stats` joined with `proc.list` on pid where available | anything holding large GPU memory |
| `disk.purgeable` | `diskutil info -plist /` (APFS purgeable) | |
| `disk.io` | `iostat -d -c 2` | read/write activity |
| `sys.events` | derived: app launches (`log show` predicate on `launchd`), pressure transitions | for graph markers |

## 25. Sampler and history store

sysq gains a long-running daemon mode (`sysqd`) so host apps don't each build their own sampler.

- **Schedules** are YAML: `{ recipe, params, every, retention, when: [view_open | on_ac | always] }`. Hidden or unrequested recipes sample less often.
- **History store**: append-only time series of recipe outputs, downsampled by age (5 s → 1 min → 10 min buckets). Stored as SQLite in `~/Library/Application Support/sysq/history.db`.
- **Primitives over history**: `history(recipe, range)` returns a table with a `t` column; all operators and `window` apply. `history.events(range)` returns markers.
- **Compact narrative**: `history.narrative(range)` is a recipe that turns recent history into a few plain sentences ("memory pressure high for 20 min; disk free fell 12 GB today") for model context.
- **Live updates**: `subscribe(recipe, every)` and `alert(expr)` stream rows to in-process listeners; the HTTP layer exposes these as SSE.

## 26. HTTP serving layer

`sysqd` can expose the same read-only surface over local HTTP (`127.0.0.1:11435` by default, Ollama-style).

| Endpoint | Maps to |
|---|---|
| `GET /v1/status` | daemon version, catalog version, enabled sensitivity classes |
| `GET /v1/recipes`, `GET /v1/recipes/{name}` | `describe` |
| `POST /v1/run/{recipe}` | `run_recipe` with params |
| `POST /v1/run` | `run(script)` — disabled by default for HTTP clients; enabled per client |
| `GET /v1/history/{recipe}?range=` | history table |
| `GET /v1/subscribe/{recipe}?every=` (SSE) | live rows |
| `POST /v1/alerts` (SSE) | `alert(expr)` |

- Loopback only; per-client token issued on first approval; origin allowlist for browsers; privacy level per client maps directly to the maximum sensitivity class that client may touch.
- Permanently read-only by construction: the HTTP layer can only reach sysq.

## 27. Companion: `sysact` (mutation runner)

Mutation is out of scope for sysq but host apps need it. `sysact` is a sibling component with the same discipline inverted:

- **Action catalog** in YAML: `{ name, doc, params(schema), tier: safe|caution|risky, argv template, undo?: argv template, requires_admin }`. Examples: `app.quit(bundle_id)`, `app.force_quit(pid)`, `ai.ollama_unload(model)`, `fs.trash(path)`, `cache.clear(app)`, `mem.purge`, `spotlight.rebuild(volume)`, `disk.verify(volume)`.
- **No scripting, no composition.** One action per request; parameters validated; argv only.
- **Every call is confirmed by a human** through the host app; `sysact` itself exposes no auto-approve path. Delay and wording are host policy, but zero confirmations is not expressible.
- **Protected paths** (system locations, keychains, security settings) are refused at catalog level.
- Audit log shared format with sysq.
- A model may *request* an action by name and params; it cannot invoke `sysact` directly.

## 28. Sensitivity ↔ privacy levels

| Host privacy level | Max sysq sensitivity |
|---|---|
| Aggregates only | `public` |
| + top consumers | `personal` (names, pids, sizes; no paths outside allowlisted roots) |
| + file details | `personal` with paths; `fs.read` still off |
| Full (human-only sessions) | `secret` |
