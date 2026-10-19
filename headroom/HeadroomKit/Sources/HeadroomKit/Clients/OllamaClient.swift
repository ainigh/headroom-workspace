import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

// The assistant's model runtime, behind ChatBackend so another local runtime
// is a new conformance (separation-of-concerns §7).

public struct ToolCall: Codable, Hashable, Sendable {
    public struct Function: Codable, Hashable, Sendable {
        public var name: String
        public var arguments: JSONValue
        public init(name: String, arguments: JSONValue) { self.name = name; self.arguments = arguments }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            name = try c.decode(String.self, forKey: .name)
            // Some models send arguments as a JSON string.
            if let s = try? c.decode(String.self, forKey: .arguments) {
                arguments = (try? JSONDecoder().decode(JSONValue.self, from: Data(s.utf8))) ?? .object([:])
            } else {
                arguments = (try? c.decode(JSONValue.self, forKey: .arguments)) ?? .object([:])
            }
        }
    }
    public var function: Function
    public init(name: String, arguments: JSONValue) { function = Function(name: name, arguments: arguments) }
}

public struct ChatMessage: Codable, Hashable, Sendable {
    public var role: String
    public var content: String
    public var tool_calls: [ToolCall]?
    public var tool_name: String?

    public init(role: String, content: String, tool_calls: [ToolCall]? = nil, tool_name: String? = nil) {
        self.role = role; self.content = content; self.tool_calls = tool_calls; self.tool_name = tool_name
    }

    public static func system(_ s: String) -> ChatMessage { ChatMessage(role: "system", content: s) }
    public static func user(_ s: String) -> ChatMessage { ChatMessage(role: "user", content: s) }
    public static func tool(_ name: String, _ s: String) -> ChatMessage { ChatMessage(role: "tool", content: s, tool_name: name) }
}

public struct ToolSpec: Codable, Hashable, Sendable {
    public struct Function: Codable, Hashable, Sendable {
        public var name: String
        public var description: String
        public var parameters: JSONValue
    }
    public var type = "function"
    public var function: Function

    public init(name: String, description: String, parameters: JSONValue) {
        function = Function(name: name, description: description, parameters: parameters)
    }
}

public struct ChatReply: Sendable {
    public var message: ChatMessage
    public var promptTokens: Int?
    public var outputTokens: Int?
    public init(message: ChatMessage, promptTokens: Int? = nil, outputTokens: Int? = nil) {
        self.message = message; self.promptTokens = promptTokens; self.outputTokens = outputTokens
    }
}

public struct ModelInfo: Codable, Hashable, Sendable, Identifiable {
    public var name: String
    public var size: Double?
    public var parameterSize: String?
    public var id: String { name }
}

public struct LoadedModel: Codable, Hashable, Sendable {
    public var name: String
    public var size: Double?
    public var size_vram: Double?
    public var expires_at: String?
}

public protocol ChatBackend: Sendable {
    func chat(model: String, messages: [ChatMessage], tools: [ToolSpec]?, keepAlive: String?) async throws -> ChatReply
    func models() async throws -> [ModelInfo]
    func loaded() async throws -> [LoadedModel]
    /// Whether the model declares tool calling (Ollama /api/show capabilities).
    func supportsTools(model: String) async throws -> Bool
    func unload(model: String) async throws
}

public final class OllamaClient: ChatBackend, @unchecked Sendable {
    public let baseURL: URL
    let transport: HTTPTransport

    public init(baseURL: URL, transport: HTTPTransport = URLSessionTransport(timeout: 300)) {
        self.baseURL = baseURL
        self.transport = transport
    }

    func post(_ path: String, _ body: JSONValue, timeout: TimeInterval = 300) async throws -> JSONValue {
        var r = URLRequest(url: baseURL.appendingPathComponent(path))
        r.httpMethod = "POST"
        r.timeoutInterval = timeout
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONEncoder().encode(body)
        return try await send(r)
    }

    func get(_ path: String) async throws -> JSONValue {
        var r = URLRequest(url: baseURL.appendingPathComponent(path))
        r.timeoutInterval = 10
        return try await send(r)
    }

    func send(_ r: URLRequest) async throws -> JSONValue {
        let (data, status) = try await transport.send(r)
        let v = (try? JSONDecoder().decode(JSONValue.self, from: data)) ?? .null
        guard (200..<300).contains(status) else {
            let msg = v["error"]?.string ?? "HTTP \(status)"
            throw ClientError(status == 0 ? .unreachable : .failed, status: status, "Ollama: \(msg)")
        }
        return v
    }

    public func chat(model: String, messages: [ChatMessage], tools: [ToolSpec]?, keepAlive: String?) async throws -> ChatReply {
        var body: [String: JSONValue] = [
            "model": .string(model),
            "messages": try JSONDecoder().decode(JSONValue.self, from: JSONEncoder().encode(messages)),
            "stream": false,
        ]
        if let tools, !tools.isEmpty { body["tools"] = try JSONDecoder().decode(JSONValue.self, from: JSONEncoder().encode(tools)) }
        if let keepAlive { body["keep_alive"] = .string(keepAlive) }
        let v = try await post("api/chat", .object(body))
        guard let m = v["message"], let msg = try? JSONDecoder().decode(ChatMessage.self, from: JSONEncoder().encode(m)) else {
            throw ClientError(.decoding, "Ollama sent no message")
        }
        return ChatReply(message: msg, promptTokens: v["prompt_eval_count"]?.double.map { Int($0) }, outputTokens: v["eval_count"]?.double.map { Int($0) })
    }

    public func models() async throws -> [ModelInfo] {
        let v = try await get("api/tags")
        return (v["models"]?.array ?? []).compactMap { m in
            guard let n = m["name"]?.string else { return nil }
            return ModelInfo(name: n, size: m["size"]?.double, parameterSize: m["details"]?["parameter_size"]?.string)
        }
    }

    public func loaded() async throws -> [LoadedModel] {
        let v = try await get("api/ps")
        return (v["models"]?.array ?? []).compactMap { m in
            guard let n = m["name"]?.string else { return nil }
            return LoadedModel(name: n, size: m["size"]?.double, size_vram: m["size_vram"]?.double, expires_at: m["expires_at"]?.string)
        }
    }

    public func supportsTools(model: String) async throws -> Bool {
        let v = try await post("api/show", ["model": .string(model)], timeout: 20)
        if let caps = v["capabilities"]?.array?.compactMap(\.string) { return caps.contains("tools") }
        // Older Ollama: look for a tools block in the chat template.
        return v["template"]?.string?.contains(".Tools") ?? false
    }

    public func unload(model: String) async throws {
        _ = try await post("api/generate", ["model": .string(model), "keep_alive": 0], timeout: 30)
    }
}
