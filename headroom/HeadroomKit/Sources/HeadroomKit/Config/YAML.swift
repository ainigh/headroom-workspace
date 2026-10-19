import Foundation
import Yams

/// A parsed YAML document: its value and the line of every JSON pointer, so
/// a schema violation can be reported as "groups.yaml line 12".
public struct YAMLDocument: Sendable {
    public let value: JSONValue
    public let lines: [String: Int]

    public func line(for pointer: String) -> Int? {
        var p = pointer
        while true {
            if let l = lines[p] { return l }
            guard let i = p.lastIndex(of: "/"), p != "/" else { return lines["/"] }
            p = i == p.startIndex ? "/" : String(p[..<i])
        }
    }

    public static func parse(_ text: String) throws -> YAMLDocument {
        guard let node = try Yams.compose(yaml: text) else {
            return YAMLDocument(value: .object([:]), lines: ["/": 1])
        }
        var lines: [String: Int] = [:]
        let v = convert(node, path: "", lines: &lines)
        return YAMLDocument(value: v, lines: lines)
    }

    private static func convert(_ node: Node, path: String, lines: inout [String: Int]) -> JSONValue {
        lines[path.isEmpty ? "/" : path] = node.mark?.line
        switch node {
        case .mapping(let m):
            var out: [String: JSONValue] = [:]
            for (k, v) in m {
                let key = k.string ?? String(describing: k)
                out[key] = convert(v, path: path + "/" + key, lines: &lines)
            }
            return .object(out)
        case .sequence(let s):
            return .array(s.enumerated().map { i, n in convert(n, path: path + "/\(i)", lines: &lines) })
        case .scalar(let sc):
            switch node.tag.description {
            case "tag:yaml.org,2002:null": return .null
            case "tag:yaml.org,2002:bool": return node.bool.map(JSONValue.bool) ?? .string(sc.string)
            case "tag:yaml.org,2002:int": return node.int.map { .number(Double($0)) } ?? .string(sc.string)
            case "tag:yaml.org,2002:float": return node.float.map(JSONValue.number) ?? .string(sc.string)
            default: return .string(sc.string)
            }
        case .alias:
            return .null
        }
    }
}
