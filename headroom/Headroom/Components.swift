import AppKit
import HeadroomKit
import SwiftUI

// Shared components. Each renders a view model or a sysq table and knows
// nothing about which group, recipe or command is behind it.

extension GroupStatus {
    var color: Color {
        switch self {
        case .green: return .green
        case .amber: return .orange
        case .red: return .red
        case .unknown: return .gray
        }
    }
    var label: String {
        switch self {
        case .green: return "normal"
        case .amber: return "amber"
        case .red: return "red"
        case .unknown: return "unknown"
        }
    }
}

extension AssistantStatus {
    var color: Color {
        switch self {
        case .ok: return .green
        case .attention: return .yellow
        case .warning: return .orange
        case .critical: return .red
        }
    }
}

struct StatusDot: View {
    var status: GroupStatus
    var size: CGFloat = 8
    var body: some View {
        Circle().fill(status.color).frame(width: size, height: size)
            .accessibilityLabel("status \(status.label)")
    }
}

/// The background material for panels.
struct VisualEffect: NSViewRepresentable {
    var material: NSVisualEffectView.Material = .sidebar
    func makeNSView(context: Context) -> NSVisualEffectView {
        let v = NSVisualEffectView()
        v.material = material
        v.blendingMode = .behindWindow
        v.state = .active
        return v
    }
    func updateNSView(_ v: NSVisualEffectView, context: Context) { v.material = material }
}

struct Sparkline: View {
    var values: [Double]
    var color: Color
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        GeometryReader { geo in
            let pts = points(in: geo.size)
            ZStack {
                if pts.count > 1 {
                    Path { p in
                        p.move(to: CGPoint(x: pts[0].x, y: geo.size.height))
                        for pt in pts { p.addLine(to: pt) }
                        p.addLine(to: CGPoint(x: pts[pts.count - 1].x, y: geo.size.height))
                        p.closeSubpath()
                    }
                    .fill(color.opacity(0.15))
                    Path { p in
                        p.move(to: pts[0])
                        for pt in pts.dropFirst() { p.addLine(to: pt) }
                    }
                    .stroke(color, style: StrokeStyle(lineWidth: 1.5, lineJoin: .round))
                } else {
                    Rectangle().fill(Color.secondary.opacity(0.08))
                }
            }
            .animation(reduceMotion ? nil : .easeOut(duration: 0.3), value: values.count)
        }
        .accessibilityHidden(true)
    }

    func points(in size: CGSize) -> [CGPoint] {
        guard values.count > 1, let lo = values.min(), let hi = values.max() else { return [] }
        let span = hi - lo == 0 ? 1 : hi - lo
        let step = size.width / CGFloat(values.count - 1)
        return values.enumerated().map { i, v in
            CGPoint(x: CGFloat(i) * step, y: size.height - CGFloat((v - lo) / span) * (size.height - 2) - 1)
        }
    }
}

/// The group box: same shape in the small view, large view and drill-in.
struct GroupBoxView: View {
    var vm: GroupViewModel
    var compact = true
    var tint: String?
    var onOpen: () -> Void = {}
    @State private var hover = false
    @Environment(\.colorSchemeContrast) private var contrast

