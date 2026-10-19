import Foundation
import Observation

// Holds the live state of every configured group (implementation.md §5.2).
// Each distinct (recipe, params) binding is one feed, shared by every group
// that names it; feeds follow sampling.yaml's rates for the current state.

public enum GroupStatus: String, Sendable, Hashable, Comparable {
    case unknown, green, amber, red

    init(attention: String?) {
        switch attention {
        case "ok": self = .green
        case "amber": self = .amber
        case "red": self = .red
        default: self = .unknown
        }
    }

    public var severity: Int { switch self { case .unknown: return 0; case .green: return 1; case .amber: return 2; case .red: return 3 } }
    public static func < (a: GroupStatus, b: GroupStatus) -> Bool { a.severity < b.severity }
    public var isAlert: Bool { self == .amber || self == .red }
}

/// The latest result of one feed.
public struct FeedResult: Sendable, Equatable {
    public var table: SysqTable
    public var error: String?
    public var updated: Date
}

/// What a group's views are built from (an immutable value).
public struct GroupState: Sendable, Equatable {
    public var id: String
    public var headline: FeedResult?
    public var metrics: [FeedResult?]
    public var detail: FeedResult?
    public var consumers: FeedResult?
    public var attention: FeedResult?
    public var reclaimable: FeedResult?
    public var sparkline: [Double]

    public var status: GroupStatus { GroupStatus(attention: attention?.table.string(0, "status")) }
    public var attentionMessage: String? { attention?.table.string(0, "message") }
    public var updated: Date? { headline?.updated }
    /// Why the headline can't be shown, if it can't.
    public var problem: String? {
        if let e = headline?.error, !e.isEmpty { return e }
        if let u = headline?.table.meta?.unavailable, !u.isEmpty { return u }
        return nil
    }
}

/// Which sampling rate a feed follows.
public enum FeedKind: String, Sendable { case headline, detail, consumers }

struct FeedSpec: Hashable, Sendable {
    var recipe: String
    var params: [String: JSONValue]
    var kind: FeedKind

    var key: String { FeedSpec.key(recipe, params) }
    static func key(_ recipe: String, _ params: [String: JSONValue]?) -> String {
        guard let params, !params.isEmpty else { return recipe }
        return recipe + JSONValue.object(params).json
    }
}

@MainActor
@Observable
public final class GroupStore {
    public private(set) var results: [String: FeedResult] = [:]
    public private(set) var sparklines: [String: [Double]] = [:]
    /// A view (small panel, large window, drill-in) is showing.
    public private(set) var viewOpen = false
    public private(set) var onBattery = false
    public private(set) var paused = false
    public private(set) var mode = "view"

    @ObservationIgnored public private(set) var config: AppConfig
    @ObservationIgnored var client: SysqClientProtocol?
    @ObservationIgnored var feeds: [String: (spec: FeedSpec, every: Double, task: Task<Void, Never>)] = [:]
    @ObservationIgnored var sparkTasks: [String: Task<Void, Never>] = [:]
    @ObservationIgnored let sparkCap = 300
    /// Called when sysqd says the token is no longer valid.
    @ObservationIgnored public var onUnauthorized: (() -> Void)?

    public init(config: AppConfig) { self.config = config }

    // MARK: inputs

    public func attach(_ client: SysqClientProtocol?) {
        self.client = client
        for (_, f) in feeds { f.task.cancel() }
        feeds = [:]
        sparkTasks.values.forEach { $0.cancel() }
        sparkTasks = [:]
        reconcile()
    }

    public func update(config: AppConfig) {
        let old = self.config
        self.config = config
        if old.groups.groups != config.groups.groups { sparkTasks.values.forEach { $0.cancel() }; sparkTasks = [:] }
        reconcile()
    }

