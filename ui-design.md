# Headroom — UI Design Proposal

References `headroom-spec.md` for behavior. This document proposes the look, layout, and interaction model. Visual polish is the developer's; structure, hierarchy, and the interaction rules here are intended.

---

## 1. Design principles

1. **Glanceable first.** The small view must answer "how is my Mac?" in under a second without reading. Color and shape carry the status; numbers confirm it.
2. **Same shape at every depth.** A group box looks the same in the small view, the large view, and the drill-in header. Drilling in adds, never rearranges.
3. **Reading is instant, changing is deliberate.** Read-only recipes run on click with no friction. Mutations always look different (distinct button style) and always confirm.
4. **The data is tables; show tables.** Recipe outputs render as compact sortable tables, not prose. Columns carry units.
5. **Native and quiet.** System fonts, vibrancy, menu-bar conventions, dark/light following the system. No branding in the chrome.
6. **Config, not code.** Every group, mode, and threshold rendered comes from YAML; the UI has no hard-coded group names.

## 2. Visual language

- **Typography:** SF Pro; headline figures in SF Pro Rounded, tabular numerals everywhere numbers change.
- **Status colors:** system green / orange / red at reduced saturation for fills; full saturation only for the status dot and sparkline line. Grey for unavailable (sudo primitive not enabled).
- **Surfaces:** `NSVisualEffectView` sidebar material for panels; 12 pt corner radius on boxes; 1 px hairline separators.
- **Icons:** SF Symbols per group, set in `groups.yaml` (`memorychip`, `cpu`, `gpu`? → `sparkles` for GPU/ANE, `internaldrive`, `network`, `battery.75`, `thermometer`, `brain`).
- **Density:** small view compact (11–12 pt), large view regular (13 pt), drill-in regular with room for tables.

## 3. Menu bar item

