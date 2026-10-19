import Foundation
import Observation

// Read-only stores: recipe runs for the Actions panel and drill-in, history
// for the Graphs panel, API pairing, and notification decisions.

/// One recipe run shown inline (▶ Run).
public struct RecipeRun: Sendable, Identifiable {
    public var id: String
    public var name: String
    public var params: [String: JSONValue]
    public var running: Bool
    public var result: RunResult?
    public var error: String?
    public var at: Date
}

@MainActor
@Observable
public final class RecipeStore {
    public private(set) var catalog: [CatalogEntry] = []
    public private(set) var runs: [String: RecipeRun] = [:]
    public private(set) var explains: [String: ExplainPlan] = [:]
    @ObservationIgnored var client: SysqClientProtocol?

    public init() {}

    public func attach(_ c: SysqClientProtocol?) {
        client = c
        Task { await loadCatalog() }
    }

    public func loadCatalog() async {
        guard let client, let c = try? await client.catalog() else { return }
        catalog = c
    }

    public func entry(_ name: String) -> CatalogEntry? { catalog.first { $0.name == name } }

    /// Recipes named in groups.yaml or modes.yaml that sysqd doesn't have
    /// (the group renders as unavailable; a banner says which).
    public func missingRecipes(_ c: AppConfig) -> [String] {
        guard !catalog.isEmpty else { return [] }
        let have = Set(catalog.map(\.name))
        var names = Set<String>()
        for g in c.groups.groups {
            names.formUnion([g.headline.recipe, g.detail.recipe, g.consumers.recipe, g.sparkline.recipe, g.attention])
            names.formUnion((g.metrics ?? []).map(\.recipe))
            if let r = g.reclaimable { names.insert(r.recipe) }
        }
        for m in c.modes.modes.values { for g in (m.groups ?? [:]).values { names.formUnion(g.recipes ?? []) } }
        return names.subtracting(have).sorted()
    }

    public nonisolated static func runKey(_ name: String, _ params: [String: JSONValue]) -> String { FeedSpec.key(name, params) }

    @discardableResult
    public func run(_ name: String, params: [String: JSONValue] = [:]) async -> RecipeRun {
        let key = Self.runKey(name, params)
        var r = RecipeRun(id: key, name: name, params: params, running: true, at: Date())
        runs[key] = r
        guard let client else {
            r.running = false; r.error = "sysqd isn't connected"; runs[key] = r
            return r
        }
        do {
            let res = try await client.runRecipe(name, params: params)
            r.result = res
            r.error = res.error
        } catch {
            r.error = error.localizedDescription
        }
        r.running = false
        r.at = Date()
        runs[key] = r
        return r
    }

    public func explain(_ name: String) async -> ExplainPlan? {
        if let e = explains[name] { return e }
        guard let client, let p = try? await client.explain(recipe: name) else { return nil }
        explains[name] = p
        return p
    }

    /// `sysq disk hogs --root ~/Library` for Copy as CLI.
    public nonisolated static func cli(_ name: String, params: [String: JSONValue]) -> String {
        var parts = ["sysq"] + name.split(separator: ".").map { String($0).replacingOccurrences(of: "_", with: "-") }
        for k in params.keys.sorted() {
            let v = params[k]!
            let flag = "--" + k.replacingOccurrences(of: "_", with: "-")
            if case .bool(let b) = v { parts.append(b ? flag : flag + "=false"); continue }
            let text = v.string ?? v.description
            let quoted = text.range(of: "^[A-Za-z0-9._/~:@%+=-]+$", options: .regularExpression) != nil ? text : "'" + text.replacingOccurrences(of: "'", with: "'\\''") + "'"
            parts.append("\(flag) \(quoted)")
        }
        return parts.joined(separator: " ")
    }

    public func clear(_ key: String) { runs[key] = nil }
}

/// One series for the Graphs panel.
public struct Series: Sendable, Identifiable, Hashable {
    public var id: String
    public var label: String
    public var unit: String?
    public var columnType: String
    public var points: [(Date, Double)]

    public static func == (a: Series, b: Series) -> Bool { a.id == b.id && a.points.count == b.points.count && a.points.last?.1 == b.points.last?.1 }
    public func hash(into h: inout Hasher) { h.combine(id) }
}

public struct EventMarker: Sendable, Identifiable, Hashable {
    public var id: String
    public var at: Date
    public var kind: String
    public var message: String
    public var severity: String
}

@MainActor
@Observable
public final class HistoryStore {
    public private(set) var series: [String: Series] = [:]
    public private(set) var events: [EventMarker] = []
    public private(set) var loading = false
    public private(set) var error: String?
    public private(set) var consumersAt: (group: String, at: Date, table: SysqTable)?
    @ObservationIgnored var client: SysqClientProtocol?

    public init() {}
    public func attach(_ c: SysqClientProtocol?) { client = c }

    nonisolated static func date(_ v: JSONValue?) -> Date? {
        if let s = v?.string {
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let d = f.date(from: s) { return d }
            f.formatOptions = [.withInternetDateTime]
            return f.date(from: s)
        }
        if let n = v?.double { return Date(timeIntervalSince1970: n) }
        return nil
    }

