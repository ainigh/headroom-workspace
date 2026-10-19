import HeadroomKit
import SwiftUI

/// The floating panel (ui-design §4): key groups 2×2, alert strip, grouped
/// top consumers.
@MainActor
struct SmallPanelView: View {
    let model: HeadroomModel
    let ui: UIState
    let windows: WindowManager

    var body: some View {
        let p = model.panel()
        VStack(spacing: 0) {
            header(p)
            Divider()
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    BannerList(banners: p.banners)
                    if !p.connected && p.allGroups.allSatisfy({ $0.updated == nil }) {
                        ServiceDownCard(model: model)
                    }
                    LazyVGrid(columns: [GridItem(.flexible(), spacing: 8), GridItem(.flexible(), spacing: 8)], spacing: 8) {
                        ForEach(p.keyGroups) { g in
                            GroupBoxView(vm: g, compact: true, tint: p.mode?.tint) { windows.showLarge(drillIn: g.id) }
                        }
                    }
                    .opacity(p.connected ? 1 : 0.55)
                    ForEach(p.alerts) { g in
                        Button { windows.showLarge(drillIn: g.id) } label: {
                            HStack(spacing: 6) {
                                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(g.status.color)
                                Text("\(g.name): \(g.status.label)")
                                if let m = g.statusMessage { Text(m).foregroundStyle(.secondary).lineLimit(1) }
                                Spacer()
                            }
                            .font(.system(size: 11))
                        }
                        .buttonStyle(.plain)
                    }
                    Divider()
                    ForEach(p.keyGroups) { g in
                        if !g.consumers.isEmpty {
                            Text(g.name.uppercased()).font(.system(size: 10, weight: .semibold)).foregroundStyle(.secondary)
                            ForEach(g.consumers) { c in
                                ConsumerRow(vm: c) { inspect(c) }
                            }
                        }
                    }
                }
                .padding(12)
            }
        }
        .frame(width: 380)
        .frame(minHeight: 300)
        .background(VisualEffect())
    }

    func header(_ p: PanelViewModel) -> some View {
        HStack(spacing: 8) {
            StatusDot(status: p.worst)
            Text("Headroom").font(.system(size: 13, weight: .semibold))
            if p.paused || p.onBattery {
                Image(systemName: p.paused ? "pause.circle" : "clock").foregroundStyle(.secondary)
                    .help(p.paused ? "Sampling paused" : "On battery: sampling slower")
            }
            Spacer()
            ModePicker(model: model).frame(width: 150)
            Button { windows.showLarge() } label: { Image(systemName: "arrow.up.left.and.arrow.down.right") }.help("Large view")
            Button { windows.showGraphs() } label: { Image(systemName: "chart.xyaxis.line") }.help("Graphs")
            Button { windows.showExplorer() } label: { Image(systemName: "externaldrive") }.help("Storage Explorer")
            Button { windows.showAssistant(scope: .keyGroups) } label: { Image(systemName: "sparkles") }.help("Ask AI")
                .disabled(!model.assistant.available)
            PinButton(model: model, windows: windows)
        }
        .buttonStyle(.borderless)
        .padding(.horizontal, 12)
        .padding(.top, 10)
        .padding(.bottom, 8)
    }

    func inspect(_ c: ConsumerViewModel) {
        guard let id = c.itemID else { return }
        windows.inspect(ItemRef(kind: c.kind ?? "process", id: id, name: c.name, groupID: c.groupID))
    }
}

/// Always-on-top toggle, saved to layout.yaml.
@MainActor
struct PinButton: View {
    let model: HeadroomModel
    let windows: WindowManager
    var body: some View {
        let on = model.config.layout.panel?.always_on_top ?? false
        Button {
            windows.setAlwaysOnTop(!on)
            _ = model.setAlwaysOnTop(!on)
        } label: { Image(systemName: on ? "pin.fill" : "pin") }
            .help(on ? "Unpin" : "Keep on top")
    }
}

/// The large view (ui-design §5) and the drill-in (§6) in one window.
@MainActor
struct LargeView: View {
    let model: HeadroomModel
    let ui: UIState
    let windows: WindowManager
    @State private var overview: String?
    @State private var collapsed: Set<String> = []

