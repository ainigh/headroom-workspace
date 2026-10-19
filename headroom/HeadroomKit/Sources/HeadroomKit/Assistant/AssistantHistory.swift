import Foundation
#if canImport(SQLite3)
import SQLite3
#endif

// Summary history and the turn log (headroom-spec §8.10). The full MESSAGE
// is never stored. On macOS both live in SQLite (Application
// Support/Headroom/assistant.db); elsewhere (tests, Linux) in JSON files.

public struct SummaryRecord: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var at: Date
    public var scope: String
    public var mode: String
    public var status: String?
    public var summary: String
    public var action: String?

    public init(id: String = UUID().uuidString, at: Date, scope: String, mode: String, status: String?, summary: String, action: String?) {
        self.id = id; self.at = at; self.scope = scope; self.mode = mode; self.status = status; self.summary = summary; self.action = action
    }
}

public struct CallRecord: Codable, Sendable, Hashable {
    public var call: String
    public var rows: Int
    public var cost: Int
    public var error: String?
}

/// One logged turn: everything except the full message.
public struct TurnRecord: Codable, Sendable, Hashable {
    public var at: Date
    public var scope: String
    public var mode: String
    public var model: String
    public var question: String
    public var summary: String?
    public var status: String?
    public var calls: [CallRecord]
    public var actionRequest: String?
    public var actionExplanation: String?
    public var decision: String?
    public var actionResult: String?
    public var incomplete: Bool
}

public protocol SummaryHistory: Sendable {
    func all() -> [SummaryRecord]
    func append(_ r: SummaryRecord)
    func delete(ids: Set<String>)
    func delete(before: Date)
    func deleteAll()
}

extension SummaryHistory {
    /// The latest n summaries, oldest first (deleted ones are gone, so the
    /// window refills from older ones).
    public func window(_ n: Int) -> [SummaryRecord] {
        Array(all().sorted { $0.at < $1.at }.suffix(max(0, n)))
    }
}

public protocol AssistantLog: Sendable {
    func append(_ t: TurnRecord)
    func recent(_ n: Int) -> [TurnRecord]
    func prune(olderThan days: Int)
}

// MARK: JSON files

/// Summaries in one JSON file (written atomically on each change).
public final class FileSummaryHistory: SummaryHistory, @unchecked Sendable {
    let url: URL
    let lock = NSLock()
    var cache: [SummaryRecord]?

    public init(url: URL) { self.url = url }

    static let encoder: JSONEncoder = { let e = JSONEncoder(); e.dateEncodingStrategy = .iso8601; e.outputFormatting = [.sortedKeys]; return e }()
    static let decoder: JSONDecoder = { let d = JSONDecoder(); d.dateDecodingStrategy = .iso8601; return d }()

    func load() -> [SummaryRecord] {
        if let c = cache { return c }
        let v = (try? Data(contentsOf: url)).flatMap { try? Self.decoder.decode([SummaryRecord].self, from: $0) } ?? []
        cache = v
        return v
    }

    func save(_ v: [SummaryRecord]) {
        cache = v
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let d = try? Self.encoder.encode(v) { try? d.write(to: url, options: .atomic) }
    }

    public func all() -> [SummaryRecord] { lock.lock(); defer { lock.unlock() }; return load() }
    public func append(_ r: SummaryRecord) { lock.lock(); defer { lock.unlock() }; save(load() + [r]) }
    public func delete(ids: Set<String>) { lock.lock(); defer { lock.unlock() }; save(load().filter { !ids.contains($0.id) }) }
    public func delete(before: Date) { lock.lock(); defer { lock.unlock() }; save(load().filter { $0.at >= before }) }
    public func deleteAll() { lock.lock(); defer { lock.unlock() }; save([]) }
}

/// The turn log as JSON lines.
public final class JSONLAssistantLog: AssistantLog, @unchecked Sendable {
    let url: URL
    let lock = NSLock()

    public init(url: URL) { self.url = url }

    public func append(_ t: TurnRecord) {
        lock.lock(); defer { lock.unlock() }
        guard var d = try? FileSummaryHistory.encoder.encode(t) else { return }
        d.append(0x0a)
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let h = try? FileHandle(forWritingTo: url) {
            h.seekToEndOfFile(); h.write(d); try? h.close()
        } else {
            try? d.write(to: url)
        }
    }

    func records() -> [TurnRecord] {
        guard let d = try? Data(contentsOf: url) else { return [] }
        return d.split(separator: 0x0a).compactMap { try? FileSummaryHistory.decoder.decode(TurnRecord.self, from: Data($0)) }
    }

    public func recent(_ n: Int) -> [TurnRecord] { lock.lock(); defer { lock.unlock() }; return Array(records().suffix(n)) }