    var body: some View {
        VStack(alignment: .leading, spacing: compact ? 4 : 6) {
            HStack(spacing: 6) {
                Image(systemName: vm.symbol).foregroundStyle(.secondary).frame(width: 16)
                Text(vm.name).font(.system(size: compact ? 12 : 13, weight: .semibold)).lineLimit(1)
                Spacer(minLength: 4)
                if hover { Text("Open").font(.system(size: 10)).foregroundStyle(.secondary) }
                if vm.isKey && !compact { Image(systemName: "star.fill").font(.system(size: 9)).foregroundStyle(.yellow) }
                if tint == "warnings", vm.status.isAlert { Image(systemName: "exclamationmark.triangle.fill").font(.system(size: 10)).foregroundStyle(vm.status.color) }
                StatusDot(status: vm.status)
            }
            HStack(alignment: .firstTextBaseline, spacing: 4) {
                Text(vm.headline)
                    .font(.system(size: compact ? 20 : 24, weight: .semibold, design: .rounded))
                    .monospacedDigit()
                    .foregroundStyle(vm.unavailable == nil ? Color.primary : Color.secondary)
                    .lineLimit(1).minimumScaleFactor(0.6)
                if let l = vm.headlineLabel { Text(l).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1) }
            }
            .help(vm.unavailable ?? vm.statusMessage ?? "")
            HStack(spacing: 6) {
                Sparkline(values: vm.sparkline, color: vm.status == .unknown ? .accentColor : vm.status.color)
                    .frame(height: compact ? 22 : 30)
                if let m = vm.metrics.first {
                    metric(m).frame(minWidth: 70, alignment: .trailing)
                }
            }
            ForEach(vm.metrics.dropFirst().prefix(compact ? 1 : 4)) { m in metric(m) }
            if tint == "reclaimable", let r = vm.reclaimable {
                Text("Reclaim up to \(r)").font(.system(size: 11, weight: .medium)).foregroundStyle(.teal)
            }
        }
        .padding(compact ? 10 : 12)
        .background(
            RoundedRectangle(cornerRadius: 12)
                .fill(vm.status.isAlert ? vm.status.color.opacity(contrast == .increased ? 0.3 : 0.1) : Color.secondary.opacity(0.08))
        )
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color.secondary.opacity(hover ? 0.35 : 0.15), lineWidth: 1))
        .contentShape(RoundedRectangle(cornerRadius: 12))
        .onHover { hover = $0 }
        .onTapGesture(perform: onOpen)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(vm.accessibilityLabel)
        .accessibilityAddTraits(.isButton)
    }

    func metric(_ m: MetricViewModel) -> some View {
        HStack(spacing: 4) {
            Text(m.label).foregroundStyle(.secondary)
            Text(m.text).monospacedDigit()
        }
        .font(.system(size: 11))
        .lineLimit(1)
    }
}

struct ConsumerRow: View {
    var vm: ConsumerViewModel
    var onActions: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            if let icon = Finder.icon(kind: vm.kind, id: vm.itemID, name: vm.name) {
                Image(nsImage: icon).resizable().frame(width: 14, height: 14)
            }
            Text(vm.name).lineLimit(1).truncationMode(.middle)
            Spacer(minLength: 6)
            Text(vm.valueText).monospacedDigit().foregroundStyle(.secondary).frame(minWidth: 60, alignment: .trailing)
            GeometryReader { g in
                ZStack(alignment: .leading) {
                    Capsule().fill(Color.secondary.opacity(0.15))
                    Capsule().fill(Color.accentColor.opacity(0.7)).frame(width: max(2, g.size.width * vm.fraction))
                }
            }
            .frame(width: 70, height: 5)
            Button(action: onActions) { Image(systemName: "ellipsis.circle") }
                .buttonStyle(.borderless)
                .help("Actions")
                .disabled(vm.itemID == nil)
        }
        .font(.system(size: 12))
        .padding(.vertical, 2)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(vm.name), \(vm.valueText)")
    }
}

/// Renders any sysq table: discovered columns, units, click-to-sort.
struct SysqTableView: View {
    var table: SysqTable
    var maxHeight: CGFloat? = 320
    /// Called with the table as shown (sorted and filtered) and the row.
    var onRow: ((SysqTable, Int) -> Void)? = nil
    @State private var sortColumn: String?
    @State private var descending = true
    @State private var filter = ""

    var shown: SysqTable {
        var t = table
        if let c = sortColumn { t = t.sorted(by: c, descending: descending) }
        let f = filter.trimmingCharacters(in: .whitespaces).lowercased()
        if !f.isEmpty {
            t.rows = (0..<t.count).filter { i in t.schema.contains { t.text(i, $0.name).lowercased().contains(f) } }.map { t.rows[$0] }
        }
        return t
    }

