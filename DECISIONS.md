# Decisions log
One line per choice the docs left open. Format: `YYYY-MM-DD — area — decision — why`.

2026-10-19 — repo — Root copies of the five docs removed; `docs/` is the single source — they were byte-identical duplicates that would drift once docs are amended.
2026-10-19 — go — Go 1.25 language level, `go.work` at the root joining `sysq` and `sysact` — golangci-lint here is built with 1.25; Homebrew Go on the Mac is newer.
2026-10-19 — sysq/argv — Shared argv/param/audit code lives in public `sysq/pkg/{argv,audit}`; sysact imports them — implementation.md §8 recommends sharing; Go `internal/` can't cross modules.
2026-10-19 — sysq/argv — Argv elements may embed a placeholder (`-n{n}`, `{min}`→`+1024k`) but each still yields exactly one element; an element may be an optional group (a YAML list) dropped when its params are unset — needed for optional flags like `--predicate`.
2026-10-19 — sysq/argv — A substituted value that would start an element with `-` is refused unless the spec sets `allow_dash` (numbers exempt) — blocks argument injection, which argv-only alone doesn't.
2026-10-19 — sysq/argv — Predicate params (`log show --predicate`) accept only a structured Expr rendered by sysq with escaping, never raw text — keeps "no model-authored command text" true for query dialects too.
2026-10-19 — sysq/catalog — A primitive may have `commands:` (several argv, each with an id) instead of one `argv`; parser receives all outputs — sys.memory needs vm_stat + sysctl + memory_pressure.
2026-10-19 — sysq/catalog — `binary_paths` + `{binary}` argv[0] for tools installed in varying places (ollama, brew); missing binary → table marked unavailable, not an error.
2026-10-19 — sysq/catalog — Loader refuses shell/interpreter binaries (sh, bash, env, osascript, xargs), mutating binaries (rm, kill, curl, …) and known write verbs/flags (`find -delete/-exec`, `git fetch`, `brew update`, `tmutil startbackup`, …) — design §6.1 flag freezing enforced at load time.
2026-10-19 — sysq/exec — Profiles passed inline with `sandbox-exec -p` (no temp profile files); placeholders __EXEC__, __DENY_READ__, __SCRATCH__ rendered per run with SBPL string escaping; added `(deny user-preference-write)` and process-info/ipc-read allowances needed by ps/top.
2026-10-19 — sysq/exec — Subprocess env is minimal (PATH, HOME, TMPDIR=scratch, LANG/LC_ALL=C, TERM=dumb) and each run gets its own scratch dir removed afterwards — parsers see stable C-locale output; no env leaks.
2026-10-19 — sysq/exec — Narrow-sudo primitives run `sudo -n <binary> <flags>` inside a `read-only-sudo` profile that additionally allows writes to /private/var/db/sudo only — sudoers rule names the real binary, never sandbox-exec.
2026-10-19 — sysq/fixtures — Fixtures in `tests/fixtures` are synthetic (written from each command's documented output format) because this workspace was built on Linux; MANIFEST.yaml records provenance and `sysq fixture capture --all` + `go test ./internal/parsers -update` replaces them on the Mac — CLAUDE.md requires real fixtures; this is the closest honest substitute until run there.
2026-10-19 — sysq/table — Cells are plain Go values interpreted by column type (int64 for int/bytes/pid/port/uid, float64 seconds for duration, time.Time for ts) — simpler than a boxed union, and the schema is always present.
2026-10-19 — sysq/engine — Parsers emit rows keyed by column name; a key not in the YAML schema fails the golden test — the schema is the parser's contract.
2026-10-19 — sysq/engine — Shipped fixtures are embedded in the binary (`--fixtures builtin`) so Headroom and HTTP tests run anywhere without a Mac.
2026-10-19 — sysq/policy — User `policy.yaml` can only add excluded paths, never remove shipped ones; caps can only be lowered — the human loosens sensitivity, but structural limits stay.
2026-10-19 — sysq/sugar — Two input syntaxes: sysq-script (`let`, `{ }` blocks, `fn`, `emit x`, design §14.2) and Starlark-with-sugar (recipes, §19); `Detect` picks Starlark when it sees `def`/`load(`/`recipe(`/colon blocks — both appear in the design.
2026-10-19 — sysq/sugar — Lowering is an edit-based text transform: unchanged source (comments, layout) is copied, only sugar spans are rewritten — keeps `explain` output readable and line-faithful.
2026-10-19 — sysq/sugar — `t ?? other` lowers to `fallback(t, lambda: other)` rather than `t if count(t) > 0 else other` — evaluates `t` once; same meaning.
2026-10-19 — sysq/sugar — `t.cpu` stays `t.cpu`: Table's attribute lookup returns the column (the documented `t.col("cpu")`), so lowering needs no type knowledge; a 1-row table's nested-table column drills in (§12.2).
2026-10-19 — sysq/sugar — In sysq-script, a dotted name on either side of a comparison is wrapped in `scalar()` (`hot.level >= 2` → `scalar(hot.level) >= 2`); Starlark can't compare a table with an int, and `scalar` is the identity on non-tables.
2026-10-19 — sysq/sugar — Predicates/projections lower to `expr("<text>", v=v, …)`: free identifiers that are script variables in scope are passed as bindings; at evaluation a column of the input wins over a variable of the same name (design §16 uses `cpu` as both).
2026-10-19 — sysq/sugar — Keyword-named kwargs get a trailing underscore (`for: 30s` → `for_=`); `except` stage → `.except_()` — Starlark reserves those words.
2026-10-19 — sysq/script — Pushdown is runtime-lazy: a primitive reference stays unexecuted until a non-pushable op; pushable conjuncts of `filter` go into the predicate/bound params and limit into the limit param, and the full filter/limit still runs after — correctness never depends on the dialect.
2026-10-19 — sysq/script — `try_` never catches fuel, budget or cancellation errors; recursion and `while` stay disabled (starlark-go FileOptions without Recursion/While).
2026-10-19 — sysq/script — `watch` is bounded to ≥1 s intervals, ≤60 samples, ≤2 min; in fixture mode it doesn't sleep.
2026-10-19 — sysq/script — `emit({...})`: a record whose values are all tables becomes one labeled item per key; any other record becomes a one-row table (nested tables as table cells).
2026-10-19 — sysq/plan — Static explain counts a call inside a `for` loop as 10 calls when estimating cost units; cost class and sensitivity are maxima over everything reachable.
