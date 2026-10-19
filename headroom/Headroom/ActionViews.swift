import HeadroomKit
import SwiftUI

/// The Actions panel (headroom-spec §7, ui-design §7) for one item.
@MainActor
struct ActionsPanel: View {
    let model: HeadroomModel
    let ui: UIState
    let windows: WindowManager
    let item: ItemRef
    @State private var profile: RecipeRun?

    /// Profile fields by name (first row of each emitted table), used to
    /// prefill action parameters.
    var fields: [String: String] {
        var out: [String: String] = [:]
        for it in profile?.result?.items ?? [] where it.table.count >= 1 {
            for c in it.table.schema where out[c.name] == nil {
                if let v = it.table.value(0, c.name), !v.isNull { out[c.name] = v.string ?? v.description }
            }
        }
        return out
    }

    var body: some View {
        let preferred = item.groupID.map { ViewModels.binding(model.config, mode: model.mode, group: $0).actions } ?? []
        let actions = model.actions.actions(for: item, profile: fields, preferred: preferred)
        let recipes = item.groupID.map { ViewModels.binding(model.config, mode: model.mode, group: $0).recipes } ?? []
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 8) {
                    if let icon = Finder.icon(kind: item.kind, id: item.id, name: item.name) {
                        Image(nsImage: icon).resizable().frame(width: 28, height: 28)
                    }
                    VStack(alignment: .leading) {
                        Text(item.name).font(.headline).lineLimit(2)
                        Text("\(item.kind) \(item.id)").font(.system(size: 11)).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                    Spacer()
                    Button { ui.inspecting = nil } label: { Image(systemName: "xmark.circle.fill") }.buttonStyle(.borderless)
                }
                if let p = profile {
                    RecipeResultView(run: p)
                } else if model.config.groups.item_profile != nil {
                    ProgressView().controlSize(.small)
                }

                Text("SAFE").font(.system(size: 10, weight: .semibold)).foregroundStyle(.secondary)
                HStack {
                    if item.kind == "path" {
                        Button("Reveal") { Finder.reveal(item.id) }
                        Button("Open") { Finder.open(item.id) }
                    }
                    if item.kind == "app" || item.kind == "process" {
                        Button("Activity Monitor") { Finder.activityMonitor() }
                    }
                    Button("Copy \(item.kind == "path" ? "path" : "id")") { Finder.copy(item.id) }
                    Button("Ask AI") { windows.showAssistant(scope: .item(item)) }.disabled(!model.assistant.available)
                }
                .controlSize(.small)

                let caution = actions.filter { $0.0.tier != "risky" }
                let risky = actions.filter { $0.0.tier == "risky" }
                if !caution.isEmpty {
                    Text("CAUTION").font(.system(size: 10, weight: .semibold)).foregroundStyle(.orange)
                    ForEach(caution.indices, id: \.self) { i in ActionCatalogRow(model: model, info: caution[i].0, fill: caution[i].1) }
                }
                if !risky.isEmpty {
                    Text("RISKY").font(.system(size: 10, weight: .semibold)).foregroundStyle(.red)
                    ForEach(risky.indices, id: \.self) { i in ActionCatalogRow(model: model, info: risky[i].0, fill: risky[i].1) }
                }
                if let e = model.actions.catalogError { Text("sysact: \(e)").font(.system(size: 11)).foregroundStyle(.secondary) }

                if !recipes.isEmpty {
                    Text("RECIPES").font(.system(size: 10, weight: .semibold)).foregroundStyle(.secondary)
                    ForEach(recipes, id: \.self) { r in RecipeRow(model: model, name: r, params: [:]) }
                }
            }
            .padding(14)
        }
        .background(VisualEffect(material: .contentBackground))
        .task(id: item) {
            guard let name = model.config.groups.item_profile else { return }
            profile = await model.recipes.run(name, params: ["kind": .string(item.kind), "id": .string(item.id)])
        }
    }
}

/// A read-only recipe: doc, derived cost/sensitivity, Run, Copy as CLI, Explain.
@MainActor
struct RecipeRow: View {
    let model: HeadroomModel
    let name: String
    let params: [String: JSONValue]
    @State private var showExplain = false
    @State private var plan: ExplainPlan?