    public func prune(olderThan days: Int) {
        lock.lock(); defer { lock.unlock() }
        let cut = Date().addingTimeInterval(-Double(days) * 86400)
        let keep = records().filter { $0.at >= cut }
        var out = Data()
        for r in keep { if let d = try? FileSummaryHistory.encoder.encode(r) { out.append(d); out.append(0x0a) } }
        try? out.write(to: url, options: .atomic)
    }
}

// MARK: SQLite (macOS)

#if canImport(SQLite3)
/// assistant.db: `summaries` and `turns` tables.
public final class SQLiteAssistantStore: SummaryHistory, AssistantLog, @unchecked Sendable {
    var db: OpaquePointer?
    let lock = NSLock()

    public init?(url: URL) {
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        guard sqlite3_open_v2(url.path, &db, SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE | SQLITE_OPEN_FULLMUTEX, nil) == SQLITE_OK else { return nil }
        exec("PRAGMA journal_mode=WAL")
        exec("""
            CREATE TABLE IF NOT EXISTS summaries (id TEXT PRIMARY KEY, at REAL NOT NULL, scope TEXT, mode TEXT, status TEXT, summary TEXT NOT NULL, action TEXT);
            CREATE INDEX IF NOT EXISTS summaries_at ON summaries(at);
            CREATE TABLE IF NOT EXISTS turns (at REAL NOT NULL, record TEXT NOT NULL);
            CREATE INDEX IF NOT EXISTS turns_at ON turns(at);
            """)
    }

    deinit { sqlite3_close(db) }

    @discardableResult
    func exec(_ sql: String) -> Bool { sqlite3_exec(db, sql, nil, nil, nil) == SQLITE_OK }

    static let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    func run(_ sql: String, _ bind: [Any?], row: ((OpaquePointer) -> Void)? = nil) {
        lock.lock(); defer { lock.unlock() }
        var st: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &st, nil) == SQLITE_OK else { return }
        defer { sqlite3_finalize(st) }
        for (i, v) in bind.enumerated() {
            let idx = Int32(i + 1)
            switch v {
            case let s as String: sqlite3_bind_text(st, idx, s, -1, Self.transient)
            case let d as Double: sqlite3_bind_double(st, idx, d)
            default: sqlite3_bind_null(st, idx)
            }
        }
        while sqlite3_step(st) == SQLITE_ROW { row?(st!) }
    }

    static func text(_ st: OpaquePointer, _ i: Int32) -> String? {
        sqlite3_column_text(st, i).map { String(cString: $0) }
    }

    public func all() -> [SummaryRecord] {
        var out: [SummaryRecord] = []
        run("SELECT id, at, scope, mode, status, summary, action FROM summaries ORDER BY at", []) { st in
            out.append(SummaryRecord(id: Self.text(st, 0) ?? "", at: Date(timeIntervalSince1970: sqlite3_column_double(st, 1)),
                                     scope: Self.text(st, 2) ?? "", mode: Self.text(st, 3) ?? "", status: Self.text(st, 4),
                                     summary: Self.text(st, 5) ?? "", action: Self.text(st, 6)))
        }
        return out
    }

    public func append(_ r: SummaryRecord) {
        run("INSERT OR REPLACE INTO summaries VALUES (?,?,?,?,?,?,?)", [r.id, r.at.timeIntervalSince1970, r.scope, r.mode, r.status, r.summary, r.action])
    }

    public func delete(ids: Set<String>) { for id in ids { run("DELETE FROM summaries WHERE id = ?", [id]) } }
    public func delete(before: Date) { run("DELETE FROM summaries WHERE at < ?", [before.timeIntervalSince1970]) }
    public func deleteAll() { run("DELETE FROM summaries", []) }

    public func append(_ t: TurnRecord) {
        guard let d = try? FileSummaryHistory.encoder.encode(t) else { return }
        run("INSERT INTO turns VALUES (?, ?)", [t.at.timeIntervalSince1970, String(decoding: d, as: UTF8.self)])
    }

    public func recent(_ n: Int) -> [TurnRecord] {
        var out: [TurnRecord] = []
        run("SELECT record FROM turns ORDER BY at DESC LIMIT ?", [Double(n)]) { st in
            if let s = Self.text(st, 0), let r = try? FileSummaryHistory.decoder.decode(TurnRecord.self, from: Data(s.utf8)) { out.append(r) }
        }
        return out.reversed()
    }

    public func prune(olderThan days: Int) {
        run("DELETE FROM turns WHERE at < ?", [Date().addingTimeInterval(-Double(days) * 86400).timeIntervalSince1970])
    }
}
#endif

public enum AssistantPersistence {
    /// SQLite where the system has it, JSON files otherwise.
    public static func open(dir: URL) -> (SummaryHistory, AssistantLog) {
        #if canImport(SQLite3)
        if let s = SQLiteAssistantStore(url: dir.appendingPathComponent("assistant.db")) { return (s, s) }
        #endif
        return (FileSummaryHistory(url: dir.appendingPathComponent("summaries.json")),
                JSONLAssistantLog(url: dir.appendingPathComponent("assistant-log.jsonl")))
    }
}