    var body: some View {
        let t = shown
        let cols = table.schema
        VStack(alignment: .leading, spacing: 4) {
            if table.count > 8 {
                TextField("Filter", text: $filter).textFieldStyle(.roundedBorder).controlSize(.small).frame(maxWidth: 220)
            }
            if table.schema.isEmpty {
                Text(table.meta?.error ?? table.meta?.unavailable ?? "No data").font(.system(size: 12)).foregroundStyle(.secondary)
            } else {
                ScrollView([.vertical, .horizontal]) {
                    Grid(alignment: .leading, horizontalSpacing: 14, verticalSpacing: 3) {
                        GridRow {
                            ForEach(cols, id: \.name) { c in
                                Button {
                                    if sortColumn == c.name { descending.toggle() } else { sortColumn = c.name; descending = c.isNumeric }
                                } label: {
                                    HStack(spacing: 2) {
                                        Text(c.unit.map { "\(c.name) (\($0))" } ?? c.name)
                                        if sortColumn == c.name { Image(systemName: descending ? "chevron.down" : "chevron.up").font(.system(size: 8)) }
                                    }
                                }
                                .buttonStyle(.plain)
                                .font(.system(size: 11, weight: .semibold))
                                .foregroundStyle(.secondary)
                                .help(c.doc ?? c.type)
                                .gridColumnAlignment(c.isNumeric ? .trailing : .leading)
                            }
                        }
                        Divider()
                        ForEach(0..<t.count, id: \.self) { i in
                            GridRow {
                                ForEach(cols, id: \.name) { c in
                                    Text(t.text(i, c.name))
                                        .font(.system(size: 12))
                                        .monospacedDigit()
                                        .lineLimit(1)
                                        .truncationMode(.middle)
                                        .frame(maxWidth: 360, alignment: c.isNumeric ? .trailing : .leading)
                                        .textSelection(.enabled)
                                }
                            }
                            .contentShape(Rectangle())
                            .onTapGesture { onRow?(t, i) }
                        }
                    }
                    .padding(.vertical, 2)
                }
                .frame(maxHeight: maxHeight)
                HStack(spacing: 8) {
                    Text("\(table.count) rows").foregroundStyle(.secondary)
                    if table.meta?.truncated == true { Text("truncated").foregroundStyle(.orange) }
                    if let u = table.meta?.unavailable, !u.isEmpty { Text(u).foregroundStyle(.secondary).lineLimit(1) }
                }
                .font(.system(size: 10))
            }
        }
    }
}

struct BannerList: View {
    var banners: [Banner]
    var body: some View {
        if !banners.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                ForEach(banners.prefix(4)) { b in
                    HStack(alignment: .top, spacing: 6) {
                        Image(systemName: b.kind == .service ? "bolt.horizontal.circle" : "exclamationmark.triangle")
                            .foregroundStyle(.orange)
                        Text(b.text).font(.system(size: 11)).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                    }
                }
                if banners.count > 4 { Text("and \(banners.count - 4) more").font(.system(size: 10)).foregroundStyle(.secondary) }
            }
            .padding(8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 8).fill(Color.orange.opacity(0.1)))
        }
    }
}

struct ModePicker: View {
    let model: HeadroomModel
    var body: some View {
        Picker("Mode", selection: Binding(get: { model.mode }, set: { model.setMode($0) })) {
            ForEach(ViewModels.modes(model.config)) { m in Text(m.label).tag(m.id) }
        }
        .pickerStyle(.segmented)
        .labelsHidden()
        .controlSize(.small)
    }
}

/// Shown instead of data when sysqd can't be reached.
struct ServiceDownCard: View {
    let model: HeadroomModel
    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: "bolt.horizontal.circle").font(.system(size: 26)).foregroundStyle(.secondary)
            Text("sysqd isn't running").font(.headline)
            if let m = model.services.sysq.message { Text(m).font(.system(size: 11)).foregroundStyle(.secondary).multilineTextAlignment(.center) }
            Button("Start sysqd") { model.retryConnection() }
        }
        .padding(16)
        .frame(maxWidth: .infinity)
        .background(RoundedRectangle(cornerRadius: 12).fill(Color.secondary.opacity(0.08)))
    }
}

/// Recipe output inline (▶ Run).
struct RecipeResultView: View {
    var run: RecipeRun
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if run.running {
                ProgressView().controlSize(.small)
            } else if let r = run.result {
                ForEach(Array(r.items.enumerated()), id: \.offset) { _, item in
                    if let l = item.label, r.items.count > 1 { Text(l).font(.system(size: 11, weight: .semibold)) }
                    SysqTableView(table: item.table, maxHeight: 240)
                }
                if let e = run.error, !e.isEmpty { Text(e).font(.system(size: 11)).foregroundStyle(.red) }
            } else if let e = run.error {
                Text(e).font(.system(size: 11)).foregroundStyle(.red)
            }
        }
    }
}
