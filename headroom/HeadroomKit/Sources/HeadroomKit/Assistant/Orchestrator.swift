import Foundation

// One assistant turn (implementation.md §5.4): build the request, let the
// model investigate through tools (or the text loop), enforce budgets,
// parse the five sections. It depends on protocols only.

public struct AssistantRequest: Sendable {
    public var question: String
    public var mode: String
    public var scope: AssistantScope
    public var keyIDs: [String]

    public init(question: String, mode: String, scope: AssistantScope, keyIDs: [String]) {
        self.question = question; self.mode = mode; self.scope = scope; self.keyIDs = keyIDs
    }
}

public enum AssistantEvent: Sendable {
    case progress(String)
    case toolCall(String)
    case toolResult(QueryRecord)
    case refused(String)
    case warning(String)
    case response(AssistantResponse)
    case done(TurnRecord)
    case failed(String)
}

/// Conversation state carried between turns (never the model's messages).
public struct Conversation: Sendable {
    public var queries: [QueryRecord] = []
    public var actionResults: [ActionResult] = []
    public var budget: Budget
    public init(budget: Budget) { self.budget = budget }
}

public struct AssistantOrchestrator: Sendable {
    let chat: ChatBackend
    let sysq: SysqClientProtocol
    let actions: ActionRequesting
    let summaries: SummaryHistory

    public init(chat: ChatBackend, sysq: SysqClientProtocol, actions: ActionRequesting, summaries: SummaryHistory) {
        self.chat = chat; self.sysq = sysq; self.actions = actions; self.summaries = summaries
    }

    /// Whether to use tool calling for this model (assistant.yaml `tools`).
    public func useTools(_ c: AppConfig, model: String) async -> Bool {
        switch c.assistant.tools ?? "auto" {
        case "on": return true
        case "off": return false
        default: return (try? await chat.supportsTools(model: model)) ?? false
        }
    }

    /// The scope snapshot (Headroom's own calls; they don't use the budget).
    func snapshot(_ c: AppConfig, _ req: AssistantRequest) async -> (state: String, attention: String, history: String) {
        let groups = req.scope.groups(c, keyIDs: req.keyIDs)
        var calls: [(String, String, [String: JSONValue])] = []
        if case .item(let item) = req.scope, let profile = c.groups.item_profile {
            calls.append(("\(profile)(kind=\"\(item.kind)\", id=\"\(item.id)\")", profile, ["kind": .string(item.kind), "id": .string(item.id)]))
        }
        var seen = Set<String>()
        for g in groups {
            for b in [(g.headline.recipe, g.headline.params), (g.detail.recipe, g.detail.params), (g.consumers.recipe, g.consumers.params)] {
                let key = FeedSpec.key(b.0, b.1)
                if seen.insert(key).inserted {
                    let label = b.1.map { p in "\(b.0)(\(p.keys.sorted().map { "\($0)=\(p[$0]!.json)" }.joined(separator: ", ")))" } ?? b.0
                    calls.append((label, b.0, b.1 ?? [:]))
                }
            }
        }
        let sysq = self.sysq
        var parts: [(Int, String, SysqTable)] = []
        await withTaskGroup(of: (Int, String, SysqTable?).self) { tg in
            for (i, call) in calls.enumerated() {
                tg.addTask {
                    let r = try? await sysq.runRecipe(call.1, params: call.2)
                    return (i, call.0, r.map { res in res.items.count > 1 ? Self.merge(res.items) : res.table })
                }
            }
            for await (i, label, t) in tg { if let t { parts.append((i, label, t)) } }
        }
        parts.sort { $0.0 < $1.0 }
        let state = PromptBuilder.snapshot(parts.map { ($0.1, $0.2) })

        var attention = ""
        if let t = try? await sysq.attention(min: nil) {
            // Group rules outside the scope are left out; other rules apply everywhere.
            let rules = Set(groups.map(\.attention))
            let groupRules = Set(c.groups.groups.map(\.attention))
            let keep = (0..<t.count).filter { i in
                guard let rule = t.string(i, "rule") else { return true }
                return rules.contains(rule) || !groupRules.contains(rule)
            }
            attention = keep.map { i in "\(t.string(i, "severity") ?? "info"): \(t.string(i, "message") ?? "") (\(t.string(i, "rule") ?? ""))" }.joined(separator: "\n")
        }
        var history = ""
        if let n = try? await sysq.narrative(range: c.assistant.narrative_range ?? "2h") { history = n.text }
        return (state, attention, history)
    }

