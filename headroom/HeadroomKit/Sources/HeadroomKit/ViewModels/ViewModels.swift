import Foundation

// Render-ready values (separation-of-concerns §3). Pure functions of
// (store state, config): views know these types and nothing below them.

public struct MetricViewModel: Sendable, Hashable, Identifiable {
    public var id: String { label }
    public var label: String
    public var text: String
}

public struct ConsumerViewModel: Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var valueText: String
    public var value: Double
    /// 0...1 relative to the largest consumer shown.
    public var fraction: Double
    /// What the row is (process, app, path, model) and its identifier, for the
    /// Actions panel.
    public var kind: String?
    public var itemID: String?
    public var groupID: String
}

public struct GroupViewModel: Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var symbol: String
    public var headline: String
    public var headlineLabel: String?
    public var status: GroupStatus
    public var statusMessage: String?
    public var sparkline: [Double]
    public var metrics: [MetricViewModel]
    public var consumers: [ConsumerViewModel]
    public var reclaimable: String?
    public var reclaimableValue: Double?
    /// Set when the headline can't be shown (grey value + tooltip).
    public var unavailable: String?
    public var updated: Date?
    public var isKey: Bool
    public var emphasis: String?

    public var accessibilityLabel: String {
        var parts = [name, [headline, headlineLabel].compactMap { $0 }.joined(separator: " ")]
        if status != .unknown { parts.append("status \(status == .green ? "normal" : status.rawValue)") }
        return parts.joined(separator: ", ")
    }
}

public struct ModeViewModel: Sendable, Hashable, Identifiable {
    public var id: String
    public var label: String
    public var question: String
    public var tint: String?
    public var rankBy: String?
    public var historyRange: String?
    public var consumerCount: Int?
}

/// Recipes and actions a mode offers for one group.
public struct ModeBindingViewModel: Sendable, Hashable {
    public var recipes: [String]
    public var actions: [String]
    public var emphasis: String?
}

public enum ViewModels {
    public static func modes(_ c: AppConfig) -> [ModeViewModel] {
        c.modeOrder.compactMap { id in
            guard let m = c.modes.modes[id] else { return nil }
            return ModeViewModel(id: id, label: m.label, question: m.question, tint: m.tint, rankBy: m.rank_by,
                                 historyRange: m.history_range, consumerCount: m.consumers)
        }
    }

    public static func binding(_ c: AppConfig, mode: String, group: String) -> ModeBindingViewModel {
        let g = c.modes.modes[mode]?.groups?[group]
        return ModeBindingViewModel(recipes: g?.recipes ?? [], actions: g?.actions ?? [], emphasis: g?.emphasis)
    }

    /// Formats the first row's value of a binding's column.
    static func figure(_ r: FeedResult?, _ b: RecipeBinding) -> String {
        guard let t = r?.table, let col = b.column, let c = t.column(col), let v = t.value(0, col) else { return "—" }
        return Formatter.text(v, column: c, unit: b.unit)
    }

    public static func group(_ g: GroupConfig, state s: GroupState?, config c: AppConfig, mode: String,
                             isKey: Bool, consumerLimit: Int) -> GroupViewModel {
        let metrics = zip(g.metrics ?? [], s?.metrics ?? Array(repeating: nil, count: g.metrics?.count ?? 0)).map { b, r in
            MetricViewModel(label: b.label ?? b.column ?? b.recipe, text: figure(r, b))
        }
        var reclaim: (String, Double)?
        if let rb = g.reclaimable, let t = s?.reclaimable?.table, let col = rb.column, let colDef = t.column(col) {
            let total = (0..<t.count).compactMap { t.double($0, col) }.reduce(0, +)
            reclaim = (Formatter.text(.number(total), column: colDef, unit: rb.unit), total)
        }
        return GroupViewModel(
            id: g.id, name: g.name, symbol: g.symbol,
            headline: figure(s?.headline, g.headline),
            headlineLabel: g.headline.label,
            status: s?.status ?? .unknown,
            statusMessage: s?.attentionMessage,
            sparkline: s?.sparkline ?? [],
            metrics: metrics,
            consumers: consumers(g, s?.consumers?.table, limit: consumerLimit),
            reclaimable: reclaim?.0, reclaimableValue: reclaim?.1,
            unavailable: s == nil || s?.headline == nil ? nil : s?.problem,
            updated: s?.updated,
            isKey: isKey,
            emphasis: binding(c, mode: mode, group: g.id).emphasis)
    }

