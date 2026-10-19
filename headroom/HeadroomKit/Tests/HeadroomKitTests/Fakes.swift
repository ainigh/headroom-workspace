import Foundation
@testable import HeadroomKit

func loadShipped() -> AppConfig {
    let loader = ConfigLoader(userDir: tempDir(), defaultsDir: shippedConfig)
    try? loader.bootstrap()
    return loader.load(previous: nil).0
}

func table(_ cols: [(String, String)], _ rows: [[JSONValue]], unit: [String: String] = [:]) -> SysqTable {
    SysqTable(schema: cols.map { SysqColumn(name: $0.0, type: $0.1, unit: unit[$0.0]) }, rows: rows)
}

/// In-memory sysq: tables by recipe name; records every call.
final class FakeSysq: SysqClientProtocol, @unchecked Sendable {
    let lock = NSLock()
    var tables: [String: SysqTable] = [:]
    var calls: [String] = []
    var subscribeFails = false
    var catalogEntries: [CatalogEntry] = []

    func record(_ s: String) { lock.lock(); calls.append(s); lock.unlock() }
    func get(_ name: String) -> SysqTable { lock.lock(); defer { lock.unlock() }; return tables[name] ?? SysqTable(schema: [], rows: []) }
    func set(_ name: String, _ t: SysqTable) { lock.lock(); tables[name] = t; lock.unlock() }

    func status() async throws -> SysqStatus { throw ClientError(.failed, "n/a") }
    func catalog() async throws -> [CatalogEntry] { catalogEntries }
    func describe(_ name: String) async throws -> JSONValue { record("describe \(name)"); return ["name": .string(name)] }
    func runRecipe(_ name: String, params: [String: JSONValue]) async throws -> RunResult {
        record("run \(name)\(params.isEmpty ? "" : " " + JSONValue.object(params).json)")
        return RunResult(items: [SysqItem(label: name, table: get(name))], meta: RunMeta(cost_units: 5, calls: 1, primitives: nil, duration_ms: 1))
    }
    func run(script: String, budget: Int?) async throws -> RunResult {
        record("script \(script)")
        return RunResult(items: [SysqItem(table: get("script"))], meta: RunMeta(cost_units: 20))
    }
    func explain(script: String) async throws -> ExplainPlan {
        ExplainPlan(lowered: "x", primitives: ["sys.memory"], recipes: nil, cost: "cheap", cost_units: 1, sensitivity: "public")
    }
    func explain(recipe: String) async throws -> ExplainPlan { try await explain(script: recipe) }
    func history(_ name: String, range: String, column: String?, key: String?) async throws -> SysqTable {
        record("history \(name) \(range) \(column ?? "")")
        return table([("t", "ts"), ("value", "float")], [["2026-10-19T12:00:00Z", 1], ["2026-10-19T12:00:05Z", 2]])
    }
    func events(range: String) async throws -> SysqTable { table([("ts", "ts"), ("kind", "str"), ("message", "str")], []) }
    func narrative(range: String) async throws -> Narrative { Narrative(text: "Memory pressure rose to critical 40 minutes ago.", table: SysqTable(schema: [], rows: [])) }
    func consumersAt(_ name: String, at: String) async throws -> SysqTable { get(name) }
    func attention(min: String?) async throws -> SysqTable {
        table([("rule", "str"), ("severity", "str"), ("message", "str")], [["attention.memory", "medium", "Memory pressure 92%"]])
    }
    func subscribe(_ name: String, params: [String: JSONValue], every: Double) -> AsyncThrowingStream<SubscriptionUpdate, Error> {
        record("subscribe \(name)\(params.isEmpty ? "" : " " + JSONValue.object(params).json) every \(Int(every))")
        let fail = subscribeFails
        let t = get(name)
        return AsyncThrowingStream { c in
            if fail { c.finish(throwing: ClientError(.unreachable, "no")); return }
            c.yield(SubscriptionUpdate(key: name, name: name, ts: "", items: [SysqItem(table: t)]))
        }
    }
}

/// Replays scripted replies; records what it was sent.
final class FakeChat: ChatBackend, @unchecked Sendable {
    let lock = NSLock()
    var replies: [ChatMessage]
    var received: [[ChatMessage]] = []
    var receivedTools: [[ToolSpec]?] = []
    var tools = true

    init(_ replies: [ChatMessage]) { self.replies = replies }

    func next(_ messages: [ChatMessage], _ tools: [ToolSpec]?) -> ChatMessage? {
        lock.lock(); defer { lock.unlock() }
        received.append(messages)
        receivedTools.append(tools)
        return replies.isEmpty ? nil : replies.removeFirst()
    }

    func chat(model: String, messages: [ChatMessage], tools: [ToolSpec]?, keepAlive: String?) async throws -> ChatReply {
        guard let m = next(messages, tools) else { throw ClientError(.failed, "no more scripted replies") }
        return ChatReply(message: m)
    }
    func models() async throws -> [ModelInfo] { [ModelInfo(name: "qwen3:8b", size: 5e9)] }
    func loaded() async throws -> [LoadedModel] { [LoadedModel(name: "qwen3:8b")] }
    func supportsTools(model: String) async throws -> Bool { tools }
    func unload(model: String) async throws {}
}

final class FakeSysact: SysactClientProtocol, @unchecked Sendable {
    var executed: [(ActionPlan, ConfirmationToken)] = []
    var catalogList: [ActionInfo] = []

    func catalog() async throws -> [ActionInfo] { catalogList }
    func status() async throws -> JSONValue { ["dry_run": false] }
    func prepare(_ name: String, params: [String: JSONValue]) async throws -> ActionPlan {
        guard let a = catalogList.first(where: { $0.name == name }) else { throw ClientError(.failed, "unknown action \(name)") }
        return ActionPlan(plan_id: "plan-1", plan_hash: "hash-1", name: name, doc: a.doc, tier: a.tier,
                          params: params.mapValues { $0.string ?? $0.description }, effect: a.effect, undo_hint: a.undo_hint,
                          requires_admin: a.requires_admin, command: "ollama stop", expires_at: "2026-10-19T12:10:00Z")
    }
    func execute(_ plan: ActionPlan, confirmation: ConfirmationToken) async throws -> ActionOutcome {
        executed.append((plan, confirmation))
        return ActionOutcome(plan_id: plan.plan_id, name: plan.name, ok: true, exit_code: 0, output: "unloaded \(plan.params["model"] ?? "")")
    }
}

func action(_ name: String, tier: String = "caution", params: [String: ActionParamSpec] = [:], effect: String = "does it") -> ActionInfo {
    ActionInfo(name: name, doc: "\(name) doc", tier: tier, requires_admin: false, params: params, param_order: params.keys.sorted(), effect: effect, undo_hint: "redo")
}

func spec(_ type: String, required: Bool = true) -> ActionParamSpec {
    ActionParamSpec(type: type, values: nil, default: nil, min: nil, max: nil, pattern: nil, required: required, doc: nil)
}

/// A clock tests can move.
final class TestClock: @unchecked Sendable {
    var now = Date(timeIntervalSince1970: 1_800_000_000)
}

@MainActor
func waitUntil(_ timeout: Double = 5, _ cond: () -> Bool) async -> Bool {
    let end = Date().addingTimeInterval(timeout)
    while Date() < end {
        if cond() { return true }
        try? await Task.sleep(nanoseconds: 10_000_000)
    }
    return cond()
}
