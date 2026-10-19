import Foundation

/// A JSON value. sysq tables, recipe params and tool arguments travel as these.
public enum JSONValue: Codable, Hashable, Sendable, CustomStringConvertible {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null; return }
        if let b = try? c.decode(Bool.self) { self = .bool(b); return }
        if let n = try? c.decode(Double.self) { self = .number(n); return }
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let a = try? c.decode([JSONValue].self) { self = .array(a); return }
        if let o = try? c.decode([String: JSONValue].self) { self = .object(o); return }
        throw DecodingError.dataCorruptedError(in: c, debugDescription: "not JSON")
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .number(let n):
            if n.rounded() == n, abs(n) < 1e15 { try c.encode(Int64(n)) } else { try c.encode(n) }
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }

    public var double: Double? {
        switch self {
        case .number(let n): return n
        case .bool(let b): return b ? 1 : 0
        case .string(let s): return Double(s)
        default: return nil
        }
    }
    public var string: String? {
        switch self {
        case .string(let s): return s
        case .number(let n): return n.rounded() == n && abs(n) < 1e15 ? String(Int64(n)) : String(n)
        case .bool(let b): return b ? "true" : "false"
        default: return nil
        }
    }
    public var bool: Bool? { if case .bool(let b) = self { return b }; return nil }
    public var isNull: Bool { if case .null = self { return true }; return false }
    public var object: [String: JSONValue]? { if case .object(let o) = self { return o }; return nil }
    public var array: [JSONValue]? { if case .array(let a) = self { return a }; return nil }

    public subscript(key: String) -> JSONValue? { object?[key] }

    public var description: String {
        switch self {
        case .null: return "null"
        case .string(let s): return s
        default:
            let enc = JSONEncoder()
            enc.outputFormatting = [.sortedKeys]
            return (try? String(decoding: enc.encode(self), as: UTF8.self)) ?? "?"
        }
    }

    /// Converts Foundation values (from JSONSerialization or Yams) into JSONValue.
    public static func from(_ any: Any?) -> JSONValue {
        switch any {
        case nil: return .null
        case is NSNull: return .null
        case let b as Bool: return .bool(b)
        case let i as Int: return .number(Double(i))
        case let i as Int64: return .number(Double(i))
        case let d as Double: return .number(d)
        case let f as Float: return .number(Double(f))
        case let s as String: return .string(s)
        case let a as [Any]: return .array(a.map { from($0) })
        case let a as [Any?]: return .array(a.map { from($0) })
        case let d as [String: Any]: return .object(d.mapValues { from($0) })
        case let d as [AnyHashable: Any]:
            var o: [String: JSONValue] = [:]
            for (k, v) in d { o[String(describing: k.base)] = from(v) }
            return .object(o)
        default: return .string(String(describing: any!))
        }
    }

    /// Plain Foundation value for JSONSerialization.
    public var foundation: Any {
        switch self {
        case .null: return NSNull()
        case .bool(let b): return b
        case .number(let n): return n.rounded() == n && abs(n) < 1e15 ? Int(n) as Any : n
        case .string(let s): return s
        case .array(let a): return a.map(\.foundation)
        case .object(let o): return o.mapValues(\.foundation)
        }
    }
}

extension JSONValue: ExpressibleByStringLiteral, ExpressibleByIntegerLiteral, ExpressibleByFloatLiteral, ExpressibleByBooleanLiteral, ExpressibleByNilLiteral {
    public init(stringLiteral value: String) { self = .string(value) }
    public init(integerLiteral value: Int) { self = .number(Double(value)) }
    public init(floatLiteral value: Double) { self = .number(value) }
    public init(booleanLiteral value: Bool) { self = .bool(value) }
    public init(nilLiteral: ()) { self = .null }
}

extension JSONValue: ExpressibleByDictionaryLiteral, ExpressibleByArrayLiteral {
    public init(dictionaryLiteral elements: (String, JSONValue)...) {
        self = .object(Dictionary(elements, uniquingKeysWith: { _, b in b }))
    }
    public init(arrayLiteral elements: JSONValue...) { self = .array(elements) }
}

extension JSONValue {
    /// Compact JSON text (sorted keys), for prompts, logs and CLI lines.
    public var json: String {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return (try? e.encode(self)).map { String(decoding: $0, as: UTF8.self) } ?? description
    }
}