    var body: some View {
        let p = model.panel(consumerRows: model.modeViewModel?.consumerCount ?? 5)
        NavigationSplitView {
            // "" selects the all-groups view (List tags must match the selection type).
            List(selection: Binding<String?>(get: { ui.drillIn ?? "" }, set: { ui.drillIn = ($0 ?? "").isEmpty ? nil : $0 })) {
                Label("All groups", systemImage: "square.grid.2x2").tag("")
                Section("Groups") {
                    ForEach(p.allGroups) { g in
                        HStack {
                            Label(g.name, systemImage: g.symbol)
                            Spacer()
                            StatusDot(status: g.status)
                        }
                        .tag(g.id)
                    }
                }
            }
            .navigationSplitViewColumnWidth(min: 170, ideal: 190)
        } detail: {
            HStack(spacing: 0) {
                Group {
                    if let id = ui.drillIn, let vm = model.groupViewModel(id) {
                        DrillInView(model: model, ui: ui, windows: windows, vm: vm)
                    } else {
                        allGroups(p)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                if let item = ui.inspecting {
                    Divider()
                    ActionsPanel(model: model, ui: ui, windows: windows, item: item)
                        .frame(width: 340)
                        .id(item)
                }
            }
            .toolbar {
                ToolbarItem { ModePicker(model: model).frame(width: 280) }
                ToolbarItem {
                    Button { windows.showAssistant(scope: ui.drillIn.map { .group($0) } ?? .allGroups) } label: { Label("Ask AI", systemImage: "sparkles") }
                        .disabled(!model.assistant.available)
                }
            }
        }
        .task { await loadOverview() }
    }

    func loadOverview() async {
        guard let name = model.config.groups.overview else { return }
        let r = await model.recipes.run(name)
        let t = r.result?.table
        overview = t.flatMap { t in ["sentence", "summary", "headline", "status"].lazy.compactMap { t.string(0, $0) }.first }
    }

    func allGroups(_ p: PanelViewModel) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 10) {
                    StatusDot(status: p.worst, size: 10)
                    Text(overview ?? "Headroom").font(.title3.weight(.semibold))
                    Spacer()
                    Button { Task { await loadOverview() }; model.groups.refreshAll() } label: { Image(systemName: "arrow.clockwise") }
                        .buttonStyle(.borderless).help("Refresh")
                }
                BannerList(banners: p.banners)
                if !p.connected { ServiceDownCard(model: model) }
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 12), count: 3), spacing: 12) {
                    ForEach(p.allGroups) { g in
                        GroupBoxView(vm: g, compact: false, tint: p.mode?.tint) { ui.drillIn = g.id }
                            .contextMenu {
                                Button(g.isKey ? "Remove from key groups" : "Make a key group") { toggleKey(g.id) }
                                Button("Move earlier") { move(g.id, by: -1, p) }
                                Button("Move later") { move(g.id, by: 1, p) }
                                Button("Graph") { windows.showGraphs([g.id]) }
                            }
                    }
                }
                if p.mode?.rankBy == "reclaimable" {
                    let total = p.allGroups.compactMap(\.reclaimableValue).reduce(0, +)
                    if total > 0 { Text("Reclaim up to \(Units.bytes(total))").font(.headline).foregroundStyle(.teal) }
                }
                ForEach(p.allGroups) { g in
                    if !g.consumers.isEmpty {
                        DisclosureGroup(isExpanded: Binding(get: { !collapsed.contains(g.id) }, set: { open in
                            if open { collapsed.remove(g.id) } else { collapsed.insert(g.id) }
                        })) {
                            VStack(spacing: 2) {
                                ForEach(g.consumers) { c in
                                    ConsumerRow(vm: c) {
                                        if let id = c.itemID { ui.inspecting = ItemRef(kind: c.kind ?? "process", id: id, name: c.name, groupID: g.id) }
                                    }
                                }
                            }
                        } label: {
                            Label(g.name, systemImage: g.symbol).font(.system(size: 12, weight: .semibold))
                        }
                    }
                }
            }
            .padding(18)
        }
    }

    func toggleKey(_ id: String) {
        var keys = model.keyIDs
        if let i = keys.firstIndex(of: id) { keys.remove(at: i) } else { keys = Array((keys + [id]).suffix(4)) }
        _ = model.setPinned(keys)
    }

    func move(_ id: String, by d: Int, _ p: PanelViewModel) {
        var order = model.config.orderedGroups.map(\.id)
        guard let i = order.firstIndex(of: id) else { return }
        let j = max(0, min(order.count - 1, i + d))
        order.swapAt(i, j)
        _ = model.setGroupOrder(order)
    }
}

