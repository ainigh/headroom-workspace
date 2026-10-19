import Foundation

/// A column of a sysq table (sysq-design §12.1).
public struct SysqColumn: Codable, Hashable, Sendable {
    public var name: String
    public var type: String
    public var unit: String?
    public var values: [String]?
    public var schema: [SysqColumn]?
    public var doc: String?

    public init(name: String, type: String, unit: String? = nil) {
        self.name = name; self.type = type; self.unit = unit
    }

    public var isNumeric: Bool { ["int", "float", "bytes", "duration", "pid", "port", "uid"].contains(type) }
}

public struct SysqMeta: Codable, Hashable, Sendable {
    public var rows: Int?
    public var truncated: Bool?
    public var source: [String]?
    public var cost_ms: Int?
    public var sensitivity: String?
    public var dropped_by_limit: Int?
    public var snapshot_ts: String?
    public var cached: Bool?
    public var error: String?
    public var unavailable: String?
    public var pushdown: [String]?
    public var fixture: Bool?
    public var redacted: Int?
}

/// The universal sysq value: schema, rows, meta. Columns are discovered,
/// never assumed (separation-of-concerns §8).
public struct SysqTable: Codable, Hashable, Sendable {
    public var schema: [SysqColumn]
    public var rows: [[JSONValue]]
    public var meta: SysqMeta?

    public init(schema: [SysqColumn], rows: [[JSONValue]], meta: SysqMeta? = nil) {
        self.schema = schema; self.rows = rows; self.meta = meta
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schema = (try? c.decode([SysqColumn].self, forKey: .schema)) ?? []
        rows = (try? c.decode([[JSONValue]].self, forKey: .rows)) ?? []
        meta = try? c.decode(SysqMeta.self, forKey: .meta)
    }

    public var count: Int { rows.count }
    public var isEmpty: Bool { rows.isEmpty }

    public func index(of column: String) -> Int? { schema.firstIndex { $0.name == column } }
    public func column(_ name: String) -> SysqColumn? { schema.first { $0.name == name } }

    public func value(_ row: Int, _ column: String) -> JSONValue? {
        guard row >= 0, row < rows.count, let i = index(of: column), i < rows[row].count else { return nil }
        return rows[row][i]
    }

    public func double(_ row: Int, _ column: String) -> Double? { value(row, column)?.double }
    public func string(_ row: Int, _ column: String) -> String? { value(row, column)?.string }

    /// Formatted cell (bytes humanized, durations, units).
    public func text(_ row: Int, _ column: String, unit: String? = nil) -> String {
        guard let col = self.column(column), let v = value(row, column) else { return "—" }
        return Formatter.text(v, column: col, unit: unit)
    }

    /// The column that best names a row (for consumer lists).
    public func nameColumn(preferred: String? = nil) -> String? {
        if let p = preferred, index(of: p) != nil { return p }
        for c in ["name", "command", "model", "label", "path", "process", "app"] where index(of: c) != nil { return c }
        return schema.first { $0.type == "str" }?.name
    }

    /// Nested table in a cell.
    public func nested(_ row: Int, _ column: String) -> SysqTable? {
        guard let v = value(row, column), let data = try? JSONEncoder().encode(v) else { return nil }
        return try? JSONDecoder().decode(SysqTable.self, from: data)
    }

    /// Rows sorted by a column (numbers numerically, missing last).
    public func sorted(by column: String, descending: Bool) -> SysqTable {
        guard let i = index(of: column) else { return self }
        var out = self
        out.rows.sort { a, b in
            let x = i < a.count ? a[i] : .null, y = i < b.count ? b[i] : .null
            if x.isNull { return false }
            if y.isNull { return true }
            if let dx = x.double, let dy = y.double { return descending ? dx > dy : dx < dy }
            let sx = x.string ?? "", sy = y.string ?? ""
            return descending ? sx > sy : sx < sy
        }
        return out
    }

    /// Compact text for model context: header + rows, capped.
    public func compact(maxRows: Int = 15, maxCell: Int = 60) -> String {
        if schema.isEmpty {
            if let e = meta?.error, !e.isEmpty { return "(error: \(e))" }
            if let u = meta?.unavailable, !u.isEmpty { return "(unavailable: \(u))" }
            return "(empty)"
        }
        var lines = [schema.map { c in c.unit.map { "\(c.name) (\($0))" } ?? c.name }.joined(separator: " | ")]
        for r in 0..<min(rows.count, maxRows) {
            lines.append(schema.map { c in
                var t = text(r, c.name)
                if t.count > maxCell { t = String(t.prefix(maxCell - 1)) + "…" }
                return t.replacingOccurrences(of: "\n", with: " ")
            }.joined(separator: " | "))
        }
        if rows.count > maxRows { lines.append("… \(rows.count - maxRows) more rows") }
        if meta?.truncated == true { lines.append("(truncated)") }
        if let u = meta?.unavailable, !u.isEmpty { lines.append("(unavailable: \(u))") }
        if let e = meta?.error, !e.isEmpty { lines.append("(error: \(e))") }
        return lines.joined(separator: "\n")
    }
}

/// One emitted output of a recipe or script.
public struct SysqItem: Codable, Hashable, Sendable {
    public var label: String?
    public var table: SysqTable

    public init(label: String? = nil, table: SysqTable) { self.label = label; self.table = table }
}

/// Cell formatting shared by every view.
public enum Formatter {
    public static func text(_ v: JSONValue, column: SysqColumn, unit: String? = nil) -> String {
        if v.isNull { return "—" }
        let unit = unit ?? column.unit
        switch column.type {
        case "bytes":
            guard let d = v.double else { return v.description }
            return unit == "bytes/s" ? Units.bytes(d) + "/s" : Units.bytes(d)
        case "duration":
            return v.double.map(Units.duration) ?? v.description
        case "float", "int":
            guard let d = v.double else { return v.description }
            if unit == "%" { return Units.percent(d) }
            if unit == "bytes" { return Units.bytes(d) }
            if unit == "bytes/s" { return Units.bytes(d) + "/s" }
            let s = Units.number(d)
            return unit.map { "\(s) \($0)" } ?? s
        case "bool":
            return v.bool == true ? "yes" : "no"
        case "ts":
            guard let s = v.string, let date = ISO8601DateFormatter().date(from: s) else { return v.description }
            let f = DateFormatter()
            f.dateStyle = Calendar.current.isDateInToday(date) ? .none : .short
            f.timeStyle = .short
            return f.string(from: date)
        case "table":
            if let rows = v["rows"]?.array { return "\(rows.count) rows" }
            return "table"
        default:
            return v.description
        }
    }

    /// Formats a raw number for a headline in a binding's unit.
    public static func headline(_ v: JSONValue?, column: SysqColumn?, unit: String?) -> String {
        guard let v, let column else { return "—" }
        return text(v, column: column, unit: unit)
    }
}
