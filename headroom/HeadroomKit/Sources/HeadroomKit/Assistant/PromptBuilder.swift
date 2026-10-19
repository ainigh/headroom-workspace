import Foundation

// Pure prompt construction (separation-of-concerns §7): config, a scope
// snapshot, the narrative, summaries and this conversation's results in;
// text out. The words live in config/prompts/.

public enum AssistantScope: Sendable, Hashable {
    case keyGroups, allGroups
    case group(String)
    case item(ItemRef)

    public func label(_ c: AppConfig) -> String {
        switch self {
        case .keyGroups: return "key groups"
        case .allGroups: return "all groups"
        case .group(let id): return c.group(id)?.name ?? id
        case .item(let i): return "\(i.kind) \(i.name)"
        }
    }

    /// The groups whose recipes form the snapshot.
    public func groups(_ c: AppConfig, keyIDs: [String]) -> [GroupConfig] {
        switch self {
        case .keyGroups: return keyIDs.compactMap { c.group($0) }
        case .allGroups: return c.orderedGroups.filter { !(c.layout.hidden_groups ?? []).contains($0.id) }
        case .group(let id): return c.group(id).map { [$0] } ?? []
        case .item(let i): return i.groupID.flatMap { c.group($0) }.map { [$0] } ?? []
        }
    }
}

/// One sysq call made during a conversation, as the model will see it.
public struct QueryRecord: Sendable, Hashable, Identifiable {
    public var id: String
    public var call: String
    public var rows: Int
    public var cost: Int
    public var table: String
    public var error: String?
    /// The result items, for the trace's preview tables.
    public var items: [SysqItem]
}

public struct PromptInputs: Sendable {
    public var mode: String
    public var scope: String
    public var time: Date
    public var privacy: String
    public var currentState: String
    public var attention: String
    public var history: String
    public var summaries: [SummaryRecord]
    public var summaryWindow: Int
    public var queries: [QueryRecord]
    public var actionResults: [ActionResult]
    public var recipes: [CatalogEntry]
    public var actions: [ActionInfo]
    public var question: String
    public var timeZone: TimeZone = .current

    public init(mode: String, scope: String, time: Date, privacy: String, currentState: String, attention: String, history: String,
                summaries: [SummaryRecord], summaryWindow: Int, queries: [QueryRecord], actionResults: [ActionResult],
                recipes: [CatalogEntry], actions: [ActionInfo], question: String) {
        self.mode = mode; self.scope = scope; self.time = time; self.privacy = privacy; self.currentState = currentState
        self.attention = attention; self.history = history; self.summaries = summaries; self.summaryWindow = summaryWindow
        self.queries = queries; self.actionResults = actionResults; self.recipes = recipes; self.actions = actions; self.question = question
    }
}

public enum PromptBuilder {
    public static func privacyLabel(_ p: String) -> String {
        switch p {
        case "aggregates": return "aggregates"
        case "files": return "aggregates +consumers +files"
        default: return "aggregates +consumers"
        }
    }

    static func stamp(_ d: Date, _ tz: TimeZone) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = tz
        f.dateFormat = "yyyy-MM-dd HH:mm"
        return f.string(from: d)
    }

    /// Keeps table text inside a budget so one huge result can't crowd out
    /// the rest of the prompt.
    public static func trim(_ s: String, max: Int) -> String {
        guard s.count > max else { return s }
        return String(s.prefix(max)) + "\n… (trimmed)"
    }

    public static func actionLine(_ a: ActionInfo) -> String {
        let ps = a.orderedParams.map { p -> String in
            let spec = a.params?[p]
            return spec?.required == true ? p : "\(p)?"
        }.joined(separator: ", ")
        return "\(a.name)(\(ps)) — \(a.doc) — \(a.tier.uppercased())\(a.requires_admin ? " (admin)" : "")"
    }

    public static func render(template: String, _ x: PromptInputs) -> String {
        let summaries = x.summaries.isEmpty ? "(none yet)" : x.summaries.map { "\(stamp($0.at, x.timeZone)) — \($0.summary)" }.joined(separator: "\n")
        let queries = x.queries.isEmpty ? "(none yet)" : x.queries.map { q in
            "\(q.call) → " + (q.error.map { "error: \($0)" } ?? "\(q.rows) rows\n" + q.table)
        }.joined(separator: "\n\n")
        let actions = x.actionResults.isEmpty ? "(none)" : x.actionResults.map { r in
            "\(r.callText) — " + (r.approved ? (r.ok ? "approved and ran" : "approved; failed") : "declined") + "\n" + trim(r.output, max: 600)
        }.joined(separator: "\n\n")
        let recipes = x.recipes.map { "\($0.name) — \($0.doc)" }.joined(separator: "; ")
        let actionIndex = x.actions.isEmpty ? "(none)" : x.actions.map(actionLine).joined(separator: "; ")
        let values: [String: String] = [
            "mode": x.mode, "scope": x.scope, "time": stamp(x.time, x.timeZone), "privacy": privacyLabel(x.privacy),
            "current_state": x.currentState.isEmpty ? "(no data)" : x.currentState,
            "attention": x.attention.isEmpty ? "(nothing needs attention)" : x.attention,
            "history": x.history.isEmpty ? "(no history recorded yet)" : x.history,
            "summary_window": String(x.summaryWindow), "summaries": summaries,
            "query_results": queries, "action_results": actions,
            "recipes": recipes.isEmpty ? "(none)" : recipes, "actions": actionIndex, "question": x.question,
        ]
        // One pass over the template: substituted text is never re-scanned, so
        // a table cell containing "{{question}}" stays literal.
        var out = ""
        var rest = Substring(template)
        while let open = rest.range(of: "{{") {
            out += rest[..<open.lowerBound]
            guard let close = rest[open.upperBound...].range(of: "}}") else { rest = rest[open.lowerBound...]; break }
            let key = rest[open.upperBound..<close.lowerBound].trimmingCharacters(in: .whitespaces)
            out += values[key] ?? "{{\(key)}}"
            rest = rest[close.upperBound...]
        }
        out += rest
        return out
    }

    /// Tables in the snapshot, labeled by recipe.
    public static func snapshot(_ parts: [(label: String, table: SysqTable)], maxRows: Int = 12, budget: Int = 6000) -> String {
        trim(parts.map { "[\($0.label)]\n" + $0.table.compact(maxRows: maxRows) }.joined(separator: "\n\n"), max: budget)
    }
}
