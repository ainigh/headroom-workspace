import Foundation

// Typed views of Headroom's YAML files (headroom-spec §12,
// separation-of-concerns §4). Nothing here names a specific group, recipe or
// threshold: those come from the files.

public struct RecipeBinding: Codable, Hashable, Sendable {
    public var recipe: String
    public var column: String?
    public var label: String?
    public var unit: String?
    public var params: [String: JSONValue]?

    public init(recipe: String, column: String? = nil, label: String? = nil, unit: String? = nil, params: [String: JSONValue]? = nil) {
        self.recipe = recipe; self.column = column; self.label = label; self.unit = unit; self.params = params
    }
}

public struct ConsumerBinding: Codable, Hashable, Sendable {
    public var recipe: String
    public var params: [String: JSONValue]?
    /// Column the bar shows.
    public var value: String
    /// Column naming the consumer (auto-detected when absent).
    public var name: String?
    /// What kind of item a row is (process, app, path, model) and which column identifies it.
    public var kind: String?
    public var id: String?
    public var note: String?
}

public struct SparklineBinding: Codable, Hashable, Sendable {
    public var recipe: String
    public var column: String
    public var range: String?
}

public struct GroupConfig: Codable, Hashable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var symbol: String
    public var headline: RecipeBinding
    public var metrics: [RecipeBinding]?
    public var detail: RecipeBinding
    public var consumers: ConsumerBinding
    public var sparkline: SparklineBinding
    public var attention: String
    public var reclaimable: RecipeBinding?
}

public struct GroupsFile: Codable, Sendable {
    public var version: Int
    public var overview: String?
    public var item_profile: String?
    public var explorer: ExplorerConfig?
    public var groups: [GroupConfig]
}

/// A recipe the Storage Explorer runs, and the names of its parameters.
public struct ExplorerBinding: Codable, Hashable, Sendable {
    public var recipe: String
    public var params: [String: JSONValue]?
    public var root: String?
    public var min: String?
    public var age: String?

    /// Params for a run: fixed ones plus the explorer's filters.
    public func params(root r: String?, minBytes: Double?, ageSeconds: Double?) -> [String: JSONValue] {
        var p = params ?? [:]
        if let root, let r { p[root] = .string(r) }
        if let min, let m = minBytes, m > 0 { p[min] = .number(m) }
        if let age, let a = ageSeconds, a > 0 { p[age] = .string("\(Int(a))s") }
        return p
    }
}

public struct ExplorerConfig: Codable, Hashable, Sendable {
    public var group: String?
    public var roots: [String]?
    public var volumes: ExplorerBinding
    public var purgeable: ExplorerBinding?
    public var folders: ExplorerBinding
    public var files: ExplorerBinding?
    public var by_type: ExplorerBinding?
    public var suggestions: [ExplorerBinding]?
}

public struct LayoutFile: Codable, Sendable {
    public struct KeyGroups: Codable, Sendable {
        public var mode: String?
        public var pinned: [String]?
        public var auto_hold_minutes: Double?
    }
    public struct Panel: Codable, Sendable {
        public var always_on_top: Bool?
        public var remember_position: Bool?
        public var x: Double?
        public var y: Double?
    }
    public var version: Int
    public var key_groups: KeyGroups?
    public var group_order: [String]?
    public var hidden_groups: [String]?
    public var panel: Panel?
    public var menu_bar_readout: String?

    public var keyMode: String { key_groups?.mode ?? "pinned" }
    public var pinned: [String] { key_groups?.pinned ?? [] }
    public var holdMinutes: Double { key_groups?.auto_hold_minutes ?? 5 }
}

/// A threshold value: a number or a string with units ("50GB", "50MB/s").
public struct Quantity: Codable, Hashable, Sendable {
    public var value: Double
    public init(_ v: Double) { value = v }
    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if let d = try? c.decode(Double.self) { value = d; return }
        let s = try c.decode(String.self)
        guard let v = Units.parse(s) else {
            throw DecodingError.dataCorruptedError(in: c, debugDescription: "not a quantity: \(s)")
        }
        value = v
    }
    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(value)
    }
}

public struct Threshold: Codable, Hashable, Sendable {
    public var metric: String?
    public var amber: Quantity
    public var red: Quantity
    public var direction: String?
    public var higherIsWorse: Bool { (direction ?? "up") == "up" }
}

public struct ModeGroup: Codable, Hashable, Sendable {
    public var recipes: [String]?
    public var actions: [String]?
    public var emphasis: String?
}

public struct ModeConfig: Codable, Hashable, Sendable {
    public var label: String
    public var question: String
    public var rank_by: String?
    public var tint: String?
    public var history_range: String?
    public var consumers: Int?
    public var groups: [String: ModeGroup]?
}

public struct ModesFile: Codable, Sendable {
    public var version: Int
    public var order: [String]?
    public var modes: [String: ModeConfig]
}

