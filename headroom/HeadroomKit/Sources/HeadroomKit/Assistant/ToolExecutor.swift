import Foundation

// Maps the assistant's tool calls to sysq calls and enforces the
// per-conversation budgets (fuel, call count) — no UI, no prompt text.

public struct Budget: Sendable, Hashable {
    public var fuel: Int
    public var maxCalls: Int
    public var maxSeconds: Double
    public var fuelUsed = 0
    public var calls = 0

    public init(fuel: Int, maxCalls: Int, maxSeconds: Double) {
        self.fuel = fuel; self.maxCalls = maxCalls; self.maxSeconds = maxSeconds
    }

    public var exhausted: String? {
        if calls >= maxCalls { return "query budget used up (\(maxCalls) calls)" }
        if fuelUsed >= fuel { return "fuel budget used up (\(fuel) units)" }
        return nil
    }
    public var fraction: Double { min(1, max(Double(fuelUsed) / Double(max(1, fuel)), Double(calls) / Double(max(1, maxCalls)))) }
}

/// What a tool call turned into.
public enum ToolOutcome: Sendable {
    case query(QueryRecord)
    case actionRequest(ParsedAction, explanation: String)
    case refused(String)
}

public enum ToolNames {
    // Tool names use underscores on the wire (some runtimes reject dots);
    // dotted spellings from the spec are accepted too.
    public static let describe = "sysq_describe"
    public static let runRecipe = "sysq_run_recipe"
    public static let run = "sysq_run"
    public static let explain = "sysq_explain"
    public static let requestAction = "headroom_request_action"

    static func normalize(_ n: String) -> String { n.replacingOccurrences(of: ".", with: "_") }

    public static func display(_ n: String) -> String {
        switch normalize(n) {
        case describe: return "sysq.describe"
        case runRecipe: return "sysq.run_recipe"
        case run: return "sysq.run"
        case explain: return "sysq.explain"
        case requestAction: return "headroom.request_action"
        default: return n
        }
    }
}

