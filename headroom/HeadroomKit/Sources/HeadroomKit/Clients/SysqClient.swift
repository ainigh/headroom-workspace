import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

// Typed wrapper over sysqd's HTTP API (sysq-design §26). Transport only: no
// group names, no thresholds, no UI.

public struct RunMeta: Codable, Hashable, Sendable {
    public var cost_units: Int?
    public var calls: Int?
    public var primitives: [String]?
    public var duration_ms: Int?
}

/// The result of running a recipe or script.
public struct RunResult: Codable, Hashable, Sendable {
    public var items: [SysqItem]
    public var meta: RunMeta?
    public var error: String?
    public var lowered: String?

    public init(items: [SysqItem], meta: RunMeta? = nil, error: String? = nil, lowered: String? = nil) {
        self.items = items; self.meta = meta; self.error = error; self.lowered = lowered
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        items = (try? c.decode([SysqItem].self, forKey: .items)) ?? []
        meta = try? c.decode(RunMeta.self, forKey: .meta)
        error = try? c.decode(String.self, forKey: .error)
        lowered = try? c.decode(String.self, forKey: .lowered)
    }

    /// The first table (most recipes emit one).
    public var table: SysqTable { items.first?.table ?? SysqTable(schema: [], rows: [], meta: SysqMeta(error: error)) }
}

public struct CatalogEntry: Codable, Hashable, Sendable, Identifiable {
    public var name: String
    public var kind: String
    public var doc: String
    public var cost: String?
    public var sensitivity: String?
    public var available: Bool
    public var exposed: Bool?
    public var reason: String?
    public var id: String { name }
}

/// What explain returns: the lowered Starlark plus derived facts.
public struct ExplainPlan: Codable, Hashable, Sendable {
    public var lowered: String
    public var primitives: [String]?
    public var recipes: [String]?
    public var cost: String
    public var cost_units: Int?
    public var sensitivity: String
    public var network: Bool?
    public var pushdown: [String]?
    public var loops: Int?
    public var unknown: [String]?
}

public struct SysqStatus: Codable, Sendable {
    public struct ClientInfo: Codable, Sendable {
        public var id: String
        public var privacy: String
        public var max_sensitivity: String
        public var allow_scripts: Bool
    }
    public struct Catalog: Codable, Sendable { public var primitives: Int; public var version: String }
    public var version: String
    public var catalog: Catalog?
    public var recipes: Int?
    public var history: Bool?
    public var fixtures: Bool?
    public var view_open: Bool?
    public var on_battery: Bool?
    public var uptime_seconds: Int?
    public var client: ClientInfo?
    public var warnings: [String]?
}

/// One SSE update from /v1/subscribe.
public struct SubscriptionUpdate: Codable, Sendable {
    public var key: String
    public var name: String
    public var ts: String
    public var items: [SysqItem]
    public var error: String?

    public init(key: String, name: String, ts: String, items: [SysqItem], error: String? = nil) {
        self.key = key; self.name = name; self.ts = ts; self.items = items; self.error = error
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = (try? c.decode(String.self, forKey: .key)) ?? ""
        name = (try? c.decode(String.self, forKey: .name)) ?? ""
        ts = (try? c.decode(String.self, forKey: .ts)) ?? ""
        items = (try? c.decode([SysqItem].self, forKey: .items)) ?? []
        error = try? c.decode(String.self, forKey: .error)
    }

    public var table: SysqTable { items.first?.table ?? SysqTable(schema: [], rows: [], meta: SysqMeta(error: error)) }
}

public struct Narrative: Sendable {
    public var text: String
    public var table: SysqTable
}

/// The read-only sysq surface every store and the assistant depend on.
public protocol SysqClientProtocol: Sendable {
    func status() async throws -> SysqStatus
    func catalog() async throws -> [CatalogEntry]
    func describe(_ name: String) async throws -> JSONValue
    func runRecipe(_ name: String, params: [String: JSONValue]) async throws -> RunResult
    func run(script: String, budget: Int?) async throws -> RunResult
    func explain(script: String) async throws -> ExplainPlan
    func explain(recipe: String) async throws -> ExplainPlan
    func history(_ name: String, range: String, column: String?, key: String?) async throws -> SysqTable
    func events(range: String) async throws -> SysqTable
    func narrative(range: String) async throws -> Narrative
    func consumersAt(_ name: String, at: String) async throws -> SysqTable
    func attention(min: String?) async throws -> SysqTable
    func subscribe(_ name: String, params: [String: JSONValue], every: Double) -> AsyncThrowingStream<SubscriptionUpdate, Error>
}

public final class SysqClient: SysqClientProtocol, @unchecked Sendable {
    public let baseURL: URL
    private let lock = NSLock()
    private var _token: String
    private let transport: HTTPTransport
    public let clientName: String

    public init(baseURL: URL, token: String, clientName: String = "Headroom", transport: HTTPTransport = URLSessionTransport()) {
        self.baseURL = baseURL
        self._token = token
        self.clientName = clientName
        self.transport = transport
    }

    /// The token rotates when the host re-registers its clients.
    public var token: String {
        get { lock.lock(); defer { lock.unlock() }; return _token }
        set { lock.lock(); _token = newValue; lock.unlock() }
    }