    /// Loads one series per group (its sparkline binding) over a range.
    public func load(_ groups: [GroupConfig], range: String) async {
        guard let client else { error = "sysqd isn't connected"; return }
        loading = true
        defer { loading = false }
        error = nil
        await withTaskGroup(of: (String, Series?).self) { tg in
            for g in groups {
                tg.addTask {
                    guard let t = try? await client.history(g.sparkline.recipe, range: range, column: g.sparkline.column, key: nil) else { return (g.id, nil) }
                    let pts = (0..<t.count).compactMap { i -> (Date, Double)? in
                        guard let d = HistoryStore.date(t.value(i, "t")), let v = t.double(i, "value") else { return nil }
                        return (d, v)
                    }
                    let unit = g.headline.column == g.sparkline.column ? g.headline.unit : nil
                    return (g.id, Series(id: g.id, label: g.name, unit: unit, columnType: "float", points: pts))
                }
            }
            for await (id, s) in tg { series[id] = s }
        }
        if let t = try? await client.events(range: range) {
            events = (0..<t.count).compactMap { i in
                guard let d = Self.date(t.value(i, "ts")) else { return nil }
                return EventMarker(id: "\(i):\(t.string(i, "ts") ?? "")", at: d, kind: t.string(i, "kind") ?? "",
                                   message: t.string(i, "message") ?? "", severity: t.string(i, "severity") ?? "info")
            }
        }
    }

    /// "What spiked at 3:14 pm?" — the group's consumer table nearest a time.
    public func consumers(group: String, at: Date) async {
        guard let client else { return }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        if let t = try? await client.consumersAt("consumers.\(group)", at: f.string(from: at)) {
            consumersAt = (group, at, t)
        }
    }

    public func clearConsumers() { consumersAt = nil }
}

@MainActor
@Observable
public final class PairingStore {
    public private(set) var pending: [PendingPairing] = []
    public private(set) var clients: [APIClientInfo] = []
    public private(set) var error: String?
    @ObservationIgnored let admin: SysqAdminProtocol
    @ObservationIgnored var poller: Task<Void, Never>?

    public init(admin: SysqAdminProtocol) { self.admin = admin }

    public func refresh() async {
        do {
            pending = try await admin.pending()
            clients = try await admin.clients().filter { $0.host != true }
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// Polls while the API settings are visible (new requests appear live).
    public func watch(_ on: Bool) {
        poller?.cancel()
        poller = nil
        guard on else { return }
        poller = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refresh()
                try? await Task.sleep(nanoseconds: 3_000_000_000)
            }
        }
    }

    public func approve(_ p: PendingPairing, code: String, privacy: String, allowScripts: Bool, origins: [String]) async -> Bool {
        do {
            try await admin.approve(id: p.id, code: code.trimmingCharacters(in: .whitespaces), privacy: privacy, allowScripts: allowScripts, origins: origins)
            await refresh()
            return true
        } catch {
            self.error = error.localizedDescription
            return false
        }
    }

    public func revoke(_ id: String) async {
        do { try await admin.revoke(id); await refresh() } catch { self.error = error.localizedDescription }
    }
}

/// Posts macOS notifications (implemented by the app).
public protocol Notifier: Sendable {
    func post(id: String, title: String, body: String)
}

/// Decides when a status change or an assistant STATUS deserves a
/// notification (notifications.yaml: levels, per-group overrides,
/// cooldown, quiet hours).
@MainActor
public final class NotificationStore {
    var last: [String: GroupStatus] = [:]
    var lastSent: [String: Date] = [:]
    let notifier: Notifier?
    let calendar: Calendar

    public init(notifier: Notifier?, calendar: Calendar = .current) {
        self.notifier = notifier
        self.calendar = calendar
    }

    func level(_ s: GroupStatus) -> String { s == .red ? "red" : s == .amber ? "amber" : s == .green ? "green" : "unknown" }

    public func inQuietHours(_ c: NotificationsFile, at now: Date) -> Bool {
        guard let from = c.quiet_hours?.from, let to = c.quiet_hours?.to else { return false }
        func minutes(_ s: String) -> Int? {
            let p = s.split(separator: ":").compactMap { Int($0) }
            return p.count == 2 ? p[0] * 60 + p[1] : nil
        }
        guard let a = minutes(from), let b = minutes(to) else { return false }
        let comps = calendar.dateComponents([.hour, .minute], from: now)
        let m = (comps.hour ?? 0) * 60 + (comps.minute ?? 0)
        return a <= b ? (m >= a && m < b) : (m >= a || m < b)
    }

    /// Feed every group's status here; returns the notifications it sent.
    @discardableResult
    public func observe(_ vms: [GroupViewModel], config c: NotificationsFile, now: Date = Date()) -> [String] {
        var sent: [String] = []
        for vm in vms {
            let prev = last[vm.id]
            last[vm.id] = vm.status
            guard c.enabled ?? true, let prev, prev != vm.status, vm.status > prev else { continue }
            guard c.levels(for: vm.id).contains(level(vm.status)) else { continue }
            if let t = lastSent[vm.id], now.timeIntervalSince(t) < (c.cooldown_minutes ?? 30) * 60 { continue }
            if inQuietHours(c, at: now) { continue }
            lastSent[vm.id] = now
            let body = vm.statusMessage ?? "\(vm.headline)\(vm.headlineLabel.map { " " + $0 } ?? "")"
            notifier?.post(id: "group.\(vm.id)", title: "\(vm.name): \(level(vm.status))", body: body)
            sent.append(vm.id)
        }
        return sent
    }

    /// The assistant's STATUS line.
    public func assistant(status: String, line: String, config c: NotificationsFile, now: Date = Date()) {
        guard c.enabled ?? true, (c.assistant ?? []).contains(status), !inQuietHours(c, at: now) else { return }
        notifier?.post(id: "assistant", title: "Headroom assistant: \(status.capitalized)", body: line)
    }
}
