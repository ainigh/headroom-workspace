import Foundation
import Observation

/// One question and its answer, as the assistant panel shows it.
public struct AssistantTurn: Sendable, Identifiable {
    public var id: String
    public var question: String
    public var scope: AssistantScope
    public var mode: String
    public var keyIDs: [String] = []
    public var trace: [QueryRecord] = []
    public var notes: [String] = []
    public var progress: String?
    public var response: AssistantResponse?
    public var error: String?
    public var running = true
    public var actionResult: ActionResult?
    public var actionInfo: ActionInfo?
    /// The log record, written once the action is decided (or not needed).
    var record: TurnRecord?
}

@MainActor
@Observable
public final class AssistantStore {
    public private(set) var turns: [AssistantTurn] = []
    public private(set) var running = false
    public private(set) var model: String = ""
    public private(set) var models: [ModelInfo] = []
    public private(set) var fuel: Double = 0
    public private(set) var warning: String?
    public private(set) var summaryList: [SummaryRecord] = []
    /// The last STATUS, for the badge.
    public var status: AssistantStatus? { turns.last(where: { $0.response?.status != nil })?.response?.status }

    public let summaries: SummaryHistory
    @ObservationIgnored let log: AssistantLog
    @ObservationIgnored var orchestrator: AssistantOrchestrator?
    @ObservationIgnored var chat: ChatBackend?
    @ObservationIgnored var actions: ActionRequesting?
    @ObservationIgnored var config: AppConfig?
    @ObservationIgnored var conversation: Conversation?
    @ObservationIgnored var task: Task<Void, Never>?
    @ObservationIgnored var preflighted = Set<String>()
    /// Called with each finished answer's STATUS (notifications).
    @ObservationIgnored public var onStatus: ((AssistantStatus, String) -> Void)?

    public init(summaries: SummaryHistory, log: AssistantLog) {
        self.summaries = summaries
        self.log = log
        summaryList = summaries.all()
    }

    public func attach(sysq: SysqClientProtocol?, chat: ChatBackend, actions: ActionRequesting, config: AppConfig) {
        self.chat = chat
        self.actions = actions
        self.config = config
        orchestrator = sysq.map { AssistantOrchestrator(chat: chat, sysq: $0, actions: actions, summaries: summaries) }
        update(config: config)
        log.prune(olderThan: config.retention.assistant_log_days ?? 180)
    }

    public func update(config: AppConfig) {
        self.config = config
        if let m = config.assistant.model, !m.isEmpty { model = m }
    }

    public var enabled: Bool { config?.assistant.enabled ?? true }
    public var available: Bool { orchestrator != nil && enabled }

    public func loadModels() async {
        guard let chat else { return }
        do {
            models = try await chat.models()
            warning = nil
            if model.isEmpty, let first = models.first { model = first.name }
        } catch {
            warning = "Ollama isn't answering (\(error.localizedDescription)). Start Ollama to use the assistant."
        }
    }

    /// Picks the model for this session (Settings saves it to assistant.yaml).
    public func setModel(_ m: String) { model = m }

    /// Starts a new conversation (scope or mode changed, or the panel closed).
    public func reset() {
        stop()
        flushLogs()
        turns = []
        conversation = nil
        fuel = 0
    }

    public func ask(_ question: String?, mode: String, scope: AssistantScope, keyIDs: [String]) {
        guard let c = config, let orchestrator, !running else { return }
        let q = (question?.trimmingCharacters(in: .whitespacesAndNewlines)).flatMap { $0.isEmpty ? nil : $0 } ?? c.modes.modes[mode]?.question ?? ""
        guard !model.isEmpty else { warning = "Choose a model in Settings → Assistant."; return }
        if conversation == nil {
            conversation = Conversation(budget: Budget(fuel: c.assistant.fuel_budget ?? 2000, maxCalls: c.assistant.max_tool_calls ?? 25,
                                                       maxSeconds: c.assistant.max_turn_seconds ?? 90))
        }
        flushLogs()
        var turn = AssistantTurn(id: UUID().uuidString, question: q, scope: scope, mode: mode)
        turn.keyIDs = keyIDs
        turns.append(turn)
        running = true
        let model = self.model
        let req = AssistantRequest(question: q, mode: mode, scope: scope, keyIDs: keyIDs)
        let id = turn.id
        task = Task { [weak self] in
            guard let self else { return }
            if !self.preflighted.contains(model) {
                self.preflighted.insert(model)
                if let w = await orchestrator.preflight(c, model: model) { self.apply(id, .warning(w)) }
            }
            let start = self.conversation ?? Conversation(budget: Budget(fuel: 2000, maxCalls: 25, maxSeconds: 90))
            let (events, sink) = AsyncStream<AssistantEvent>.makeStream()
            let worker = Task.detached { () -> Conversation in
                var conv = start
                _ = await orchestrator.run(req, config: c, model: model, conversation: &conv) { sink.yield($0) }
                sink.finish()
                return conv
            }
            await withTaskCancellationHandler {
                for await e in events { self.apply(id, e) }
            } onCancel: {
                worker.cancel()
            }
            let conv = await worker.value
            self.conversation = conv
            self.fuel = conv.budget.fraction
            self.finish(id)
        }
    }