public struct Rates: Codable, Hashable, Sendable {
    public var headline: String?
    public var detail: String?
    public var consumers: String?
}

public struct SamplingFile: Codable, Sendable {
    public struct RateSet: Codable, Sendable {
        public var view_open: Rates
        public var idle: Rates
        public var on_battery: Rates?
    }
    public var version: Int
    public var rates: RateSet
    public var hidden_groups: Rates?
    /// Passed through to sysqd's schedules.yaml as written.
    public var narrative: [JSONValue]?
    public var events: [JSONValue]?
}

public struct AssistantFile: Codable, Sendable {
    public var version: Int
    public var enabled: Bool?
    public var backend: String?
    public var ollama_url: String?
    public var model: String?
    public var tools: String?
    public var privacy_level: String?
    public var summary_window: Int?
    public var action_requests: Bool?
    public var confirm_delay_seconds: Double?
    public var fuel_budget: Int?
    public var max_tool_calls: Int?
    public var max_turn_seconds: Double?
    public var narrative_range: String?
    public var system_prompt: String?
    public var request_template: String?
    public var unload_after_minutes: Double?
    public var preflight_recipe: String?

    public var privacy: String { privacy_level ?? "consumers" }
    /// sysq's sensitivity ceiling for the assistant (sysq-design §28). Never "secret".
    public var maxSensitivity: String { privacy == "aggregates" ? "public" : "personal" }
    public var confirmDelay: Double { max(1, confirm_delay_seconds ?? 2) }
}

public struct APIFile: Codable, Sendable {
    public struct ClientEntry: Codable, Sendable {
        public var id: String
        public var name: String?
        public var privacy: String?
        public var origins: [String]?
        public var allow_scripts: Bool?
    }
    public var version: Int
    public var enabled: Bool?
    public var port: Int?
    public var default_privacy: String?
    public var exposed_recipes: [String]?
    public var clients: [ClientEntry]?
}

public struct NotificationsFile: Codable, Sendable {
    public struct QuietHours: Codable, Sendable { public var from: String?; public var to: String? }
    public var version: Int
    public var enabled: Bool?
    public var on_status: [String]?
    /// Per group: a list of levels, or "off"/false.
    public var groups: [String: JSONValue]?
    public var assistant: [String]?
    public var cooldown_minutes: Double?
    public var quiet_hours: QuietHours?

    public func levels(for group: String) -> [String] {
        guard let v = groups?[group] else { return on_status ?? ["red"] }
        if case .array(let a) = v { return a.compactMap(\.string) }
        return []
    }
}

public struct RetentionFile: Codable, Sendable {
    public var version: Int
    public var assistant_log_days: Int?
    public var graph_ranges: [String]?
}

/// The whole configuration: an immutable value republished on every reload.
public struct AppConfig: Sendable {
    public var groups: GroupsFile
    public var layout: LayoutFile
    public var thresholds: [String: Threshold]
    public var modes: ModesFile
    public var sampling: SamplingFile
    public var assistant: AssistantFile
    public var api: APIFile
    public var notifications: NotificationsFile
    public var retention: RetentionFile
    public var systemPrompt: String
    public var requestTemplate: String
    public var textToolsPrompt: String
    public var notices: Notices

    public func group(_ id: String) -> GroupConfig? { groups.groups.first { $0.id == id } }

    /// Modes in display order.
    public var modeOrder: [String] {
        let declared = modes.order ?? []
        let rest = modes.modes.keys.filter { !declared.contains($0) }.sorted()
        return declared.filter { modes.modes[$0] != nil } + rest
    }

    /// Groups in the configured order, hidden ones last.
    public var orderedGroups: [GroupConfig] {
        let order = layout.group_order ?? []
        let byID = Dictionary(uniqueKeysWithValues: groups.groups.map { ($0.id, $0) })
        var out = order.compactMap { byID[$0] }
        out += groups.groups.filter { !order.contains($0.id) }
        return out
    }
}

/// Parses a duration like "5m", "2s", "24h" into seconds.
public func seconds(_ s: String?, default d: Double) -> Double {
    guard let s, let v = Units.parse(s) else { return d }
    return v
}

/// prompts/notices.md: the short messages Headroom adds to a conversation,
/// one "## name" section each.
public struct Notices: Sendable {
    public var values: [String: String]

    public init(_ text: String) {
        var out: [String: String] = [:]
        var key: String?
        var buf: [String] = []
        func flush() { if let k = key { out[k] = buf.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines) }; buf = [] }
        for line in text.components(separatedBy: "\n") {
            if line.hasPrefix("## ") { flush(); key = String(line.dropFirst(3)).trimmingCharacters(in: .whitespaces) } else if key != nil { buf.append(line) }
        }
        flush()
        values = out
    }

    public subscript(_ k: String) -> String { values[k] ?? "" }
}