    /// Several emitted items read as one labeled block.
    static func merge(_ items: [SysqItem]) -> SysqTable {
        let text = items.map { ($0.label.map { "\($0): " } ?? "") + $0.table.compact(maxRows: 8) }.joined(separator: "\n")
        return SysqTable(schema: [SysqColumn(name: "output", type: "str")], rows: [[.string(text)]])
    }

    func catalogs(_ c: AppConfig) async -> ([CatalogEntry], [ActionInfo]) {
        // Group status rules are Headroom's plumbing, not tools for the model.
        let groupRules = Set(c.groups.groups.map(\.attention))
        let recipes = ((try? await sysq.catalog()) ?? []).filter { $0.kind == "recipe" && $0.available && !groupRules.contains($0.name) }
        let acts = (c.assistant.action_requests ?? true) ? await actions.catalogIndex() : []
        return (recipes, acts)
    }

    /// Runs one turn, streaming events. `conversation` is updated in place.
    public func run(_ req: AssistantRequest, config c: AppConfig, model: String, conversation: inout Conversation,
                    emit: @Sendable (AssistantEvent) -> Void) async -> AssistantResponse? {
        let start = Date()
        let maxSeconds = c.assistant.max_turn_seconds ?? 90
        let actionsOn = c.assistant.action_requests ?? true
        emit(.progress("Looking at the current state"))
        async let snap = snapshot(c, req)
        async let cats = catalogs(c)
        let tools = await useTools(c, model: model)
        let (state, attention, history) = await snap
        let (recipes, actionIndex) = await cats
        let window = c.assistant.summary_window ?? 10
        let executor = ToolExecutor(sysq: sysq, budget: conversation.budget, actionsEnabled: actionsOn)
        let n = c.notices

        func request() -> String {
            PromptBuilder.render(template: c.requestTemplate, PromptInputs(
                mode: c.modes.modes[req.mode]?.label ?? req.mode, scope: req.scope.label(c), time: Date(), privacy: c.assistant.privacy,
                currentState: state, attention: attention, history: history,
                summaries: summaries.window(window), summaryWindow: window,
                queries: conversation.queries, actionResults: conversation.actionResults,
                recipes: recipes, actions: actionIndex, question: req.question))
        }
        var system = c.systemPrompt
        if !tools { system += "\n\n" + c.textToolsPrompt }
        var messages: [ChatMessage] = [.system(system), .user(request())]
        let specs = tools ? ToolExecutor.specs(actionsEnabled: actionsOn) : nil
        var toolAction: (ParsedAction, String)?
        var calls: [CallRecord] = []
        var final: AssistantResponse?
        var warnedTime = false
        var warnedBudget = false
        let keepAlive = c.assistant.unload_after_minutes.map { "\(Int($0))m" }

        loop: for _ in 0..<((c.assistant.max_tool_calls ?? 25) + 6) {
            if Task.isCancelled { emit(.failed("Stopped.")); break }
            if Date().timeIntervalSince(start) > maxSeconds {
                if warnedTime { emit(.failed("The turn ran out of time.")); break }
                warnedTime = true
                messages.append(.user(n["time_up"]))
            }
            emit(.progress("Thinking"))
            let reply: ChatReply
            do {
                reply = try await chat.chat(model: model, messages: messages, tools: warnedTime ? nil : specs, keepAlive: keepAlive)
            } catch {
                if Task.isCancelled { emit(.failed("Stopped.")); break }
                emit(.failed(error.localizedDescription))
                break
            }
            let m = reply.message
            if tools, let tcs = m.tool_calls, !tcs.isEmpty {
                messages.append(ChatMessage(role: "assistant", content: m.content, tool_calls: tcs))
                for tc in tcs {
                    if Task.isCancelled { break loop }
                    emit(.toolCall(ToolNames.display(tc.function.name)))
                    let out = await executor.execute(tc)
                    switch out {
                    case .query(let q):
                        conversation.queries.append(q)
                        calls.append(CallRecord(call: q.call, rows: q.rows, cost: q.cost, error: q.error))
                        emit(.toolResult(q))
                        messages.append(.tool(tc.function.name, n["data_prefix"] + "\n" + (q.error.map { "error: \($0)" } ?? "\(q.rows) rows\n" + q.table)))
                    case .actionRequest(let a, let why):
                        if toolAction == nil { toolAction = (a, why) }
                        messages.append(.tool(tc.function.name, n["action_noted"]))
                    case .refused(let why):
                        emit(.refused(why))
                        messages.append(.tool(tc.function.name, "refused: \(why)"))
                    }
                }
                let b = await executor.remaining()
                if b.exhausted != nil, !warnedBudget { warnedBudget = true; messages.append(.user(n["budget_used"])) }
                continue
            }
            if !tools, let lk = ResponseParser.lookup(m.content) {
                messages.append(ChatMessage(role: "assistant", content: m.content))
                let out = await executor.execute(lk)
                switch out {
                case .query(let q):
                    emit(.toolCall(q.call))
                    conversation.queries.append(q)
                    calls.append(CallRecord(call: q.call, rows: q.rows, cost: q.cost, error: q.error))
                    emit(.toolResult(q))
                    messages.append(.user(n["query_results"] + "\n" + n["data_prefix"] + "\n" + q.call + " → " + (q.error.map { "error: \($0)" } ?? "\(q.rows) rows\n" + q.table)))
                case .refused(let why):
                    emit(.refused(why))
                    messages.append(.user(n["query_results"] + "\nrefused: \(why)\n" + n["budget_used"]))
                case .actionRequest:
                    break
                }
                continue
            }
            var r = ResponseParser.parse(m.content, allowedActions: Set(actionIndex.map(\.name)), actionsEnabled: actionsOn)
            if r.actionRequest == nil, r.actionProblem == nil, let (a, why) = toolAction {
                if actionIndex.contains(where: { $0.name == a.name }) {
                    r.actionRequest = a
                    if r.actionExplanation == nil, !why.isEmpty { r.actionExplanation = why }
                } else {
                    r.actionProblem = "\(a.name) is not in the action catalog"
                }
            }
            final = r
            break
        }
        conversation.budget = await executor.remaining()
        guard let r = final else { return nil }
        emit(.response(r))
        let turn = TurnRecord(at: start, scope: req.scope.label(c), mode: req.mode, model: model, question: req.question,
                              summary: r.summary, status: r.status?.rawValue, calls: calls,
                              actionRequest: r.actionRequest?.text ?? r.actionProblem, actionExplanation: r.actionExplanation,
                              decision: r.actionRequest == nil ? nil : "pending", actionResult: nil, incomplete: r.incomplete)
        if let s = r.summary {
            summaries.append(SummaryRecord(at: start, scope: turn.scope, mode: req.mode, status: r.status?.rawValue, summary: s, action: r.actionRequest?.text))
        }
        emit(.done(turn))
        return r
    }

    /// Checks memory before a model loads (headroom-spec §8.8). Returns a
    /// warning to show, or nil when it fits or is already loaded.
    public func preflight(_ c: AppConfig, model: String) async -> String? {
        guard let recipe = c.assistant.preflight_recipe else { return nil }
        if let loaded = try? await chat.loaded(), loaded.contains(where: { $0.name == model }) { return nil }
        guard let size = (try? await chat.models())?.first(where: { $0.name == model })?.size, size > 0 else { return nil }
        guard let r = try? await sysq.runRecipe(recipe, params: ["memory": .number(size)]) else { return nil }
        let t = r.table
        if t.value(0, "fits")?.bool == true { return nil }
        let verdict = t.string(0, "verdict") ?? "tight"
        let avail = t.text(0, "memory_available")
        return "Loading \(model) (\(Units.bytes(size))): \(verdict); \(avail) available. A smaller model may be better."
    }
}