    func index(_ id: String) -> Int? { turns.firstIndex { $0.id == id } }

    func apply(_ id: String, _ e: AssistantEvent) {
        guard let i = index(id) else { return }
        switch e {
        case .progress(let p): turns[i].progress = p
        case .toolCall(let c): turns[i].progress = "Running \(c)"
        case .toolResult(let q): turns[i].trace.append(q)
        case .refused(let why): turns[i].notes.append(why)
        case .warning(let w): turns[i].notes.append(w)
        case .response(let r):
            turns[i].response = r
            if let a = r.actionRequest { Task { await self.lookupAction(id, a.name) } }
            if let s = r.status { onStatus?(s, r.statusLine ?? r.summary ?? "") }
        case .done(let rec):
            turns[i].record = rec
            summaryList = summaries.all()
            if rec.decision == nil { log.append(rec); turns[i].record = nil }
        case .failed(let m): turns[i].error = m
        }
    }

    func lookupAction(_ id: String, _ name: String) async {
        let info = await actions?.catalogIndex().first { $0.name == name }
        if let i = index(id) { turns[i].actionInfo = info }
    }

    func finish(_ id: String) {
        if let i = index(id) {
            turns[i].running = false
            turns[i].progress = nil
            if turns[i].response == nil, turns[i].error == nil { turns[i].error = "No answer." }
        }
        running = false
        task = nil
    }

    /// Stop ends the exchange at any point.
    public func stop() {
        task?.cancel()
    }

    /// The Confirm… button on an action card: opens the one confirmation
    /// sheet; if it runs, the model answers again with the result.
    public func confirmAction(_ turnID: String) {
        guard let i = index(turnID), let r = turns[i].response, let a = r.actionRequest, turns[i].actionResult == nil, let actions else { return }
        let explanation = r.actionExplanation ?? ""
        Task {
            let result = await actions.requestAction(a.name, params: a.params, explanation: explanation)
            guard let j = index(turnID) else { return }
            turns[j].actionResult = result
            conversation?.actionResults.append(result)
            if var rec = turns[j].record {
                rec.decision = result.approved ? "approved" : "declined"
                rec.actionResult = result.approved ? String(result.output.prefix(2000)) : nil
                log.append(rec)
                turns[j].record = nil
            }
            if result.approved {
                let t = turns[j]
                ask(t.question, mode: t.mode, scope: t.scope, keyIDs: t.keyIDs)
            }
        }
    }

    /// Declines without opening the sheet (the card's ✕).
    public func dismissAction(_ turnID: String) {
        guard let i = index(turnID), let a = turns[i].response?.actionRequest, turns[i].actionResult == nil else { return }
        let r = ActionResult(id: UUID().uuidString, name: a.name, params: a.params.mapValues { $0.string ?? $0.description },
                             requestedBy: .assistant, approved: false, ok: false, output: "Declined.", at: Date())
        turns[i].actionResult = r
        conversation?.actionResults.append(r)
        if var rec = turns[i].record { rec.decision = "declined"; log.append(rec); turns[i].record = nil }
    }

    func flushLogs() {
        for i in turns.indices { if let rec = turns[i].record { log.append(rec); turns[i].record = nil } }
    }

    // MARK: summary history

    public func refreshSummaries() { summaryList = summaries.all() }
    public func deleteSummaries(_ ids: Set<String>) { summaries.delete(ids: ids); refreshSummaries() }
    public func deleteSummaries(before d: Date) { summaries.delete(before: d); refreshSummaries() }
    public func deleteAllSummaries() { summaries.deleteAll(); refreshSummaries() }
}
