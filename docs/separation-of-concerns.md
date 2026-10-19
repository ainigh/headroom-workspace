# Separation of Concerns — sysq, sysact, Headroom

Goal: every part below the UI is reusable by other apps, the UI never needs to change when a lower layer changes, and most behavioral changes are YAML edits rather than rebuilds. References `sysq-design.md`, `headroom-spec.md`, `implementation.md`.

---

## 1. Layer map

```
L6  Headroom UI            SwiftUI views; knows only view models
L5  Headroom view models   turn store state + config into render-ready values
L4  Headroom stores        GroupStore, HistoryStore, AssistantStore, ActionStore, ConfigStore
L3  Clients                SysqClient, SysactClient, OllamaClient — typed, transport-only
L2  Services               sysqd (engine, recipes, sampler, history, HTTP), sysact (actions)
L1  Catalogs & recipes     YAML primitives, Starlark recipes, YAML actions
L0  OS                     macOS commands
```

Rules:
- A layer depends only on the layer directly below, through an interface, never on a concrete type two layers down.
- L1 and L2 are separate repos with no knowledge of Headroom.
- L3 is a Swift package (`HeadroomKit`) that any Mac app can import to talk to sysqd/sysact/Ollama.
- L4 and L5 are also in `HeadroomKit`; only L6 is in the app target. A different UI (a Shortcuts action, a Raycast extension, a web dashboard) can reuse L3–L5.

## 2. Reusable units and their contracts

| Unit | Reusable as | Contract | Replaceable without touching |
|---|---|---|---|
| sysq engine | library/binary for any read-only system access | `describe / run / run_recipe / explain / history / subscribe` over HTTP or Go API | everything above L2 |
| Primitive catalog | data | YAML schema in `implementation.md §3.1` | engine code (add a primitive = add a file) |
| Recipe library | data + Starlark | `recipe(...)` header, derived metadata | engine, UI (new recipe appears in catalog) |
| Sampler/history | library | schedules YAML, `history*` primitives | UI, assistant |
| HTTP layer | service | sysq §26 endpoints, versioned | clients |
| sysact | binary for any app needing confirmed mutations | `prepare / execute(plan, confirmations)` over unix socket | UI, assistant, sysq |
| Action catalog | data | YAML in `implementation.md §4` | sysact code |
| `SysqClient` | Swift package | protocol `SysqClientProtocol` | stores (mock in tests) |
| `SysactClient` | Swift package | protocol with `ConfirmationToken` requirement | stores |
| `OllamaClient` | Swift package | protocol `ChatBackend` (chat with tools, streaming) | assistant store (swap for another local runtime) |
| `AssistantOrchestrator` | Swift package | input: `AssistantRequest`; output: stream of `AssistantEvent` (toolCall, toolResult, section, actionRequest, done) | UI, prompt text, model |
| `ConfigStore` | Swift package | typed `AppConfig` from YAML + JSON Schema | every consumer |
| Table renderer | SwiftUI component | takes a sysq `Table` | data source |
| GroupBox component | SwiftUI component | takes `GroupViewModel` | which group it is |

## 3. What the UI may know

The UI knows:
- `GroupViewModel { id, name, symbol, headline(text, unit), status(color), sparkline([Double]), metrics([(label, text)]), consumers([ConsumerViewModel]), reclaimable? }`
- `ModeViewModel { id, label, recipes, actions, question }`
- `Table` (for rendering any recipe output)
- `ActionPlanViewModel { name, effect, params, tier, undo, requiresAdmin, requestedBy }`
- `AssistantEvent`

The UI does not know: recipe names of specific groups, sysq types, HTTP, Ollama, YAML, thresholds, sampling rates, or which command backs a number. Adding a "Disk I/O" group or renaming `mem.headline` touches no view.

## 4. Configuration surface (no rebuild)

All under `~/Library/Application Support/Headroom/config/`, each with a JSON Schema in the repo, hot-reloaded:

| File | Controls | Consumers |
|---|---|---|
| `groups.yaml` | group list, symbol, headline/detail/consumer recipe bindings, sparkline source, attention rule | GroupStore, ViewModels |
| `layout.yaml` | key groups (pinned/auto, hold time), order, hidden groups, panel geometry | ViewModels |
| `thresholds.yaml` | per-group green/amber/red | passed as params to attention recipes |
| `modes.yaml` | per mode per group: recipes, actions, default question, emphasis | ViewModels, Orchestrator |
| `sampling.yaml` | refresh rates by state; written through to sysqd `schedules.yaml` | GroupStore, sysqd |
| `assistant.yaml` | model, privacy level, N summaries, fuel, delays, prompt file path, tools on/off | Orchestrator |
| `prompts/system.md`, `prompts/request.md.tmpl`, `prompts/text-tools.md`, `prompts/notices.md` | prompt text, template, text-loop instructions, the loop's short messages | Orchestrator |
| `api.yaml` | port, privacy, clients, exposed recipes | sysqd client table |
| `notifications.yaml`, `retention.yaml` | per-group notifications; history/summary retention | stores |

