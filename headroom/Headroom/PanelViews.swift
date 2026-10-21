import AppKit
import HeadroomKit
import SwiftUI

/// What the panel holds; reports its size so the panel fits it.
@MainActor
struct PanelRoot: View {
    let model: HeadroomModel
    let ui: UIState
    let windows: WindowManager
    var resized: (CGSize) -> Void

    var body: some View {
        SmallPanelView(model: model, ui: ui, windows: windows)
            .fixedSize()
            .background(GeometryReader { g in
                Color.clear
                    .onAppear { resized(g.size) }
                    .onChange(of: g.size) { _, size in resized(size) }
            })
    }
}

/// The panel that drops down from the menu bar (ui-design §4): header with
/// status and quick switches, mode, key groups 2×2, alert strip, the app's
/// windows as tiles, top consumers, anything left to set up, and the bottom
/// bar (version and Update, open at login, Quit).
@MainActor
struct SmallPanelView: View {
    let model: HeadroomModel
    let ui: UIState
    let windows: WindowManager

    /// Room for the scrolling middle: what the icon's screen leaves under it,
    /// less the header, mode chips, bottom bar and padding (measured).
    @State private var chrome: CGFloat = 170
    var middleMax: CGFloat { max(160, ui.panelMaxHeight - chrome) }

