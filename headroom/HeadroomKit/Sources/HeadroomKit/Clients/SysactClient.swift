import Foundation

// JSON-lines client for sysact's unix socket (implementation.md §4). Execution
// needs a ConfirmationToken, which only ConfirmationFlow can construct.

public struct ActionParamSpec: Codable, Hashable, Sendable {
    public var type: String
    public var values: [String]?
    public var `default`: JSONValue?
    public var min: Double?
    public var max: Double?
    public var pattern: String?
    public var required: Bool?
    public var doc: String?
}

public struct ActionInfo: Codable, Hashable, Sendable, Identifiable {
    public var name: String
    public var doc: String
    public var tier: String
    public var requires_admin: Bool
    public var params: [String: ActionParamSpec]?
    public var param_order: [String]?
    public var effect: String
    public var undo_hint: String?
    public var id: String { name }

    /// Parameters in declared order.
    public var orderedParams: [String] {
        let declared = param_order ?? []
        return declared + (params?.keys.filter { !declared.contains($0) }.sorted() ?? [])
    }
}

/// A prepared plan: the exact action, resolved params and effect statement.
public struct ActionPlan: Codable, Hashable, Sendable, Identifiable {
    public var plan_id: String
    public var plan_hash: String
    public var name: String
    public var doc: String
    public var tier: String
    public var params: [String: String]
    public var effect: String
    public var undo_hint: String?
    public var requires_admin: Bool
    public var command: String
    public var expires_at: String
    public var warnings: [String]?
    public var dry_run: Bool?
    public var id: String { plan_id }

    public var callText: String {
        let ps = params.keys.sorted().map { "\($0)=\"\(params[$0]!)\"" }.joined(separator: ", ")
        return "\(name)(\(ps))"
    }
}

public struct ActionOutcome: Codable, Hashable, Sendable {
    public var plan_id: String
    public var name: String
    public var ok: Bool
    public var exit_code: Int
    public var output: String
}

public protocol SysactClientProtocol: Sendable {
    func catalog() async throws -> [ActionInfo]
    func prepare(_ name: String, params: [String: JSONValue]) async throws -> ActionPlan
    /// Runs a confirmed plan. The token is the proof that a person confirmed
    /// exactly this plan; there is no other way to call sysact's execute.
    func execute(_ plan: ActionPlan, confirmation: ConfirmationToken) async throws -> ActionOutcome
    func status() async throws -> JSONValue
}

/// Signs confirmation records with the host key sysact keeps in its data
/// directory (host.key, hex, 0600).
public struct HostKeySigner: Sendable {
    public let keyPath: URL
    public init(keyPath: URL) { self.keyPath = keyPath }

    public func key() throws -> [UInt8] {
        guard let text = try? String(contentsOf: keyPath, encoding: .utf8),
              let k = SHA256.fromHex(text.trimmingCharacters(in: .whitespacesAndNewlines)), k.count >= 32 else {
            throw ClientError(.notRunning, "sysact's host key is missing; start sysact once to create it")
        }
        return k
    }

    public static func canonical(planID: String, planHash: String, confirmedAt: Date, nonce: String, via: String) -> String {
        ["sysact-confirm-v1", planID, planHash, rfc3339(confirmedAt), nonce, via].joined(separator: "\n")
    }

    public static func rfc3339(_ d: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        f.timeZone = TimeZone(identifier: "UTC")
        return f.string(from: d)
    }

    func record(_ t: ConfirmationToken) throws -> JSONValue {
        let text = Self.canonical(planID: t.planID, planHash: t.planHash, confirmedAt: t.confirmedAt, nonce: t.nonce, via: t.via)
        let sig = SHA256.hex(SHA256.hmac(key: try key(), message: Array(text.utf8)))
        return ["plan_id": .string(t.planID), "plan_hash": .string(t.planHash), "confirmed_at": .string(Self.rfc3339(t.confirmedAt)),
                "via": .string(t.via), "nonce": .string(t.nonce), "signature": .string(sig)]
    }
}

public final class SysactClient: SysactClientProtocol, @unchecked Sendable {
    public let socketPath: String
    public let signer: HostKeySigner
    private let counter = Counter()

    public init(socketPath: String, signer: HostKeySigner) {
        self.socketPath = socketPath
        self.signer = signer
    }

    final class Counter: @unchecked Sendable {
        private var n = 0
        private let lock = NSLock()
        func next() -> Int { lock.lock(); defer { lock.unlock() }; n += 1; return n }
    }

    func call(_ method: String, _ params: JSONValue, timeout: TimeInterval = 30) async throws -> JSONValue {
        let id = counter.next()
        let line = try JSONEncoder().encode(JSONValue.object(["id": .number(Double(id)), "method": .string(method), "params": params]))
        let path = socketPath
        let reply: Data = try await Task.detached {
            do {
                let s = try UnixSocket(path: path, timeout: timeout)
                try s.write(line + Data([0x0a]))
                return try s.readLine()
            } catch {
                throw ClientError(.notRunning, "sysact: \(error.localizedDescription)")
            }
        }.value
        let v = try JSONDecoder().decode(JSONValue.self, from: reply)
        if let msg = v["error"]?["message"]?.string { throw ClientError(.failed, msg) }
        return v["result"] ?? .null
    }

    func decode<T: Decodable>(_ t: T.Type, _ v: JSONValue) throws -> T {
        do { return try JSONDecoder().decode(T.self, from: JSONEncoder().encode(v)) } catch {
            throw ClientError(.decoding, "sysact sent an unexpected reply: \(error)")
        }
    }

    public func status() async throws -> JSONValue { try await call("status", [:], timeout: 5) }

    public func catalog() async throws -> [ActionInfo] {
        try decode([ActionInfo].self, try await call("catalog", [:])["actions"] ?? [])
    }

    public func prepare(_ name: String, params: [String: JSONValue]) async throws -> ActionPlan {
        try decode(ActionPlan.self, try await call("prepare", ["name": .string(name), "params": .object(params)]))
    }

    public func execute(_ plan: ActionPlan, confirmation: ConfirmationToken) async throws -> ActionOutcome {
        guard confirmation.planID == plan.plan_id, confirmation.planHash == plan.plan_hash else {
            throw ClientError(.badRequest, "confirmation is for a different plan")
        }
        let rec = try signer.record(confirmation)
        // Admin actions wait on macOS's own password prompt.
        let v = try await call("execute", ["plan_id": .string(plan.plan_id), "confirmations": [rec]], timeout: plan.requires_admin ? 600 : 300)
        return try decode(ActionOutcome.self, v)
    }
}