    public func setViewOpen(_ open: Bool) { if open != viewOpen { viewOpen = open; reconcile() } }
    public func setOnBattery(_ b: Bool) { if b != onBattery { onBattery = b; reconcile() } }
    public func setPaused(_ p: Bool) { if p != paused { paused = p; reconcile() } }
    public func setMode(_ m: String) { if m != mode { mode = m; reconcile() } }

    // MARK: outputs

    public func state(_ id: String) -> GroupState? {
        guard let g = config.group(id) else { return nil }
        return GroupState(
            id: id,
            headline: results[FeedSpec.key(g.headline.recipe, g.headline.params)],
            metrics: (g.metrics ?? []).map { results[FeedSpec.key($0.recipe, $0.params)] },
            detail: results[FeedSpec.key(g.detail.recipe, g.detail.params)],
            consumers: results[FeedSpec.key(g.consumers.recipe, g.consumers.params)],
            attention: results[FeedSpec.key(g.attention, attentionParams(g))],
            reclaimable: g.reclaimable.flatMap { results[FeedSpec.key($0.recipe, $0.params)] },
            sparkline: sparklines[id] ?? [])
    }

    public var states: [GroupState] { config.orderedGroups.compactMap { state($0.id) } }

    /// Refreshes everything now (the ↻ button, or after waking from sleep).
    public func refreshAll() {
        for (key, f) in feeds {
            f.task.cancel()
            feeds[key] = (f.spec, f.every, start(f.spec, every: f.every))
        }
    }

    // MARK: feeds

    func attentionParams(_ g: GroupConfig) -> [String: JSONValue] {
        guard let t = config.thresholds[g.id] else { return [:] }
        var p: [String: JSONValue] = ["amber": .number(t.amber.value), "red": .number(t.red.value)]
        if let d = t.direction { p["direction"] = .string(d) }
        return p
    }

    func rate(_ kind: FeedKind, hidden: Bool) -> Double {
        let r = config.sampling.rates
        let set: Rates = viewOpen ? r.view_open : (onBattery ? (r.on_battery ?? r.idle) : r.idle)
        func pick(_ rs: Rates?) -> String? {
            switch kind {
            case .headline: return rs?.headline
            case .detail: return rs?.detail
            case .consumers: return rs?.consumers
            }
        }
        let defaults: [FeedKind: Double] = [.headline: viewOpen ? 2 : 30, .detail: viewOpen ? 5 : 60, .consumers: viewOpen ? 5 : 60]
        var s = seconds(pick(set), default: defaults[kind]!)
        if hidden { s = max(s, seconds(pick(config.sampling.hidden_groups) ?? config.sampling.hidden_groups?.headline, default: 300)) }
        return max(1, s)
    }

    /// The feeds the current config, mode and view state call for.
    func desired() -> [String: (FeedSpec, Double)] {
        var out: [String: (FeedSpec, Double)] = [:]
        let hidden = Set(config.layout.hidden_groups ?? [])
        func add(_ recipe: String, _ params: [String: JSONValue]?, _ kind: FeedKind, _ isHidden: Bool) {
            let spec = FeedSpec(recipe: recipe, params: params ?? [:], kind: kind)
            let every = rate(kind, hidden: isHidden)
            if let have = out[spec.key], have.1 <= every { return }
            out[spec.key] = (spec, every)
        }
        for g in config.groups.groups {
            let h = hidden.contains(g.id)
            add(g.headline.recipe, g.headline.params, .headline, h)
            for m in g.metrics ?? [] { add(m.recipe, m.params, .headline, h) }
            add(g.attention, attentionParams(g), .detail, h)
            // Detail and consumer lists only matter while something shows them.
            if viewOpen || !h {
                add(g.consumers.recipe, g.consumers.params, .consumers, h)
            }
            if viewOpen { add(g.detail.recipe, g.detail.params, .detail, h) }
            if let rc = g.reclaimable, viewOpen || mode == "optimize" { add(rc.recipe, rc.params, .detail, h) }
        }
        return out
    }

