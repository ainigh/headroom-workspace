import Foundation

// sysqd's host-only admin surface: a 0600 unix socket under sysq's data
// directory (never TCP). Headroom uses it to register its own clients, apply
// api.yaml, approve pairings, report view/power state and write schedules.

public struct APIClientInfo: Codable, Hashable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var privacy: String
    public var allow_scripts: Bool
    public var allow_propose: Bool?
    public var origins: [String]?
    public var exposed: [String]?
    public var created: String?
    public var last_seen: String?
    public var host: Bool?
}

public struct PendingPairing: Codable, Hashable, Sendable, Identifiable {
    public var id: String
    public var code: String
    public var name: String
    public var origin: String?
    public var requested: String?
    public var approved: Bool?
}

/// A client registration Headroom makes for itself.
public struct HostClient: Codable, Sendable {
    public var id: String
    public var name: String
    public var privacy: String
    public var allow_scripts: Bool
    public var allow_propose: Bool
    public var exposed: [String]
    public var origins: [String]?

    public init(id: String, name: String, privacy: String, allow_scripts: Bool, allow_propose: Bool = false, exposed: [String] = ["*"]) {
        self.id = id; self.name = name; self.privacy = privacy
        self.allow_scripts = allow_scripts; self.allow_propose = allow_propose; self.exposed = exposed
    }
}

public struct APISettings: Codable, Sendable, Equatable {
    public var enabled: Bool
    public var port: Int
    public var exposed: [String]
    public init(enabled: Bool, port: Int, exposed: [String]) { self.enabled = enabled; self.port = port; self.exposed = exposed }
}

public protocol SysqAdminProtocol: Sendable {
    func status() async throws -> JSONValue
    /// The status after sysqd forgets its cached sudo checks (a rule was just added).
    func status(recheck: Bool) async throws -> JSONValue
    func clients() async throws -> [APIClientInfo]
    func pending() async throws -> [PendingPairing]
    func approve(id: String, code: String, privacy: String, allowScripts: Bool, origins: [String]) async throws
    /// Creates or replaces a client and returns its (fresh) token.
    func upsert(_ client: HostClient) async throws -> String
    func revoke(_ id: String) async throws
    func setAPI(_ api: APISettings) async throws
    func setState(viewOpen: Bool, onBattery: Bool?) async throws
    func putSchedules(_ yaml: String) async throws
    func reload() async throws
}

extension SysqAdminProtocol {
    public func status(recheck: Bool) async throws -> JSONValue { try await status() }
}

public final class SysqAdminClient: SysqAdminProtocol, @unchecked Sendable {
    public let socketPath: String

    public init(socketPath: String) { self.socketPath = socketPath }

    func send(_ method: String, _ path: String, body: Data? = nil) async throws -> Data {
        let sock = socketPath
        let resp: UnixHTTP.Response = try await Task.detached {
            do {
                return try UnixHTTP.request(socket: sock, method: method, path: path, body: body)
            } catch {
                throw ClientError(.notRunning, "sysqd admin socket: \(error.localizedDescription)")
            }
        }.value
        guard (200..<300).contains(resp.status) else { throw SysqClient.error(resp.status, resp.body) }
        return resp.body
    }

    func json<T: Encodable>(_ v: T) throws -> Data { try JSONEncoder().encode(v) }

    public func status() async throws -> JSONValue { try await status(recheck: false) }

    public func status(recheck: Bool) async throws -> JSONValue {
        try JSONDecoder().decode(JSONValue.self, from: await send("GET", recheck ? "/admin/status?recheck=1" : "/admin/status"))
    }

    public func clients() async throws -> [APIClientInfo] {
        struct R: Decodable { var clients: [APIClientInfo]? }
        return try JSONDecoder().decode(R.self, from: await send("GET", "/admin/clients")).clients ?? []
    }

    public func pending() async throws -> [PendingPairing] {
        struct R: Decodable { var pending: [PendingPairing]? }
        return try JSONDecoder().decode(R.self, from: await send("GET", "/admin/pending")).pending ?? []
    }

    public func approve(id: String, code: String, privacy: String, allowScripts: Bool, origins: [String]) async throws {
        let body: JSONValue = ["id": .string(id), "code": .string(code), "privacy": .string(privacy),
                               "allow_scripts": .bool(allowScripts), "origins": .array(origins.map(JSONValue.string))]
        _ = try await send("POST", "/admin/approve", body: json(body))
    }

    public func upsert(_ client: HostClient) async throws -> String {
        struct R: Decodable { var token: String }
        return try JSONDecoder().decode(R.self, from: await send("POST", "/admin/clients", body: json(client))).token
    }

    public func revoke(_ id: String) async throws {
        _ = try await send("DELETE", "/admin/clients/\(id)")
    }

    public func setAPI(_ api: APISettings) async throws {
        _ = try await send("PUT", "/admin/api", body: json(api))
    }

    public func setState(viewOpen: Bool, onBattery: Bool?) async throws {
        var body: [String: JSONValue] = ["view_open": .bool(viewOpen)]
        if let onBattery { body["on_battery"] = .bool(onBattery) }
        _ = try await send("POST", "/admin/state", body: json(JSONValue.object(body)))
    }

    public func putSchedules(_ yaml: String) async throws {
        _ = try await send("PUT", "/admin/schedules", body: Data(yaml.utf8))
    }

    public func reload() async throws {
        _ = try await send("POST", "/admin/reload")
    }
}
