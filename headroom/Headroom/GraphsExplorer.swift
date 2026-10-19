import Charts
import HeadroomKit
import SwiftUI

struct ChartSeries: Identifiable {
    let id: String
    let group: GroupConfig
    let pts: [ChartPoint]
}

struct ChartPoint: Identifiable {
    let id: Int
    let date: Date
    let value: Double
    let series: String
}

/// Graphs panel (headroom-spec §10, ui-design §10).
@MainActor
struct GraphsView: View {
    let model: HeadroomModel
    let ui: UIState
    @State private var range = "1h"
    @State private var overlay = false
    @State private var selected: Date?
    @State private var consumersFor: String?

    var ranges: [String] { model.config.retention.graph_ranges ?? ["5m", "1h", "24h", "7d"] }

    var body: some View {
        let groups = model.config.orderedGroups.filter { ui.graphsSelection.contains($0.id) }
        HStack(spacing: 0) {
            List {
                ForEach(model.config.orderedGroups) { g in
                    Toggle(isOn: Binding(get: { ui.graphsSelection.contains(g.id) }, set: { on in
                        if on { ui.graphsSelection.insert(g.id) } else { ui.graphsSelection.remove(g.id) }
                    })) {
                        Label(g.name, systemImage: g.symbol)
                    }
                }
            }
            .frame(width: 200)
            Divider()
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Toggle("Overlay", isOn: $overlay).toggleStyle(.switch).controlSize(.small)
                    if model.history.loading { ProgressView().controlSize(.small) }
                    Spacer()
                    Picker("Range", selection: $range) { ForEach(ranges, id: \.self) { Text($0).tag($0) } }
                        .pickerStyle(.segmented).labelsHidden().frame(width: 240)
                }
                if let e = model.history.error { Text(e).foregroundStyle(.secondary) }
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        if overlay {
                            chart(groups, normalized: true).frame(height: 300)
                        } else {
                            ForEach(groups) { g in
                                VStack(alignment: .leading, spacing: 4) {
                                    HStack {
                                        Label(g.name, systemImage: g.symbol).font(.system(size: 12, weight: .semibold))
                                        Spacer()
                                        if let d = selected {
                                            Button("Top consumers at \(d.formatted(date: .omitted, time: .shortened))") {
                                                consumersFor = g.id
                                                Task { await model.history.consumers(group: g.id, at: d) }
                                            }
                                            .buttonStyle(.borderless).font(.system(size: 11))
                                        }
                                    }
                                    chart([g], normalized: false).frame(height: 150)
                                }
                            }
                        }
                        if let c = model.history.consumersAt, consumersFor == c.group {
                            GroupBox("Top consumers at \(c.at.formatted(date: .abbreviated, time: .shortened)) — \(model.config.group(c.group)?.name ?? c.group)") {
                                SysqTableView(table: c.table, maxHeight: 260)
                            }
                        }
                    }
                }
            }
            .padding(14)
        }
        .task(id: "\(range)|\(ui.graphsSelection.sorted().joined(separator: ","))") {
            await model.history.load(groups, range: range)
        }
    }

    func points(_ g: GroupConfig, normalized: Bool) -> [ChartPoint] {
        let pts = model.history.series[g.id]?.points ?? []
        let lo = pts.map(\.1).min() ?? 0, hi = pts.map(\.1).max() ?? 1
        let span = hi - lo == 0 ? 1 : hi - lo
        return pts.enumerated().map { i, p in
            ChartPoint(id: i, date: p.0, value: normalized ? (p.1 - lo) / span * 100 : p.1, series: g.name)
        }
    }

    func chart(_ groups: [GroupConfig], normalized: Bool) -> some View {
        let series = groups.map { ChartSeries(id: $0.id, group: $0, pts: points($0, normalized: normalized)) }
        let events = model.history.events
        let yLabel: String = normalized ? "Relative %" : "Value"
        return Chart {
            ForEach(series) { s in
                ForEach(s.pts) { p in
                    LineMark(x: .value("Time", p.date), y: .value(yLabel, p.value))
                        .foregroundStyle(by: .value("Group", s.group.name))
                        .interpolationMethod(.monotone)
                }
            }
            ForEach(events) { e in
                RuleMark(x: .value("Event", e.at))
                    .foregroundStyle(Color.orange.opacity(0.4))
                    .annotation(position: .top, alignment: .center) {
                        Image(systemName: "arrowtriangle.down.fill").font(.system(size: 7)).foregroundStyle(.orange).help(e.message)
                    }
            }
            if let d = selected {
                RuleMark(x: .value("Selected", d)).foregroundStyle(Color.secondary)
                    .annotation(position: .top, alignment: .leading) {
                        VStack(alignment: .leading, spacing: 1) {
                            ForEach(series) { s in
                                if let v = nearest(s.pts, d) {
                                    Text("\(s.group.name): \(normalized ? Units.percent(v) : format(v, s.group))").font(.system(size: 10))
                                }
                            }
                        }
                        .padding(4)
                        .background(RoundedRectangle(cornerRadius: 4).fill(.background))
                    }
            }
        }
        .chartXSelection(value: $selected)
        .chartLegend(groups.count > 1 ? .visible : .hidden)
    }

    func nearest(_ pts: [ChartPoint], _ d: Date) -> Double? {
        pts.min { abs($0.date.timeIntervalSince(d)) < abs($1.date.timeIntervalSince(d)) }?.value
    }

    func format(_ v: Double, _ g: GroupConfig) -> String {
        let unit = g.headline.column == g.sparkline.column ? g.headline.unit : nil
        if unit == "bytes/s" { return Units.bytes(v) + "/s" }
        // Headline columns of byte quantities are large; percentages are small.
        return v > 1_000_000 ? Units.bytes(v) : Units.number(v)
    }
}

