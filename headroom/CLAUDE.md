# Headroom (Swift)

Menu bar app + `HeadroomKit` package. See `../docs/headroom-spec.md`, `../docs/ui-design.md`, `../docs/separation-of-concerns.md`, `../docs/implementation.md §5`.

## Invariants
- The app target contains views and lifecycle only. Stores, view models, clients, orchestrator, config loader live in `HeadroomKit`.
- Views never call `SysqClient`/`SysactClient`/`OllamaClient`; they observe stores.
- `SysactClient.execute` requires a `ConfirmationToken` whose initializer is internal to `ConfirmationFlow`. Test that no other module can construct one.
- No group name, recipe name, threshold, or prompt text is hard-coded; all come from `config/*.yaml` and `config/prompts/`. `config/` holds the shipped defaults, copied to Application Support on first run.
- The assistant's sysq client is created with `max_sensitivity` from `assistant.yaml` and never `secret`.
- Headroom runs no HTTP server; it configures sysqd's client table and provides pairing UI.

## Layout
```
Headroom/            app target (status item, drop-down panel, windows, views)
HeadroomKit/         Swift package: Config, Clients, Stores, ViewModels, Assistant, Confirmation
config/              default YAML + JSON Schemas + prompts
```
Build after sysq M3 and sysact M5 exist; until then develop against sysq `--fixtures` mode and sysact `--dry-run`.