    func reconcile() {
        guard client != nil, !paused else {
            for (_, f) in feeds { f.task.cancel() }
            feeds = [:]
            return
        }
        let want = desired()
        for (key, f) in feeds where want[key] == nil || want[key]!.1 != f.every {
            f.task.cancel()
            feeds[key] = nil
        }
        for (key, (spec, every)) in want where feeds[key] == nil {
            feeds[key] = (spec, every, start(spec, every: every))
        }
        for g in config.groups.groups where sparkTasks[g.id] == nil {
            sparkTasks[g.id] = loadSparkline(g)
        }
    }

    /// One feed: subscribe over SSE; if that fails, poll at the same rate and
    /// retry the subscription with backoff.
    func start(_ spec: FeedSpec, every: Double) -> Task<Void, Never> {
        let client = self.client
        return Task { [weak self] in
            guard let client else { return }
            var backoff = 2.0
            while !Task.isCancelled {
                do {
                    for try await u in client.subscribe(spec.recipe, params: spec.params, every: every) {
                        if Task.isCancelled { return }
                        self?.deliver(spec, u.table, u.error)
                        backoff = 2
                    }
                } catch let e as ClientError where e.kind == .unauthorized {
                    self?.onUnauthorized?()
                    return
                } catch {
                    if Task.isCancelled { return }
                }
                // Poll once so the view isn't stale, then try again.
                do {
                    let r = try await client.runRecipe(spec.recipe, params: spec.params)
                    self?.deliver(spec, r.table, r.error)
                } catch let e as ClientError where e.kind == .unauthorized {
                    self?.onUnauthorized?()
                    return
                } catch {
                    self?.fail(spec, error)
                }
                try? await Task.sleep(nanoseconds: UInt64(max(backoff, every) * 1e9))
                backoff = min(backoff * 2, 60)
            }
        }
    }

    func deliver(_ spec: FeedSpec, _ table: SysqTable, _ error: String?) {
        let now = Date()
        results[spec.key] = FeedResult(table: table, error: error?.isEmpty == true ? nil : error, updated: now)
        // Live sparkline points from the headline feed.
        for g in config.groups.groups where g.sparkline.recipe == spec.recipe && spec.params.isEmpty && seconds(g.sparkline.range, default: 300) <= 3600 {
            guard let v = table.double(0, g.sparkline.column) else { continue }
            var s = sparklines[g.id] ?? []
            s.append(v)
            // Keep about one range's worth of points at the current rate.
            let cap = min(sparkCap, max(30, Int(seconds(g.sparkline.range, default: 300) / rate(.headline, hidden: false))))
            if s.count > cap { s.removeFirst(s.count - cap) }
            sparklines[g.id] = s
        }
    }

    func fail(_ spec: FeedSpec, _ error: Error) {
        var r = results[spec.key] ?? FeedResult(table: SysqTable(schema: [], rows: []), error: nil, updated: Date())
        r.error = error.localizedDescription
        results[spec.key] = r
    }

    func loadSparkline(_ g: GroupConfig) -> Task<Void, Never> {
        let client = self.client
        return Task { [weak self] in
            guard let client else { return }
            let range = g.sparkline.range ?? "5m"
            let long = seconds(range, default: 300) > 3600
            while !Task.isCancelled {
                if let t = try? await client.history(g.sparkline.recipe, range: range, column: g.sparkline.column, key: nil) {
                    let values = (0..<t.count).compactMap { t.double($0, "value") }
                    guard let self else { return }
                    if !values.isEmpty {
                        // Short ranges: history first, then live points; long
                        // ranges are re-read from history (downsampled tiers).
                        var s = long ? values : values + (self.sparklines[g.id] ?? [])
                        if s.count > self.sparkCap { s.removeFirst(s.count - self.sparkCap) }
                        self.sparklines[g.id] = s
                    }
                }
                if !long { return }
                try? await Task.sleep(nanoseconds: 600 * 1_000_000_000)
            }
        }
    }
}