    var body: some View {
        let entry = model.recipes.entry(name)
        let run = model.recipes.runs[RecipeStore.runKey(name, params)]
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: "play.fill").font(.system(size: 9)).foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 1) {
                    Text(name).font(.system(size: 12, design: .monospaced))
                    if let d = entry?.doc { Text(d).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(2) }
                }
                Spacer()
                if let c = entry?.cost { Badge(text: c) }
                if let s = entry?.sensitivity { Badge(text: s) }
                Button("Run") { Task { await model.recipes.run(name, params: params) } }
                    .disabled(entry?.available == false)
                    .help(entry?.reason ?? "Runs now; read-only")
                Button { Finder.copy(RecipeStore.cli(name, params: params)) } label: { Image(systemName: "terminal") }
                    .help("Copy as CLI: \(RecipeStore.cli(name, params: params))")
                Button { showExplain = true; Task { plan = await model.recipes.explain(name) } } label: { Image(systemName: "questionmark.circle") }
                    .help("Explain")
            }
            .buttonStyle(.borderless)
            .controlSize(.small)
            if let run {
                RecipeResultView(run: run)
                    .padding(.leading, 18)
            }
        }
        .sheet(isPresented: $showExplain) {
            ExplainSheet(name: name, plan: plan) { showExplain = false }
        }
    }
}

@MainActor
struct Badge: View {
    var text: String
    var body: some View {
        Text(text).font(.system(size: 9, weight: .medium))
            .padding(.horizontal, 5).padding(.vertical, 1)
            .background(Capsule().fill(Color.secondary.opacity(0.15)))
    }
}

@MainActor
struct ExplainSheet: View {
    var name: String
    var plan: ExplainPlan?
    var close: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Explain \(name)").font(.headline)
            if let p = plan {
                Text("cost \(p.cost) (\(p.cost_units ?? 0) units) · sensitivity \(p.sensitivity) · primitives \((p.primitives ?? []).joined(separator: ", "))")
                    .font(.system(size: 11)).foregroundStyle(.secondary)
                if let push = p.pushdown, !push.isEmpty { Text("pushdown: \(push.joined(separator: "; "))").font(.system(size: 11)) }
                ScrollView {
                    Text(p.lowered).font(.system(size: 11, design: .monospaced)).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            } else {
                ProgressView()
            }
            HStack { Spacer(); Button("Done", action: close).keyboardShortcut(.defaultAction) }
        }
        .padding(16)
        .frame(width: 560, height: 420)
    }
}

/// A sysact action row. Its button only ever opens the confirmation sheet.
@MainActor
struct ActionCatalogRow: View {
    let model: HeadroomModel
    let info: ActionInfo
    let fill: [String: String]
    @State private var values: [String: String] = [:]
    @State private var editing = false

    var body: some View {
        let risky = info.tier == "risky"
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill").font(.system(size: 10)).foregroundStyle(risky ? Color.red : Color.orange)
                VStack(alignment: .leading, spacing: 1) {
                    Text(info.name).font(.system(size: 12, design: .monospaced))
                    Text(info.doc + (info.requires_admin ? " (admin)" : "")).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(2)
                }
                Spacer()
                if !info.orderedParams.isEmpty {
                    Button { editing.toggle() } label: { Image(systemName: "slider.horizontal.3") }.buttonStyle(.borderless).help("Parameters")
                }
                Button("Confirm…") { start() }
                    .controlSize(.small)
                    .tint(risky ? Color.red : Color.orange)
                    .buttonStyle(.bordered)
                    .disabled(model.actions.busy != nil || !missing.isEmpty)
                    .help(missing.isEmpty ? info.effect : "Needs \(missing.joined(separator: ", "))")
            }
            if editing || !missing.isEmpty, !info.orderedParams.isEmpty {
                ForEach(info.orderedParams, id: \.self) { p in
                    HStack {
                        Text(p).font(.system(size: 11)).frame(width: 80, alignment: .trailing)
                        TextField(info.params?[p]?.doc ?? p, text: Binding(get: { values[p] ?? fill[p] ?? "" }, set: { values[p] = $0 }))
                            .textFieldStyle(.roundedBorder).controlSize(.small)
                    }
                }
            }
        }
        .padding(8)
        .background(RoundedRectangle(cornerRadius: 8).fill((risky ? Color.red : Color.orange).opacity(0.06)))
    }

    var current: [String: String] { fill.merging(values) { $1 } }

    var missing: [String] {
        (info.params ?? [:]).filter { $0.value.required == true && (current[$0.key] ?? "").isEmpty }.keys.sorted()
    }

    func start() {
        var params: [String: JSONValue] = [:]
        for (k, v) in current where !v.isEmpty {
            let type = info.params?[k]?.type
            if type == "int" || type == "pid" || type == "float", let d = Double(v) { params[k] = .number(d) } else { params[k] = .string(v) }
        }
        Task { _ = await model.actions.run(info.name, params: params, requestedBy: .user) }
    }
}

