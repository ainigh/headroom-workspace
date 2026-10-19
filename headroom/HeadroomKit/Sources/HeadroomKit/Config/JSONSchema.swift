import Foundation

/// A JSON Schema (draft-07 subset) validator for Headroom's config files:
/// type, properties, required, additionalProperties, items, enum, const,
/// pattern, minimum/maximum, minItems/maxItems, minLength, oneOf/anyOf and
/// local $ref. Values are JSONValue so booleans and numbers never blur.
public struct JSONSchema: Sendable {
    public struct Violation: Error, CustomStringConvertible, Sendable {
        public let path: [String]
        public let message: String
        public var pointer: String { path.isEmpty ? "/" : "/" + path.joined(separator: "/") }
        public var description: String { "\(pointer): \(message)" }
    }

    let root: JSONValue

    public init(data: Data) throws {
        root = try JSONDecoder().decode(JSONValue.self, from: data)
        guard root.object != nil else { throw Violation(path: [], message: "schema is not an object") }
    }

    /// Returns the first violation, or nil when valid.
    public func validate(_ value: JSONValue) -> Violation? { check(value, root, []) }

    private func resolve(_ s: JSONValue) -> JSONValue {
        guard let ref = s["$ref"]?.string, ref.hasPrefix("#/") else { return s }
        var node = root
        for part in ref.dropFirst(2).split(separator: "/") {
            guard let next = node[String(part)] else { return s }
            node = next
        }
        return node
    }

    private func typeName(_ v: JSONValue) -> String {
        switch v {
        case .null: return "null"
        case .bool: return "boolean"
        case .number(let n): return n.rounded() == n ? "integer" : "number"
        case .string: return "string"
        case .array: return "array"
        case .object: return "object"
        }
    }

    private func matches(_ v: JSONValue, _ t: String) -> Bool {
        let actual = typeName(v)
        return t == actual || (t == "number" && actual == "integer")
    }

    private func check(_ v: JSONValue, _ s0: JSONValue, _ path: [String]) -> Violation? {
        let s = resolve(s0)
        if let t = s["type"]?.string, !matches(v, t) {
            return Violation(path: path, message: "expected \(t), found \(typeName(v))")
        }
        if let ts = s["type"]?.array?.compactMap(\.string), !ts.isEmpty, !ts.contains(where: { matches(v, $0) }) {
            return Violation(path: path, message: "expected one of \(ts.joined(separator: ", ")), found \(typeName(v))")
        }
        if let c = s["const"], c != v {
            return Violation(path: path, message: "must be \(c)")
        }
        if let e = s["enum"]?.array, !e.contains(v) {
            return Violation(path: path, message: "must be one of \(e.map(\.description).joined(separator: ", "))")
        }
        if case .string(let str) = v {
            if let p = s["pattern"]?.string, str.range(of: p, options: .regularExpression) == nil {
                return Violation(path: path, message: "\"\(str)\" does not match \(p)")
            }
            if let m = s["minLength"]?.double, Double(str.count) < m {
                return Violation(path: path, message: "shorter than \(Int(m))")
            }
        }
        if case .number(let n) = v {
            if let m = s["minimum"]?.double, n < m { return Violation(path: path, message: "below minimum \(Units.number(m))") }
            if let m = s["maximum"]?.double, n > m { return Violation(path: path, message: "above maximum \(Units.number(m))") }
        }
        if case .array(let arr) = v {
            if let m = s["minItems"]?.double, Double(arr.count) < m { return Violation(path: path, message: "needs at least \(Int(m)) items") }
            if let m = s["maxItems"]?.double, Double(arr.count) > m { return Violation(path: path, message: "allows at most \(Int(m)) items") }
            if let items = s["items"], items.object != nil {
                for (i, it) in arr.enumerated() {
                    if let bad = check(it, items, path + [String(i)]) { return bad }
                }
            }
        }
        if case .object(let obj) = v {
            for r in s["required"]?.array?.compactMap(\.string) ?? [] where obj[r] == nil {
                return Violation(path: path, message: "missing required \(r)")
            }
            let props = s["properties"]?.object ?? [:]
            for key in obj.keys.sorted() {
                let child = obj[key]!
                if let ps = props[key] {
                    if let bad = check(child, ps, path + [key]) { return bad }
                } else if let ap = s["additionalProperties"] {
                    if case .bool(false) = ap { return Violation(path: path + [key], message: "unknown key \(key)") }
                    if ap.object != nil, let bad = check(child, ap, path + [key]) { return bad }
                }
            }
        }
        if let one = s["oneOf"]?.array {
            let ok = one.filter { check(v, $0, path) == nil }.count
            if ok != 1 { return Violation(path: path, message: ok == 0 ? "matches none of the allowed forms" : "is ambiguous") }
        }
        if let any = s["anyOf"]?.array, !any.contains(where: { check(v, $0, path) == nil }) {
            return Violation(path: path, message: "matches none of the allowed forms")
        }
        return nil
    }
}