    public static func consumers(_ g: GroupConfig, _ t: SysqTable?, limit: Int) -> [ConsumerViewModel] {
        guard let t, let valueCol = t.column(g.consumers.value) else { return [] }
        let nameCol = t.nameColumn(preferred: g.consumers.name)
        let sorted = t.sorted(by: valueCol.name, descending: true)
        let n = min(limit, sorted.count)
        let top = (0..<n).map { sorted.double($0, valueCol.name) ?? 0 }.max() ?? 0
        return (0..<n).map { i in
            let v = sorted.double(i, valueCol.name) ?? 0
            let name = nameCol.map { sorted.text(i, $0) } ?? "—"
            let itemID = g.consumers.id.flatMap { sorted.value(i, $0) }.map { $0.string ?? $0.description }
            return ConsumerViewModel(id: "\(g.id):\(itemID ?? name):\(i)", name: name,
                                     valueText: sorted.text(i, valueCol.name), value: v,
                                     fraction: top > 0 ? max(0, min(1, v / top)) : 0,
                                     kind: g.consumers.kind, itemID: itemID, groupID: g.id)
        }
    }

    /// Groups in display order for a mode: Optimize (rank_by: reclaimable)
    /// sorts by the reclaimable amount.
    public static func ordered(_ vms: [GroupViewModel], mode: ModeViewModel?) -> [GroupViewModel] {
        guard mode?.rankBy == "reclaimable" else { return vms }
        return vms.enumerated().sorted { a, b in
            let x = a.element.reclaimableValue ?? -1, y = b.element.reclaimableValue ?? -1
            return x == y ? a.offset < b.offset : x > y
        }.map(\.element)
    }

    /// The menu bar readout ("<group>.headline").
    public static func readout(_ c: AppConfig, vms: [GroupViewModel]) -> (text: String, status: GroupStatus)? {
        guard let r = c.layout.menu_bar_readout, !r.isEmpty, r != "none" else { return nil }
        if r == "worst" {
            guard let w = vms.max(by: { $0.status < $1.status }) else { return nil }
            return (w.headline, w.status)
        }
        let id = r.split(separator: ".").first.map(String.init) ?? r
        guard let vm = vms.first(where: { $0.id == id }) else { return nil }
        return (vm.headline, vm.status)
    }
}

/// Chooses the four key groups (headroom-spec §4.1). Pinned: the configured
/// list. Auto: the most severe groups, where a group that earned a slot keeps
/// it for the hold time after it calms down.
public struct KeyGroupSelector: Sendable {
    public static let slots = 4
    var held: [String: (severity: Int, until: Date)] = [:]
    public private(set) var current: [String] = []

    public init() {}

    public mutating func select(_ c: AppConfig, statuses: [String: GroupStatus], now: Date = Date()) -> [String] {
        let order = c.orderedGroups.map(\.id).filter { !(c.layout.hidden_groups ?? []).contains($0) }
        let pinned = c.layout.pinned.filter { order.contains($0) }
        if c.layout.keyMode != "auto" {
            current = Array((pinned.isEmpty ? order : pinned).prefix(Self.slots))
            return current
        }
        let hold = c.layout.holdMinutes * 60
        for (id, h) in held where h.until < now { held[id] = nil }
        func score(_ id: String) -> Int {
            let s = statuses[id]?.severity ?? 0
            if let h = held[id], current.contains(id) { return max(s, h.severity) }
            return s
        }
        let base = pinned.isEmpty ? order : pinned + order.filter { !pinned.contains($0) }
        let ranked = base.enumerated().sorted { a, b in
            let x = score(a.element), y = score(b.element)
            if x != y { return x > y }
            // Among equally stressed groups the ones already shown stay;
            // calm groups fall back to the configured order.
            let alert = GroupStatus.amber.severity
            let ca = current.contains(a.element) && x >= alert, cb = current.contains(b.element) && y >= alert
            if ca != cb { return ca }
            return a.offset < b.offset
        }.map(\.element)
        let next = Array(ranked.prefix(Self.slots))
        for id in next {
            let s = statuses[id]?.severity ?? 0
            if s >= GroupStatus.amber.severity {
                held[id] = (max(s, held[id]?.severity ?? 0), now.addingTimeInterval(hold))
            }
        }
        // Keep the previous order for groups that stay, so boxes don't jump.
        let stay = current.filter { next.contains($0) }
        current = stay + next.filter { !stay.contains($0) }
        return current
    }
}