public actor ToolExecutor {
    let sysq: SysqClientProtocol
    public private(set) var budget: Budget
    let actionsEnabled: Bool
    var requestedAction = false
    var seq = 0

    public init(sysq: SysqClientProtocol, budget: Budget, actionsEnabled: Bool) {
        self.sysq = sysq
        self.budget = budget
        self.actionsEnabled = actionsEnabled
    }

    public func newTurn() { requestedAction = false }
    public func remaining() -> Budget { budget }

    public static func specs(actionsEnabled: Bool) -> [ToolSpec] {
        var out = [
            ToolSpec(name: ToolNames.describe, description: "Describe the sysq catalog (no name) or one recipe/primitive: its parameters and output columns.",
                     parameters: ["type": "object", "properties": ["name": ["type": "string", "description": "recipe or primitive name; omit for the catalog"]]]),
            ToolSpec(name: ToolNames.runRecipe, description: "Run a named read-only sysq recipe. Returns tables.",
                     parameters: ["type": "object", "required": ["name"], "properties": [
                        "name": ["type": "string", "description": "recipe name, e.g. mem.detail"],
                        "params": ["type": "object", "description": "recipe parameters, e.g. {\"by\": \"mem\", \"n\": 10}"]]]),
            ToolSpec(name: ToolNames.run, description: "Run a read-only sysq-script when no recipe fits. Costs fuel from the conversation budget.",
                     parameters: ["type": "object", "required": ["script"], "properties": ["script": ["type": "string", "description": "sysq-script, e.g. proc.top(by: \"mem\", n: 10) | select pid, command, mem"]]]),
            ToolSpec(name: ToolNames.explain, description: "Plan a sysq-script without running it: primitives, cost, sensitivity.",
                     parameters: ["type": "object", "required": ["script"], "properties": ["script": ["type": "string"]]]),
        ]
        if actionsEnabled {
            out.append(ToolSpec(name: ToolNames.requestAction,
                                description: "Ask the user to confirm ONE catalog action (at most one per answer). It runs only if they confirm; the result comes back later under ACTION RESULTS.",
                                parameters: ["type": "object", "required": ["name", "explanation"], "properties": [
                                    "name": ["type": "string", "description": "action name from AVAILABLE ACTIONS"],
                                    "params": ["type": "object"],
                                    "explanation": ["type": "string", "description": "what it changes, why, tier, undo"]]]))
        }
        return out
    }

    func record(_ call: String, _ r: RunResult?, error: String?) -> QueryRecord {
        seq += 1
        let items = r?.items ?? []
        let rows = items.reduce(0) { $0 + $1.table.count }
        let text = items.isEmpty ? (r?.error ?? "") : items.map { it in
            (it.label.map { "[\($0)]\n" } ?? "") + it.table.compact(maxRows: 15)
        }.joined(separator: "\n")
        return QueryRecord(id: "q\(seq)", call: call, rows: rows, cost: r?.meta?.cost_units ?? 0,
                           table: PromptBuilder.trim(text, max: 3000), error: error ?? r?.error, items: items)
    }

    static func describeText(_ v: JSONValue) -> String {
        PromptBuilder.trim(v.json, max: 3000)
    }

    /// Runs one tool call.
    public func execute(_ call: ToolCall) async -> ToolOutcome {
        let name = ToolNames.normalize(call.function.name)
        let args = call.function.arguments
        if name == ToolNames.requestAction {
            guard actionsEnabled else { return .refused("action requests are turned off") }
            guard !requestedAction else { return .refused("only one action request per answer") }
            guard let n = args["name"]?.string else { return .refused("request_action needs a name") }
            requestedAction = true
            return .actionRequest(ParsedAction(name: n, params: args["params"]?.object ?? [:]), explanation: args["explanation"]?.string ?? "")
        }
        if let why = budget.exhausted { return .refused(why + "; give your final answer now") }
        budget.calls += 1
        switch name {
        case ToolNames.describe:
            let target = args["name"]?.string ?? ""
            do {
                if target.isEmpty {
                    let cat = try await sysq.catalog().filter(\.available)
                    let text = cat.map { "\($0.name) [\($0.kind), \($0.cost ?? "?")] — \($0.doc)" }.joined(separator: "\n")
                    seq += 1
                    return .query(QueryRecord(id: "q\(seq)", call: "sysq.describe()", rows: cat.count, cost: 0, table: PromptBuilder.trim(text, max: 4000), error: nil, items: []))
                }
                let v = try await sysq.describe(target)
                seq += 1
                return .query(QueryRecord(id: "q\(seq)", call: "sysq.describe(\"\(target)\")", rows: 1, cost: 0, table: Self.describeText(v), error: nil, items: []))
            } catch {
                return .query(record("sysq.describe(\"\(target)\")", nil, error: error.localizedDescription))
            }
        case ToolNames.runRecipe:
            guard let recipe = args["name"]?.string ?? args["recipe"]?.string else { return .refused("run_recipe needs a name") }
            let params = args["params"]?.object ?? [:]
            let label = "sysq.run_recipe(\"\(recipe)\"\(params.isEmpty ? "" : ", " + JSONValue.object(params).json))"
            do {
                let r = try await sysq.runRecipe(recipe, params: params)
                budget.fuelUsed += r.meta?.cost_units ?? 1
                return .query(record(label, r, error: nil))
            } catch {
                return .query(record(label, nil, error: error.localizedDescription))
            }
        case ToolNames.run:
            guard let script = args["script"]?.string, !script.isEmpty else { return .refused("run needs a script") }
            let label = "sysq.run(\(JSONValue.string(script).json))"
            do {
                let r = try await sysq.run(script: script, budget: max(1, budget.fuel - budget.fuelUsed))
                budget.fuelUsed += r.meta?.cost_units ?? 1
                return .query(record(label, r, error: nil))
            } catch {
                return .query(record(label, nil, error: error.localizedDescription))
            }
        case ToolNames.explain:
            guard let script = args["script"]?.string, !script.isEmpty else { return .refused("explain needs a script") }
            let label = "sysq.explain(\(JSONValue.string(script).json))"
            do {
                let p = try await sysq.explain(script: script)
                let text = "cost: \(p.cost) (\(p.cost_units ?? 0) units), sensitivity: \(p.sensitivity), primitives: \((p.primitives ?? []).joined(separator: ", "))"
                    + ((p.unknown ?? []).isEmpty ? "" : "\nnotes: " + (p.unknown ?? []).joined(separator: "; "))
                    + "\nlowered:\n" + PromptBuilder.trim(p.lowered, max: 1500)
                seq += 1
                return .query(QueryRecord(id: "q\(seq)", call: label, rows: 0, cost: 0, table: text, error: nil, items: []))
            } catch {
                return .query(record(label, nil, error: error.localizedDescription))
            }
        default:
            budget.calls -= 1
            return .refused("unknown tool \(call.function.name)")
        }
    }

    /// Text-loop lookups use the same paths and budget.
    public func execute(_ lookup: ResponseParser.Lookup) async -> ToolOutcome {
        switch lookup {
        case .recipe(let a):
            return await execute(ToolCall(name: ToolNames.runRecipe, arguments: ["name": .string(a.name), "params": .object(a.params)]))
        case .script(let s):
            return await execute(ToolCall(name: ToolNames.run, arguments: ["script": .string(s)]))
        case .describe(let n):
            return await execute(ToolCall(name: ToolNames.describe, arguments: ["name": .string(n == "catalog" ? "" : n)]))
        }
    }
}