- Icon plus an optional readout chosen in settings (e.g. memory available, or the worst group's headline). Readout text turns amber/red with status.
- Left click: toggle the small view. Right click: menu with mode switch, Open Large View, Graphs, Storage Explorer, Ask AI, Pause sampling, Settings, Quit.

## 4. Small view (floating panel)

```
┌──────────────────────────────────────────────────────┐
│ ● Headroom          [View ▾]      ⤢  📈  🗂  ✦AI  📌 │
├──────────────────────────────────────────────────────┤
│ ┌────────────────────┐ ┌────────────────────┐        │
│ │ ▮ Memory      ●    │ │ ▮ CPU         ●    │        │
│ │ 18.2 GB available  │ │ 34 %               │        │
│ │ ▁▂▃▅▆▇▆▅▃ pressure │ │ ▂▃▂▅▇▆▃▂▁ P 41 E 12│        │
│ │ swap 2.1 GB        │ │ load 4.2           │        │
│ └────────────────────┘ └────────────────────┘        │
│ ┌────────────────────┐ ┌────────────────────┐        │
│ │ ▮ GPU & ANE   ●    │ │ ▮ Storage     ●    │        │
│ │ 62 %               │ │ 91 GB free         │        │
│ │ ▃▅▆▇▇▆▅▅▆ 12.4 GB  │ │ ▇▇▇▇▇▇▇▆▆ purge 14G│        │
│ └────────────────────┘ └────────────────────┘        │
│ ▲ Thermals: amber                                    │
├──────────────────────────────────────────────────────┤
│ MEMORY                                               │
│   ollama (gemma-27b)      22.0 GB  ████████░░  ⋯     │
│   Google Chrome            5.1 GB  ██░░░░░░░░  ⋯     │
│   Xcode                    3.8 GB  █░░░░░░░░░  ⋯     │
│ CPU                                                  │
│   photoanalysisd            180 %  █████████░  ⋯     │
│   …                                                  │
└──────────────────────────────────────────────────────┘
```

- Fixed width (≈ 380 pt), height grows with consumer lists up to a cap, then scrolls.
- **Group box anatomy:** icon + name, status dot top-right, headline figure large, sparkline (last 5 min) with one supporting metric to its right, second supporting metric beneath. Hover reveals a subtle "Open" affordance; click drills in.
- **Consumer rows:** name (with app icon when resolvable), value with unit, proportional bar, `⋯` opens the Actions panel. Rows are keyboard-navigable.
- **Non-key alert strip** appears only when a hidden group is amber/red; click jumps to its drill-in.
- Mode switch is a segmented control in the header; it recolors the panel subtly (Optimize tints reclaimable values; Repair shows warning glyphs).
- Pin toggle sets always-on-top; drag anywhere on the header.

## 5. Large view

- Resizable window, minimum 720 × 520.
- Header: overall health sentence from `health.overview` ("Healthy — memory tight"), power/thermal chips, mode switch, Ask AI.
- Grid of all group boxes (same component), 3 per row; drag to reorder; a star marks key groups (max four).
- Below the grid: consumer sections per group, collapsible; in Optimize mode groups sort by reclaimable amount and show a "Reclaim up to 24 GB" caption.
- Sidebar (collapsible) listing groups with status dots for quick jump.

## 6. Drill-in view

```
┌ ← All groups      ▮ Memory   ●  18.2 GB available        [View ▾]  ✦AI ┐
│ ┌─ Metrics ───────────────┐ ┌─ History ── 5m 1h 24h 7d ───────────────┐ │
│ │ Used        45.8 GB     │ │  ▁▂▃▅▆▇▆▅▃▂▁▂▃▅▆▇▇▆▅▃▂                  │ │
│ │ Pressure    Critical    │ │  markers: ▲ ollama run  ▲ pressure red  │ │
│ │ Swap         6.1 GB     │ └─────────────────────────────────────────┘ │
│ │ Compressed   9.2 GB     │                                             │
│ └─────────────────────────┘                                             │
│ ┌─ Consumers ── sort: memory ▾  filter ▭ ────────────────────────────┐  │
│ │ Name              Memory   Inactive   CPU   Uptime   ⋯             │  │
│ │ ollama (gemma)    22.0 GB  22.0 GB    0 %   2 h 10   ⋯             │  │
│ └────────────────────────────────────────────────────────────────────┘  │
│ ┌─ Recipes & actions (Optimize) ─────────────────────────────────────┐  │
│ │ ▶ mem.inactive_holders   Apps holding inactive memory   [Run][CLI] │  │
│ │ ⚠ mem.purge              Free inactive memory (admin)   [Confirm…] │  │
│ └────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

- Metrics panel lists every column of `<group>.detail` with units; Deep Dive adds raw values and a "Copy as JSON".
- Consumers table: sortable columns, filter field, right-click → Actions panel.
- Recipes & actions list is mode-dependent; recipe rows have a `▶` (run, read-only) and show results inline in an expandable table; action rows have a warning glyph and a `Confirm…` button styled as a destructive-capable control.

## 7. Actions panel (inspector)

Slides in from the right (large/drill-in) or replaces the consumer list with a back button (small view).

- **Header:** app icon, name, bundle id, pid, uptime.
- **Usage strip:** one chip per group where the item consumes anything (memory 22 GB · CPU 0 % · GPU 11 GB · energy low).
- **Mini graph:** item's history (5 m) for the current group.
- **Actions, three visually distinct groups:**
  - Safe — plain buttons in a row (Reveal, Info, Copy path, Activity Monitor).
  - Caution — bordered buttons with an orange leading glyph.
  - Risky — red-tinted buttons; never in the first row; never default-focused.
- **Recipes:** list with doc line, derived cost/sensitivity badge, `Run`, `Copy as CLI`, `Explain` (opens a sheet with lowered Starlark and plan).
- Inline results render as the standard table component with a "Open in drill-in" link.

## 8. Confirmation sheets (mutations)

- One sheet titled with the action name; body shows the effect statement from the catalog, the params, the tier badge, and the undo note. When assistant-originated it adds a "Requested by assistant" line and the model's explanation. The Confirm button shows a small countdown ring and is disabled until the configured delay elapses (default 2 s). Buttons: Cancel (default) / Confirm.
- Admin actions then show the macOS authorization dialog.
- Result toast with output snippet and "Show in log".

## 9. AI assistant panel

- Opens as a side panel (large/drill-in) or sheet (small view). Header shows scope and mode chips, model name, status badge from STATUS.
- **Investigation trace:** collapsed by default; expands to a list of sysq calls with row counts and a small table preview each. This is the trust layer — the user can see exactly what the model looked at.
- **Response:** SUMMARY as a bold line; MESSAGE as rich text where recipe and action names render as chips that open the matching panel; ACTION REQUEST as a card with a Confirm… button that opens the confirmation sheet.
- Input field at the bottom for follow-ups; Stop button while a turn is running; fuel meter as a thin progress bar.
- Summary History opens in a separate window: list with timestamp, status dot, mode, scope, action run; search and filters; multi-select delete; "delete before date" in the toolbar.

## 10. Graphs panel

- Left: group checklist with color swatches (key groups preselected). Right: one chart per selected group stacked, or a single overlaid chart (toggle). Range control top-right.
- Hover crosshair with values for all series; event markers as small triangles with tooltips; click a point → popover "Top consumers at 3:14 pm" from `history.consumers_at`.

## 11. Storage Explorer

- Top: volume bar (used / purgeable / free) per volume.
- Toolbar: root picker, Files/Folders toggle, Group by type, filters (age, min size), List/Blocks toggle.
- List: name, size, type, last opened, location; row actions Reveal, Preview, `Trash…`.
- Blocks view: treemap of the current list; click to zoom; breadcrumb above.
- Optimize mode adds a "Suggestions" section (stale caches, old downloads, unused simulators, stale AI models), each with "Show items" and a per-item Trash, never a bulk destructive button.

## 12. Settings

- Standard macOS tabbed settings: General, Groups, Modes, Thresholds, Assistant, API, Notifications, Advanced.
- Every tab edits a YAML file and shows "Edit file…" to open it in the default editor; changes made externally appear live.
- Advanced shows sysqd/sysact status, enabled sensitivity classes, sudo-enabled primitives, and an "Open audit log" button.

## 13. States

- **Unavailable metric** (e.g. fan RPM without sudo): grey value with a tooltip explaining how to enable.
- **Sampling paused / on battery:** small clock glyph in the header.
- **sysqd not running:** panel shows a single card with "Start sysqd" and the last-known data dimmed.
- **Config error:** non-blocking banner naming the file and line.

## 14. Accessibility and input

- Full keyboard navigation in all lists; VoiceOver labels include unit and status ("Memory, 18.2 gigabytes available, status amber").
- Respect Reduce Motion (no sparkline animation) and Increase Contrast (solid status fills).
- Global hotkey to toggle the small view.