    func url(_ path: String, _ query: [String: String] = [:]) -> URL {
        var c = URLComponents(url: baseURL.appendingPathComponent(path), resolvingAgainstBaseURL: false)!
        if !query.isEmpty { c.queryItems = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) } }
        return c.url!
    }

    func request(_ method: String, _ path: String, query: [String: String] = [:], body: JSONValue? = nil, timeout: TimeInterval = 60) throws -> URLRequest {
        var r = URLRequest(url: url(path, query))
        r.httpMethod = method
        r.timeoutInterval = timeout
        r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        r.setValue(clientName, forHTTPHeaderField: "X-Sysq-Client")
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        if let body {
            r.httpBody = try JSONEncoder().encode(body)
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return r
    }

    /// Sends and decodes; a run that failed inside the engine (422/403 with a
    /// run body) still decodes, so callers see the partial result and error.
    func call<T: Decodable>(_ type: T.Type, _ req: URLRequest, runBody: Bool = false) async throws -> T {
        let (data, status) = try await transport.send(req)
        if (200..<300).contains(status) || (runBody && (status == 422 || status == 403 || status == 400)) {
            if runBody, !(200..<300).contains(status), let v = try? JSONDecoder().decode(JSONValue.self, from: data), v["items"] == nil {
                throw Self.error(status, data)
            }
            do { return try JSONDecoder().decode(T.self, from: data) } catch {
                throw ClientError(.decoding, status: status, "sysqd sent an unexpected reply: \(error)")
            }
        }
        throw Self.error(status, data)
    }

    static func error(_ status: Int, _ data: Data) -> ClientError {
        let v = try? JSONDecoder().decode(JSONValue.self, from: data)
        let code = v?["error"]?.string ?? "http \(status)"
        let detail = v?["detail"]?.string
        let msg = detail.map { "\(code): \($0)" } ?? code
        switch status {
        case 401: return ClientError(.unauthorized, status: status, msg)
        case 403: return ClientError(.forbidden, status: status, msg)
        case 400, 404: return ClientError(.badRequest, status: status, msg)
        case 0: return ClientError(.unreachable, msg)
        default: return ClientError(.failed, status: status, msg)
        }
    }

    public func status() async throws -> SysqStatus {
        try await call(SysqStatus.self, request("GET", "v1/status", timeout: 10))
    }

    public func catalog() async throws -> [CatalogEntry] {
        struct R: Decodable { var items: [CatalogEntry]? }
        return try await call(R.self, request("GET", "v1/recipes")).items ?? []
    }

    public func describe(_ name: String) async throws -> JSONValue {
        try await call(JSONValue.self, request("GET", "v1/recipes/\(name)"))
    }

    public func runRecipe(_ name: String, params: [String: JSONValue]) async throws -> RunResult {
        try await call(RunResult.self, request("POST", "v1/run/\(name)", body: .object(params), timeout: 120), runBody: true)
    }

    public func run(script: String, budget: Int?) async throws -> RunResult {
        var body: [String: JSONValue] = ["script": .string(script)]
        if let budget { body["budget"] = .number(Double(budget)) }
        return try await call(RunResult.self, request("POST", "v1/run", body: .object(body), timeout: 120), runBody: true)
    }

    public func explain(script: String) async throws -> ExplainPlan {
        try await call(ExplainPlan.self, request("POST", "v1/explain", body: ["script": .string(script)]))
    }

    public func explain(recipe: String) async throws -> ExplainPlan {
        try await call(ExplainPlan.self, request("POST", "v1/explain", body: ["recipe": .string(recipe)]))
    }

    struct Items: Decodable { var items: [SysqItem]?; var text: String? }

    public func history(_ name: String, range: String, column: String?, key: String?) async throws -> SysqTable {
        var q = ["range": range]
        if let column { q["column"] = column }
        if let key { q["key"] = key }
        return try await call(Items.self, request("GET", "v1/history/\(name)", query: q)).items?.first?.table ?? SysqTable(schema: [], rows: [])
    }

    public func events(range: String) async throws -> SysqTable {
        try await call(Items.self, request("GET", "v1/events", query: ["range": range])).items?.first?.table ?? SysqTable(schema: [], rows: [])
    }

    public func narrative(range: String) async throws -> Narrative {
        let r = try await call(Items.self, request("GET", "v1/narrative", query: ["range": range]))
        return Narrative(text: r.text ?? "", table: r.items?.first?.table ?? SysqTable(schema: [], rows: []))
    }

    public func consumersAt(_ name: String, at: String) async throws -> SysqTable {
        try await call(Items.self, request("GET", "v1/consumers_at/\(name)", query: ["at": at])).items?.first?.table ?? SysqTable(schema: [], rows: [])
    }

    public func attention(min: String?) async throws -> SysqTable {
        let q = min.map { ["min": $0] } ?? [:]
        return try await call(Items.self, request("GET", "v1/attention", query: q, timeout: 120)).items?.first?.table ?? SysqTable(schema: [], rows: [])
    }

    public func subscribe(_ name: String, params: [String: JSONValue], every: Double) -> AsyncThrowingStream<SubscriptionUpdate, Error> {
        var q = ["every": "\(max(1, Int(every.rounded())))s"]
        if !params.isEmpty, let d = try? JSONEncoder().encode(JSONValue.object(params)) { q["params"] = String(decoding: d, as: UTF8.self) }
        guard var req = try? request("GET", "v1/subscribe/\(name)", query: q, timeout: 120) else {
            return AsyncThrowingStream { $0.finish(throwing: ClientError(.badRequest, "bad subscription")) }
        }
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        let events = SSEStream.open(req)
        return AsyncThrowingStream { cont in
            let task = Task {
                do {
                    for try await e in events where e.event == "update" {
                        if let u = try? JSONDecoder().decode(SubscriptionUpdate.self, from: Data(e.data.utf8)) { cont.yield(u) }
                    }
                    cont.finish()
                } catch {
                    cont.finish(throwing: error)
                }
            }
            cont.onTermination = { _ in task.cancel() }
        }
    }
}
