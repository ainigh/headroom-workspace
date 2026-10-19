# sysact (Go)

Confirmed mutation runner. See `../docs/sysq-design.md §27` and `../docs/implementation.md §4`.

## Invariants (tests enforce these)
- `execute(plan_id, confirmation)` fails without a confirmation record signed by the host. No flag, env var, or config bypasses this. Write that test first.
- Argv only, same substitution rules as sysq. Reuse sysq's exec/argv/audit code via a shared module if you create one.
- `fs.trash` goes through Finder (recoverable), never `rm`. Protected roots in `protected_paths.yaml` are refused at `prepare`.
- One action per request. No scripting, no composition.
- Unix socket only.

## Initial catalog
app.quit, app.force_quit, ai.ollama_unload, ai.ollama_stop, fs.trash, cache.clear, mem.purge (admin), spotlight.rebuild (admin), disk.verify, tm.start_backup. See `actions/examples/` for the shape.
