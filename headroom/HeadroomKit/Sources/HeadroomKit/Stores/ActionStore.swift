import Foundation
import Observation

// The mutation path (separation-of-concerns §6): prepare → ConfirmationFlow
// → execute with the token. The UI and the assistant both come through here.

/// One finished (or declined) action, for toasts, the log and the
/// assistant's ACTION RESULTS.
public struct ActionResult: Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var params: [String: String]
    public var requestedBy: ActionRequester
    public var approved: Bool
    public var ok: Bool
    public var output: String
    public var at: Date
    /// The sheet was never shown (another confirmation was open): nobody declined anything.
    public var notShown = false

    public var callText: String {
        "\(name)(\(params.keys.sorted().map { "\($0)=\"\(params[$0]!)\"" }.joined(separator: ", ")))"
    }
}

/// An item someone selected (a consumer row, a file, a model).
public struct ItemRef: Sendable, Hashable, Identifiable {
    public var kind: String
    public var id: String
    public var name: String
    public var groupID: String?

    public init(kind: String, id: String, name: String, groupID: String? = nil) {
        self.kind = kind; self.id = id; self.name = name; self.groupID = groupID
    }
}

extension ItemRef {
    /// Action parameter names an item kind fills in.
    static let paramForKind: [String: [String]] = ["process": ["pid"], "path": ["path"], "model": ["model"], "app": ["app", "bundle_id"]]
}

/// Lets the assistant ask for an action without knowing the store.
public protocol ActionRequesting: Sendable {
    func catalogIndex() async -> [ActionInfo]
    /// Prepares the action, shows the confirmation and runs it if confirmed.
    func requestAction(_ name: String, params: [String: JSONValue], explanation: String) async -> ActionResult
}

@MainActor
@Observable
public final class ActionStore {
    public private(set) var catalog: [ActionInfo] = []
    public private(set) var catalogError: String?
    public private(set) var recent: [ActionResult] = []
    /// The latest result, shown as a toast.
    public var toast: ActionResult?
    public private(set) var busy: String?

    public let flow: ConfirmationFlow
    @ObservationIgnored var client: SysactClientProtocol

    public init(client: SysactClientProtocol, flow: ConfirmationFlow) {
        self.client = client
        self.flow = flow
    }

    public func loadCatalog() async {
        do {
            catalog = try await client.catalog()
            catalogError = nil
        } catch {
            catalogError = error.localizedDescription
        }
    }

    public func action(_ name: String) -> ActionInfo? { catalog.first { $0.name == name } }

    /// Actions that fit an item: every required parameter can be filled from
    /// the item (its kind, id, or a same-named field of its profile).
    public func actions(for item: ItemRef, profile: [String: String] = [:], preferred: [String] = []) -> [(ActionInfo, [String: String])] {
        var out: [(ActionInfo, [String: String])] = []
        for a in catalog {
            let fill = prefill(a, item: item, profile: profile)
            let required = (a.params ?? [:]).filter { $0.value.required == true }.keys
            guard !required.isEmpty, required.allSatisfy({ fill[$0] != nil }) else { continue }
            out.append((a, fill))
        }
        // Mode-bound actions first, then by tier (caution before risky).
        let rank = ["safe": 0, "caution": 1, "risky": 2]
        return out.sorted { x, y in
            let px = preferred.firstIndex(of: x.0.name) ?? Int.max, py = preferred.firstIndex(of: y.0.name) ?? Int.max
            if px != py { return px < py }
            let tx = rank[x.0.tier] ?? 3, ty = rank[y.0.tier] ?? 3
            return tx == ty ? x.0.name < y.0.name : tx < ty
        }
    }

    public func prefill(_ a: ActionInfo, item: ItemRef, profile: [String: String]) -> [String: String] {
        var fill: [String: String] = [:]
        for p in a.orderedParams {
            if ItemRef.paramForKind[item.kind]?.contains(p) == true, !(p == "bundle_id" && !item.id.contains(".")) {
                fill[p] = item.id
            } else if let v = profile[p], !v.isEmpty {
                fill[p] = v
            } else if let d = a.params?[p]?.default?.string {
                fill[p] = d
            }
        }
        return fill
    }

    /// The whole confirmed path for one action.
    public func run(_ name: String, params: [String: JSONValue], requestedBy: ActionRequester, explanation: String? = nil) async -> ActionResult {
        let shown = params.mapValues { $0.string ?? $0.description }
        func result(_ approved: Bool, _ ok: Bool, _ out: String, _ p: [String: String]) -> ActionResult {
            let r = ActionResult(id: UUID().uuidString, name: name, params: p, requestedBy: requestedBy, approved: approved, ok: ok, output: out, at: Date())
            recent.insert(r, at: 0)
            if recent.count > 50 { recent.removeLast() }
            toast = r
            return r
        }
        let plan: ActionPlan
        do {
            busy = name
            defer { busy = nil }
            plan = try await client.prepare(name, params: params)
        } catch {
            return result(false, false, "Couldn't prepare \(name): \(error.localizedDescription)", shown)
        }
        if flow.current != nil {
            var r = ActionResult(id: UUID().uuidString, name: name, params: plan.params, requestedBy: requestedBy, approved: false, ok: false,
                                 output: "Another confirmation is open; finish that one first.", at: Date())
            r.notShown = true
            toast = r
            return r
        }
        guard let token = await flow.request(plan, requestedBy: requestedBy, explanation: explanation) else {
            return result(false, false, "Declined.", plan.params)
        }
        busy = name
        defer { busy = nil }
        do {
            let out = try await client.execute(plan, confirmation: token)
            return result(true, out.ok, out.output.isEmpty ? (out.ok ? "Done." : "Exited with \(out.exit_code).") : out.output, plan.params)
        } catch {
            return result(true, false, error.localizedDescription, plan.params)
        }
    }
}

/// Bridges the assistant (off the main actor) to the store.
public struct ActionBridge: ActionRequesting {
    let store: ActionStore
    public init(_ store: ActionStore) { self.store = store }

    public func catalogIndex() async -> [ActionInfo] { await MainActor.run { store.catalog } }

    public func requestAction(_ name: String, params: [String: JSONValue], explanation: String) async -> ActionResult {
        await store.run(name, params: params, requestedBy: .assistant, explanation: explanation)
    }
}