/// Storage Explorer (headroom-spec §9, ui-design §11).
@MainActor
struct ExplorerWindow: View {
    let model: HeadroomModel
    let windows: WindowManager
    @State private var blocks = false
    @State private var minGB = 0.5
    @State private var ageDays = 0.0

    var body: some View {
        let ex = model.explorer
        VStack(alignment: .leading, spacing: 10) {
            if !ex.available {
                Text("No explorer section in groups.yaml.").foregroundStyle(.secondary)
            }
            if let v = ex.volumes { VolumeBars(table: v) }
            HStack(spacing: 10) {
                Picker("Root", selection: Binding(get: { ex.root }, set: { ex.root = $0; ex.refresh() })) {
                    ForEach(ex.roots, id: \.self) { Text($0).tag($0) }
                    if !ex.roots.contains(ex.root) { Text(ex.root).tag(ex.root) }
                }
                .frame(width: 220)
                Picker("View", selection: Binding(get: { ex.view }, set: { ex.view = $0; ex.refresh() })) {
                    ForEach(ExplorerView.allCases) { v in Text(v.label).tag(v) }
                }
                .pickerStyle(.segmented).frame(width: 240)
                Stepper("Min \(String(format: "%.1f", minGB)) GB", value: $minGB, in: 0...100, step: 0.5)
                    .onChange(of: minGB) { _, _ in ex.minBytes = minGB * 1e9; ex.refresh() }
                if ex.view == .files {
                    Stepper(ageDays == 0 ? "Any age" : "Last \(Int(ageDays)) d", value: $ageDays, in: 0...365, step: 7)
                        .onChange(of: ageDays) { _, _ in ex.ageDays = ageDays; ex.refresh() }
                }
                Spacer()
                Toggle("Blocks", isOn: $blocks).toggleStyle(.switch).controlSize(.small)
                if ex.scanning {
                    ProgressView().controlSize(.small)
                    Button("Cancel") { ex.cancel() }
                } else {
                    Button { ex.refresh() } label: { Image(systemName: "arrow.clockwise") }
                }
            }
            if let e = ex.error { Text(e).font(.system(size: 11)).foregroundStyle(.secondary) }
            if ex.breadcrumb.count > 0, ex.root != ex.roots.first {
                HStack(spacing: 4) {
                    Button("↑ \(ex.roots.first ?? "~")") { ex.root = ex.roots.first ?? "~"; ex.refresh() }.buttonStyle(.borderless)
                    Text("/ \(ex.root)").foregroundStyle(.secondary)
                }
                .font(.system(size: 11))
            }
            if blocks {
                TreemapView(items: ex.items) { item in
                    if ex.view == .folders, let p = item.path { ex.zoom(into: p) }
                }
                .frame(minHeight: 300)
            } else {
                List(ex.items) { item in
                    HStack {
                        Text(item.name).lineLimit(1).truncationMode(.middle)
                        Spacer()
                        if let d = item.detail { Text(d).foregroundStyle(.secondary).font(.system(size: 11)) }
                        Text(item.sizeText).monospacedDigit().frame(width: 90, alignment: .trailing)
                        if let p = item.path {
                            Button("Reveal") { Finder.reveal(p) }.buttonStyle(.borderless)
                            Button("Trash…") {
                                Task { _ = await model.actions.run("fs.trash", params: ["path": .string(p)], requestedBy: .user) }
                            }
                            .buttonStyle(.bordered).tint(.red).controlSize(.small)
                            .disabled(model.actions.action("fs.trash") == nil)
                        }
                    }
                    .contextMenu {
                        if let p = item.path {
                            Button("Reveal in Finder") { Finder.reveal(p) }
                            Button("Copy path") { Finder.copy(p) }
                            if ex.view == .folders { Button("Explore inside") { ex.zoom(into: p) } }
                        }
                    }
                }
            }
            if model.mode == "optimize" {
                GroupBox("Suggestions") {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(ex.suggestions) { s in
                            DisclosureGroup {
                                if let t = s.table { SysqTableView(table: t, maxHeight: 200) }
                                if let e = s.error { Text(e).foregroundStyle(.secondary) }
                            } label: {
                                HStack {
                                    Text(s.recipe).font(.system(size: 12, design: .monospaced))
                                    Text(model.recipes.entry(s.recipe)?.doc ?? "").font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
                                    Spacer()
                                    if s.total > 0 { Text(Units.bytes(s.total)).monospacedDigit() }
                                }
                            }
                        }
                        Text("Suggestions only: each item is trashed one at a time, with a confirmation.").font(.system(size: 10)).foregroundStyle(.secondary)
                    }
                }
                .task { await ex.loadSuggestions() }
            }
        }
        .padding(14)
        .task {
            ex.minBytes = minGB * 1e9
            await ex.loadVolumes()
            if ex.items.isEmpty { ex.refresh() }
        }
    }
}

