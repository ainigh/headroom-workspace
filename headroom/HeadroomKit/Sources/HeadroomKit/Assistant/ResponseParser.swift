import Foundation

// Parses the five-section answer (headroom-spec §8.4) and the text-loop
// sections (## RECIPE / ## QUERY / ## DESCRIBE). Malformed input never
// throws: whatever parses is kept and the response is marked incomplete.

public enum AssistantStatus: String, Sendable, Hashable, CaseIterable {
    case ok = "OK", attention = "ATTENTION", warning = "WARNING", critical = "CRITICAL"

    public var groupStatus: GroupStatus {
        switch self { case .ok: return .green; case .attention: return .amber; case .warning: return .amber; case .critical: return .red }
    }
}

/// An action as the model wrote it: `name(param=value, ...)`.
public struct ParsedAction: Sendable, Hashable {
    public var name: String
    public var params: [String: JSONValue]

    public var text: String {
        "\(name)(\(params.keys.sorted().map { "\($0)=\(params[$0]!.json)" }.joined(separator: ", ")))"
    }
}

public struct AssistantResponse: Sendable, Hashable {
    public var summary: String?
    public var message: String?
    public var status: AssistantStatus?
    public var statusLine: String?
    public var actionRequest: ParsedAction?
    /// The raw ACTION REQUEST text when it didn't parse or isn't allowed.
    public var actionProblem: String?
    public var actionExplanation: String?
    public var incomplete: Bool
    public var raw: String
}

public enum ResponseParser {
    static let headers = ["SUMMARY", "MESSAGE", "STATUS", "ACTION REQUEST", "ACTION EXPLANATION", "QUERY", "RECIPE", "DESCRIBE"]