/// The one confirmation sheet (ui-design §8): exact action, params, effect,
/// tier, undo, who asked, the model's explanation; Cancel is the default and
/// Confirm stays disabled for the configured delay.
@MainActor
struct ConfirmationSheet: View {
    let flow: ConfirmationFlow
    let catalog: ActionStore
    @State private var now = Date()
    private let tick = Timer.publish(every: 0.1, on: .main, in: .common).autoconnect()

    var body: some View {
        if let req = flow.current {
            let p = req.plan
            let remaining = max(0, req.enablesAt.timeIntervalSince(now))
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(p.tier == "risky" ? Color.red : Color.orange)
                    Text(p.name).font(.system(size: 16, weight: .semibold, design: .monospaced))
                    Badge(text: p.tier.uppercased())
                    if p.requires_admin { Badge(text: "ADMIN") }
                }
                Text(p.effect).font(.system(size: 13)).fixedSize(horizontal: false, vertical: true)
                if !p.params.isEmpty {
                    Grid(alignment: .leading, horizontalSpacing: 10, verticalSpacing: 3) {
                        ForEach(p.params.keys.sorted(), id: \.self) { k in
                            GridRow {
                                Text(k).foregroundStyle(.secondary)
                                Text(p.params[k] ?? "").textSelection(.enabled)
                            }
                        }
                    }
                    .font(.system(size: 12, design: .monospaced))
                }
                if let u = p.undo_hint { Label(u, systemImage: "arrow.uturn.backward").font(.system(size: 12)) }
                ForEach(p.warnings ?? [], id: \.self) { w in Label(w, systemImage: "exclamationmark.circle").font(.system(size: 12)).foregroundStyle(.orange) }
                if req.requestedBy == .assistant {
                    Divider()
                    Text("Requested by assistant").font(.system(size: 11, weight: .semibold)).foregroundStyle(.secondary)
                    if let e = req.explanation, !e.isEmpty { Text(e).font(.system(size: 12)).fixedSize(horizontal: false, vertical: true) }
                }
                if p.dry_run == true { Text("sysact is in dry-run mode: nothing will change.").font(.system(size: 11)).foregroundStyle(.secondary) }
                if p.requires_admin { Text("macOS will ask for an administrator password.").font(.system(size: 11)).foregroundStyle(.secondary) }
                Spacer(minLength: 4)
                HStack {
                    Spacer()
                    Button("Cancel", role: .cancel) { flow.decline() }
                        .keyboardShortcut(.defaultAction)
                    Button {
                        flow.confirm()
                    } label: {
                        HStack(spacing: 6) {
                            if remaining > 0 {
                                ZStack {
                                    Circle().stroke(Color.secondary.opacity(0.3), lineWidth: 2)
                                    Circle().trim(from: 0, to: CGFloat(1 - remaining / req.delay)).stroke(Color.accentColor, lineWidth: 2).rotationEffect(.degrees(-90))
                                }
                                .frame(width: 12, height: 12)
                            }
                            Text("Confirm")
                        }
                    }
                    .tint(p.tier == "risky" ? Color.red : Color.orange)
                    .buttonStyle(.borderedProminent)
                    .disabled(remaining > 0 || !flow.confirmEnabled)
                }
            }
            .padding(20)
            .frame(width: 460)
            .onExitCommand { flow.decline() }
            .onReceive(tick) { now = $0 }
        } else {
            Color.clear.frame(width: 1, height: 1)
        }
    }
}