sysq side (`~/Library/Application Support/sysq/`): `catalog/`, `recipes/`, `schedules.yaml`, `clients.yaml`, `policy.yaml` (allowed roots, excluded paths, enabled sensitivity classes, sudo primitives). sysact side: `actions/`, `protected_paths.yaml`.

Rule of thumb: if a change is "which recipe, which threshold, which words, which order, which rate", it is YAML. If it is "a new kind of thing the UI can render" or "a new guarantee", it is code.

## 5. Data flow (read path)

```
sysqd schedule ──▶ history.db
      │
GroupStore ──subscribe(recipe)──▶ SysqClient ──HTTP/SSE──▶ sysqd ──▶ engine ──▶ primitives
      │
      ▼ GroupState (immutable value)
ViewModel (applies config: key groups, mode, thresholds→status) ──▶ View
```
Stores publish immutable values; view models are pure functions of `(state, config)`; views are pure functions of view models. This makes snapshot testing trivial and allows the sampling source to change (e.g. a different engine) without touching anything above L3.

## 6. Data flow (mutation path)

```
UI button ─┐
Assistant ─┴▶ ActionStore.prepare(name, params) ──▶ SysactClient.prepare ──▶ plan
                     │
              ConfirmationFlow (one sheet, timed confirm) ──▶ ConfirmationToken
                     │
              ActionStore.execute(plan, token) ──▶ SysactClient.execute ──▶ sysact ──▶ OS
```
`ConfirmationToken` has a non-public initializer owned by `ConfirmationFlow`. The compiler enforces that no code path reaches `execute` without the flow. The delay and wording are config; the existence of the confirmation is code.

## 7. Assistant isolation

- `AssistantOrchestrator` depends on `ChatBackend`, `SysqClientProtocol`, `ActionRequesting` protocols only.
- Prompt construction is a separate pure module (`PromptBuilder`) that takes `(config, scope snapshot, narrative, summaries, transcript)` and returns text; the template lives in a file.
- Tool execution is a separate module that maps tool calls to client calls and enforces budgets; it has no UI dependency.
- Summary history is its own store with its own SQLite table; the orchestrator reads a window from it and never owns it.
- Swapping Ollama for another local runtime = new `ChatBackend` conformance.

## 8. Versioning and compatibility

- sysq HTTP API is versioned (`/v1`); `SysqClient` pins a major version; sysqd reports catalog and recipe versions in `/status`, and Headroom shows a banner when a recipe named in `groups.yaml` is missing (the group renders as unavailable rather than crashing).
- Config files carry a `version:` key; the loader migrates forward with explicit migration functions and keeps a backup.
- `Table` schema changes in a recipe are tolerated by the renderer (columns are discovered, not assumed); view models that need specific columns declare them in `groups.yaml` so a mismatch is a config error, not a runtime surprise.

## 9. Testing seams

- Every protocol in L3 has an in-memory fake; stores are tested with fakes.
- sysq has a `--fixtures` mode that serves recorded tables instead of executing primitives, so Headroom integration tests run on CI machines with no sudo or real system variance.
- sysact has a `--dry-run` mode that returns plans and refuses execution, for UI testing of the confirmation flow.
- Prompt builder tests assert the rendered request against goldens.

## 10. Checklist for a change

| Change | Where |
|---|---|
| New metric in a group | recipe (sysq repo) + `groups.yaml` binding |
| New group | recipes + `groups.yaml` entry; optionally `thresholds.yaml`, `modes.yaml` |
| Different color thresholds | `thresholds.yaml` |
| New mode behavior | `modes.yaml` |
| New action | `actions/<name>.yaml` in sysact; optionally `modes.yaml` to surface it |
| Different model or prompt | `assistant.yaml`, `prompts/` |
| New API client | pairing UI → `clients.yaml` |
| New command backing a metric | primitive YAML + parser (sysq code) |
| New kind of visualization | UI code (L5–L6) |
| New safety guarantee | sysq/sysact code; document in non-negotiables |