    /// Splits text into `## NAME` sections (case-insensitive, tolerant of
    /// "## 1. SUMMARY", "**SUMMARY**" headings and trailing colons).
    public static func sections(_ text: String) -> [String: String] {
        let lines = text.components(separatedBy: "\n")
        // A mixed-case bold line with text after it ("**Status:** memory is
        // fine") is often prose inside MESSAGE: it counts as a heading only
        // when the response has no proper heading of that name.
        let proper = Set(lines.compactMap { l in header(l).flatMap { $0.weak ? nil : $0.name } })
        var out: [String: String] = [:]
        var current: String?
        var buf: [String] = []
        func flush() {
            if let c = current, out[c] == nil { out[c] = buf.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines) }
            buf = []
        }
        for line in lines {
            if let h = header(line), !(h.weak && proper.contains(h.name)) {
                flush()
                current = h.name
                if !h.rest.isEmpty { buf.append(h.rest) }
            } else if current != nil {
                buf.append(line)
            }
        }
        flush()
        return out
    }

    static func header(_ line: String) -> (name: String, rest: String, weak: Bool)? {
        var l = line.trimmingCharacters(in: .whitespaces)
        let bold = !l.hasPrefix("#")
        guard l.hasPrefix("#") || l.hasPrefix("**") else { return nil }
        l = l.trimmingCharacters(in: CharacterSet(charactersIn: "#* "))
        if let r = l.range(of: "^[0-9]+[.)]\\s*", options: .regularExpression) { l.removeSubrange(r) }
        let upper = l.uppercased()
        for h in headers.sorted(by: { $0.count > $1.count }) where upper.hasPrefix(h) {
            let after = String(l.dropFirst(h.count))
            // The name must end there: "### Status of disks" is text, not STATUS.
            if let c = after.trimmingCharacters(in: .whitespaces).first, !":*—–-".contains(c) { return nil }
            let rest = after.trimmingCharacters(in: CharacterSet(charactersIn: "*:—–- ").union(.whitespaces))
            // A bold line is a heading only when it's nothing but the name
            // ("**Summary of findings**" inside MESSAGE is text).
            if bold, !rest.isEmpty, !line.contains(":") { return nil }
            let weak = bold && !rest.isEmpty && !l.hasPrefix(h)
            return (h, rest, weak)
        }
        return nil
    }

    static func isNone(_ s: String?) -> Bool {
        guard let s else { return true }
        let t = s.trimmingCharacters(in: CharacterSet(charactersIn: " \n\t.\"'`*")).lowercased()
        return t.isEmpty || t == "none" || t == "n/a" || t == "no action"
    }

    public static func parse(_ text: String, allowedActions: Set<String>?, actionsEnabled: Bool = true) -> AssistantResponse {
        let s = sections(text)
        var r = AssistantResponse(summary: s["SUMMARY"].flatMap { $0.isEmpty ? nil : firstLine($0) },
                                  message: s["MESSAGE"].flatMap { $0.isEmpty ? nil : $0 },
                                  status: nil, statusLine: nil, actionRequest: nil, actionProblem: nil,
                                  actionExplanation: isNone(s["ACTION EXPLANATION"]) ? nil : s["ACTION EXPLANATION"],
                                  incomplete: false, raw: text)
        if let st = s["STATUS"] {
            let line = firstLine(st)
            let up = line.uppercased()
            r.status = AssistantStatus.allCases.first { up.hasPrefix($0.rawValue) }
            if let st = r.status {
                r.statusLine = String(line.dropFirst(st.rawValue.count)).trimmingCharacters(in: CharacterSet(charactersIn: " —–-:*").union(.whitespaces))
            }
        }
        let present = ["SUMMARY", "MESSAGE", "STATUS", "ACTION REQUEST", "ACTION EXPLANATION"].filter { s[$0] != nil }
        r.incomplete = present.count < 5 || r.summary == nil || r.status == nil
        if let req = s["ACTION REQUEST"], !isNone(req) {
            if r.incomplete {
                r.actionProblem = "ignored because the response is incomplete: \(firstLine(req))"
            } else if !actionsEnabled {
                r.actionProblem = "ignored: action requests are off in assistant.yaml"
            } else if let a = parseCall(firstLine(req).trimmingCharacters(in: CharacterSet(charactersIn: "`"))) {
                if let allowed = allowedActions, !allowed.contains(a.name) {
                    r.actionProblem = "\(a.name) is not in the action catalog"
                } else {
                    r.actionRequest = a
                }
            } else {
                r.actionProblem = "couldn't read: \(firstLine(req))"
            }
        }
        return r
    }

    static func firstLine(_ s: String) -> String {
        s.split(separator: "\n", omittingEmptySubsequences: true).first.map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
    }

    /// Parses `name(key=value, key2="text", n=10)`. Values: quoted strings,
    /// numbers, true/false, or bare words (taken as strings).
    public static func parseCall(_ text: String) -> ParsedAction? {
        let t = text.trimmingCharacters(in: .whitespaces)
        guard let open = t.firstIndex(of: "("), t.hasSuffix(")") else {
            let name = t
            return name.range(of: "^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$", options: .regularExpression) != nil ? ParsedAction(name: name, params: [:]) : nil
        }
        let name = String(t[..<open]).trimmingCharacters(in: .whitespaces)
        guard name.range(of: "^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$", options: .regularExpression) != nil else { return nil }
        let inner = t[t.index(after: open)..<t.index(before: t.endIndex)]
        var params: [String: JSONValue] = [:]
        var i = inner.startIndex
        func skipSpace() { while i < inner.endIndex, inner[i] == " " || inner[i] == "," || inner[i] == "\n" { i = inner.index(after: i) } }
        while true {
            skipSpace()
            if i >= inner.endIndex { break }
            var key = ""
            while i < inner.endIndex, inner[i].isLetter || inner[i].isNumber || inner[i] == "_" { key.append(inner[i]); i = inner.index(after: i) }
            while i < inner.endIndex, inner[i] == " " { i = inner.index(after: i) }
            guard !key.isEmpty, i < inner.endIndex, inner[i] == "=" || inner[i] == ":" else { return nil }
            i = inner.index(after: i)
            while i < inner.endIndex, inner[i] == " " { i = inner.index(after: i) }
            guard i < inner.endIndex else { return nil }
            if inner[i] == "\"" || inner[i] == "'" {
                let q = inner[i]
                i = inner.index(after: i)
                var v = ""
                while i < inner.endIndex, inner[i] != q {
                    if inner[i] == "\\", inner.index(after: i) < inner.endIndex { i = inner.index(after: i) }
                    v.append(inner[i]); i = inner.index(after: i)
                }
                guard i < inner.endIndex else { return nil }
                i = inner.index(after: i)
                params[key] = .string(v)
            } else {
                var v = ""
                while i < inner.endIndex, inner[i] != "," { v.append(inner[i]); i = inner.index(after: i) }
                v = v.trimmingCharacters(in: .whitespaces)
                if v == "true" || v == "True" { params[key] = .bool(true) }
                else if v == "false" || v == "False" { params[key] = .bool(false) }
                else if let d = Double(v) { params[key] = .number(d) }
                else { params[key] = .string(v) }
            }
        }
        return ParsedAction(name: name, params: params)
    }

    /// A text-loop lookup request, when the reply is one instead of an answer.
    public enum Lookup: Sendable, Hashable {
        case recipe(ParsedAction), script(String), describe(String)
    }

    public static func lookup(_ text: String) -> Lookup? {
        let s = sections(text)
        if s["SUMMARY"] != nil && s["STATUS"] != nil { return nil }
        if let r = s["RECIPE"], let call = parseCall(stripFence(r)) { return .recipe(call) }
        if let q = s["QUERY"], !stripFence(q).isEmpty { return .script(stripFence(q)) }
        if let d = s["DESCRIBE"] {
            let name = stripFence(d).trimmingCharacters(in: CharacterSet(charactersIn: "\"'`() "))
            if !name.isEmpty { return .describe(name) }
        }
        return nil
    }

    static func stripFence(_ s: String) -> String {
        var lines = s.components(separatedBy: "\n")
        lines.removeAll { $0.trimmingCharacters(in: .whitespaces).hasPrefix("```") }
        return lines.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