    var body: some View {
        let p = model.panel()
        VStack(alignment: .leading, spacing: 12) {
            header(p)
            modes(p)
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    BannerList(banners: p.banners)
                    if !p.connected && p.allGroups.allSatisfy({ $0.updated == nil }) {
                        ServiceDownCard(model: model)
                    } else {
                        groups(p)
                    }
                    alerts(p)
                    tools
                    consumers(p)
                    setup
                }
            }
            .frame(maxHeight: middleMax)
            .background(GeometryReader { g in Color.clear.preference(key: MiddleHeight.self, value: g.size.height) })
            Divider().opacity(0.5)
            footer
        }
        .padding(16)
        .frame(width: MenuPanel.width)
        .background(GeometryReader { g in Color.clear.preference(key: TotalHeight.self, value: g.size.height) })
        .onPreferenceChange(TotalHeight.self) { total in totalHeight = total; measureChrome() }
        .onPreferenceChange(MiddleHeight.self) { middle in middleHeight = middle; measureChrome() }
    }

    @State private var totalHeight: CGFloat = 0
    @State private var middleHeight: CGFloat = 0

    func measureChrome() {
        guard totalHeight > 0, middleHeight > 0 else { return }
        let c = (totalHeight - middleHeight).rounded()
        if abs(c - chrome) > 1 { chrome = c }
    }

    // MARK: header

    func header(_ p: PanelViewModel) -> some View {
        HStack(spacing: 10) {
            AppMark(colors: markColors(p))
            VStack(alignment: .leading, spacing: 1) {
                Text("Headroom").font(.system(size: 15, weight: .semibold))
                HStack(spacing: 5) {
                    Circle().fill(p.connected ? p.worst.color : Color.gray).frame(width: 7, height: 7)
                    Text(statusText(p)).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer()
            Button { model.setPaused(!p.paused) } label: {
                Image(systemName: p.paused ? "play.fill" : "pause.fill")
            }
            .buttonStyle(IconButton(active: p.paused, tint: Palette.orange))
            .help(p.paused ? "Resume sampling" : "Pause sampling")
            PinButton(model: model, windows: windows)
            Button { windows.fromPanel { windows.showSettings() } } label: { Image(systemName: "gearshape.fill") }
                .buttonStyle(IconButton())
                .help("Settings")
        }
    }

    func markColors(_ p: PanelViewModel) -> [Color] {
        guard p.connected else { return [Color.gray, Color.gray.opacity(0.7)] }
        switch p.worst {
        case .red: return [Palette.red, Palette.pink]
        case .amber: return [Palette.orange, Palette.pink]
        default: return [Palette.teal, Palette.blue]
        }
    }

    func statusText(_ p: PanelViewModel) -> String {
        if !p.connected { return "sysqd isn't running" }
        if p.paused { return "Sampling paused" }
        let alerts = p.allGroups.filter { $0.status.isAlert }
        var text: String
        if alerts.isEmpty {
            text = p.allGroups.contains { $0.updated != nil } ? "All normal" : "Waiting for the first samples…"
        } else {
            text = alerts.prefix(2).map { "\($0.name) \($0.status.label)" }.joined(separator: ", ")
            if alerts.count > 2 { text += " +\(alerts.count - 2)" }
        }
        if p.onBattery { text += " · on battery" }
        return text
    }

    // MARK: mode

    func modes(_ p: PanelViewModel) -> some View {
        HStack(spacing: 6) {
            ForEach(p.modes) { m in
                Button(m.label) { model.setMode(m.id) }
                    .buttonStyle(Chip(selected: m.id == model.mode, tint: modeTint(m)))
                    .help(m.question)
            }
            Spacer(minLength: 0)
        }
    }

    func modeTint(_ m: ModeViewModel) -> Color {
        switch m.tint {
        case "reclaimable": return Palette.teal
        case "warnings": return Palette.orange
        default: return Palette.indigo
        }
    }

    // MARK: groups

    func groups(_ p: PanelViewModel) -> some View {
        LazyVGrid(columns: [GridItem(.flexible(), spacing: 8), GridItem(.flexible(), spacing: 8)], spacing: 8) {
            ForEach(p.keyGroups) { g in
                GroupBoxView(vm: g, compact: true, tint: p.mode?.tint) { windows.fromPanel { windows.showLarge(drillIn: g.id) } }
            }
        }
        .opacity(p.connected ? 1 : 0.55)
    }

    @ViewBuilder
    func alerts(_ p: PanelViewModel) -> some View {
        if !p.alerts.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(p.alerts) { g in
                    Button { windows.fromPanel { windows.showLarge(drillIn: g.id) } } label: {
                        HStack(spacing: 6) {
                            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(g.status.color)
                            Text("\(g.name): \(g.status.label)").fontWeight(.medium)
                            if let m = g.statusMessage { Text(m).foregroundStyle(.secondary).lineLimit(1) }
                            Spacer()
                            Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                        }
                        .font(.system(size: 11))
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(8)
            .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(Color.orange.opacity(0.1)))
        }
    }

    // MARK: windows

    var tools: some View {
        HStack(spacing: 8) {
            ToolTile(title: "Overview", symbol: "square.grid.2x2.fill", colors: [Palette.indigo, Palette.violet]) {
                windows.fromPanel { windows.showLarge() }
            }
            .help("Every group, its consumers and actions (large view)")
            ToolTile(title: "Graphs", symbol: "chart.xyaxis.line", colors: [Palette.blue, Palette.teal]) {
                windows.fromPanel { windows.showGraphs() }
            }
            .help("History of every group")
            ToolTile(title: "Storage", symbol: "externaldrive.fill", colors: [Palette.green, Palette.teal], enabled: model.explorer.available) {
                windows.fromPanel { windows.showExplorer() }
            }
            .help("Storage Explorer: what takes the space")
            ToolTile(title: "Ask AI", symbol: "sparkles", colors: [Palette.violet, Palette.pink], enabled: model.assistant.available) {
                windows.fromPanel { windows.showAssistant(scope: .keyGroups) }
            }
            .help(model.assistant.available ? "Ask the local model how the Mac is doing" : (model.assistant.warning ?? "The assistant isn't available (Ollama not running?)"))
            ToolTile(title: "History", symbol: "clock.arrow.circlepath", colors: [Palette.orange, Palette.pink]) {
                windows.fromPanel { windows.showSummaries() }
            }
            .help("Summary History: the assistant's earlier answers")
        }
    }

    // MARK: consumers

    @ViewBuilder
    func consumers(_ p: PanelViewModel) -> some View {
        let lists = p.keyGroups.filter { !$0.consumers.isEmpty }
        if !lists.isEmpty {
            VStack(alignment: .leading, spacing: 6) {
                SectionTitle("Top consumers")
                ForEach(lists) { g in
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(spacing: 5) {
                            Image(systemName: g.symbol).font(.system(size: 10)).foregroundStyle(.secondary).frame(width: 14)
                            Text(g.name).font(.system(size: 11, weight: .semibold)).foregroundStyle(.secondary)
                        }
                        ForEach(g.consumers) { c in
                            ConsumerRow(vm: c) { inspect(c) }
                        }
                    }
                }
            }
            .padding(10)
            .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Color.primary.opacity(0.04)))
        }
    }

    func inspect(_ c: ConsumerViewModel) {
        guard let id = c.itemID else { return }
        windows.fromPanel { windows.inspect(ItemRef(kind: c.kind ?? "process", id: id, name: c.name, groupID: c.groupID)) }
    }

    // MARK: setup

    @ViewBuilder
    var setup: some View {
        let n = windows.permissions.needsAttention(model.setup)
        if n > 0 {
            Button { windows.fromPanel { windows.showSettings(tab: .permissions) } } label: {
                HStack(spacing: 8) {
                    Image(systemName: "checkmark.shield.fill").foregroundStyle(Palette.blue)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(n == 1 ? "1 thing left to set up" : "\(n) things left to set up").font(.system(size: 12, weight: .medium))
                        Text("Permissions and the powermetrics rule, for every metric").font(.system(size: 10)).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                }
                .padding(10)
                .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Palette.blue.opacity(0.08)))
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
        }
    }

    // MARK: bottom bar

    var footer: some View {
        HStack(spacing: 10) {
            UpdateButton()
            Spacer()
            LaunchAtLoginToggle()
            Button("Quit") { NSApp.terminate(nil) }
                .buttonStyle(PillButton(tint: .secondary, filled: false))
                .keyboardShortcut("q")
        }
    }
}