/// One group in full (ui-design §6).
@MainActor
struct DrillInView: View {
    let model: HeadroomModel
    let ui: UIState
    let windows: WindowManager
    let vm: GroupViewModel
    @State private var range = "5m"
    @State private var points: [Double] = []

    var body: some View {
        let st = model.groups.state(vm.id)
        let binding = ViewModels.binding(model.config, mode: model.mode, group: vm.id)
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 10) {
                    Button { ui.drillIn = nil } label: { Label("All groups", systemImage: "chevron.left") }.buttonStyle(.borderless)
                    Image(systemName: vm.symbol)
                    Text(vm.name).font(.title2.weight(.semibold))
                    StatusDot(status: vm.status, size: 10)
                    Text([vm.headline, vm.headlineLabel].compactMap { $0 }.joined(separator: " ")).font(.title3).monospacedDigit()
                    Spacer()
                    if model.config.groups.explorer?.group == vm.id {
                        Button("Storage Explorer") { windows.showExplorer() }
                    }
                }
                if let m = vm.statusMessage { Text(m).foregroundStyle(.secondary) }
                HStack(alignment: .top, spacing: 14) {
                    GroupBox("Metrics") {
                        if let t = st?.detail?.table { MetricsList(table: t) } else { ProgressView().controlSize(.small) }
                    }
                    .frame(minWidth: 260, maxWidth: 340)
                    GroupBox {
                        Sparkline(values: points.isEmpty ? vm.sparkline : points, color: vm.status == .unknown ? .accentColor : vm.status.color)
                            .frame(height: 120)
                    } label: {
                        HStack {
                            Text("History")
                            Spacer()
                            Picker("Range", selection: $range) {
                                ForEach(model.config.retention.graph_ranges ?? ["5m", "1h", "24h", "7d"], id: \.self) { Text($0).tag($0) }
                            }
                            .pickerStyle(.segmented).labelsHidden().frame(width: 200)
                            Button("Graphs") { windows.showGraphs([vm.id]) }.buttonStyle(.borderless)
                        }
                    }
                }
                GroupBox("Consumers") {
                    if let t = st?.consumers?.table {
                        SysqTableView(table: t, maxHeight: 300) { shown, row in inspectRow(shown, row) }
                    } else {
                        Text("No consumer data yet").foregroundStyle(.secondary)
                    }
                }
                if !binding.recipes.isEmpty || !binding.actions.isEmpty {
                    GroupBox("Recipes & actions (\(model.modeViewModel?.label ?? model.mode))") {
                        VStack(alignment: .leading, spacing: 8) {
                            ForEach(binding.recipes, id: \.self) { r in RecipeRow(model: model, name: r, params: [:]) }
                            ForEach(binding.actions, id: \.self) { a in
                                if let info = model.actions.action(a) { ActionCatalogRow(model: model, info: info, fill: [:]) }
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
            .padding(18)
        }
        .task(id: range) { await loadHistory() }
    }

    func loadHistory() async {
        guard let g = model.config.group(vm.id) else { return }
        await model.history.load([g], range: range)
        points = model.history.series[vm.id]?.points.map(\.1) ?? []
    }

    func inspectRow(_ t: SysqTable, _ row: Int) {
        guard let g = model.config.group(vm.id), let idCol = g.consumers.id, let v = t.value(row, idCol) else { return }
        let name = t.nameColumn(preferred: g.consumers.name).map { t.text(row, $0) } ?? ""
        ui.inspecting = ItemRef(kind: g.consumers.kind ?? "process", id: v.string ?? v.description, name: name, groupID: vm.id)
    }
}

/// Every column of a one-row detail table, as label/value lines.
@MainActor
struct MetricsList: View {
    var table: SysqTable
    var body: some View {
        if table.count == 1 {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 4) {
                ForEach(table.schema, id: \.name) { c in
                    GridRow {
                        Text(c.name.replacingOccurrences(of: "_", with: " ").capitalized).foregroundStyle(.secondary)
                        Text(table.text(0, c.name)).monospacedDigit().textSelection(.enabled).gridColumnAlignment(.trailing)
                    }
                    .font(.system(size: 12))
                }
            }
        } else {
            SysqTableView(table: table, maxHeight: 220)
        }
    }
}