@MainActor
struct VolumeBars: View {
    var table: SysqTable
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(0..<table.count, id: \.self) { i in
                if table.value(i, "internal")?.bool != true, let size = table.double(i, "size"), size > 0 {
                    let used = table.double(i, "used") ?? 0
                    HStack(spacing: 8) {
                        Text(table.string(i, "name") ?? table.string(i, "mount") ?? "").frame(width: 140, alignment: .leading).lineLimit(1)
                        GeometryReader { g in
                            ZStack(alignment: .leading) {
                                Capsule().fill(Color.secondary.opacity(0.15))
                                Capsule().fill(Color.accentColor.opacity(0.7)).frame(width: g.size.width * CGFloat(min(1, used / size)))
                            }
                        }
                        .frame(height: 8)
                        Text("\(table.text(i, "free")) free of \(table.text(i, "size"))").font(.system(size: 11)).foregroundStyle(.secondary).frame(width: 190, alignment: .trailing)
                    }
                }
            }
        }
    }
}

@MainActor
struct TreemapView: View {
    var items: [ExplorerItem]
    var onTap: (ExplorerItem) -> Void

    var body: some View {
        GeometryReader { geo in
            let rects = Treemap.layout(items.map(\.size), width: Double(geo.size.width), height: Double(geo.size.height))
            ZStack(alignment: .topLeading) {
                ForEach(rects, id: \.index) { r in
                    let item = items[r.index]
                    RoundedRectangle(cornerRadius: 3)
                        .fill(Color.accentColor.opacity(0.15 + 0.5 * Double(r.index % 5) / 5))
                        .overlay(alignment: .topLeading) {
                            if r.w > 60, r.h > 28 {
                                VStack(alignment: .leading, spacing: 0) {
                                    Text(item.name).font(.system(size: 10, weight: .medium)).lineLimit(1)
                                    Text(item.sizeText).font(.system(size: 9)).foregroundStyle(.secondary)
                                }
                                .padding(3)
                            }
                        }
                        .frame(width: max(0, CGFloat(r.w) - 2), height: max(0, CGFloat(r.h) - 2))
                        .offset(x: CGFloat(r.x) + 1, y: CGFloat(r.y) + 1)
                        .onTapGesture { onTap(item) }
                        .help("\(item.name) — \(item.sizeText)")
                }
            }
        }
    }
}