/// "Open at login" (macOS login items).
@MainActor
struct LaunchAtLoginToggle: View {
    @State private var on = LaunchAtLogin.enabled
    @State private var error: String?

    var body: some View {
        Toggle("Open at login", isOn: Binding(get: { on }, set: { want in
            error = LaunchAtLogin.set(want)
            on = LaunchAtLogin.enabled
        }))
        .toggleStyle(.checkbox)
        .font(.system(size: 11))
        .help(error ?? (LaunchAtLogin.needsApproval ? "macOS needs you to allow Headroom in System Settings → General → Login Items" : "Start Headroom when you log in"))
        .onAppear { on = LaunchAtLogin.enabled }
    }
}

/// Pin: the panel stays open (and on top) when you click elsewhere. Saved to
/// layout.yaml (panel.always_on_top).
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
            .buttonStyle(IconButton(active: on, tint: Palette.indigo))
            .help(on ? "Unpin: close when you click elsewhere" : "Pin: keep the panel open and on top")
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
        // Keyed on the group too: picking another group in the sidebar reuses
        // this view, and its history must not stay on screen under the new name.
        .task(id: "\(vm.id) \(range)") { await loadHistory() }
        .onAppear {
            let ranges = model.config.retention.graph_ranges ?? []
            if !ranges.isEmpty, !ranges.contains(range) { range = ranges[0] }
        }
    }

    func loadHistory() async {
        points = []
        guard let g = model.config.group(vm.id) else { return }
        await model.drillHistory.load([g], range: range)
        guard !Task.isCancelled else { return }
        points = model.drillHistory.series[vm.id]?.points.map(\.1) ?? []
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

private struct TotalHeight: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

private struct MiddleHeight: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}
