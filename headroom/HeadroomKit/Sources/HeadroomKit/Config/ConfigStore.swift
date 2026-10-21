import Foundation
import Observation
import Yams
#if canImport(CoreServices)
import CoreServices
#endif

/// A problem with one config file, shown as a non-blocking banner.
public struct ConfigProblem: Sendable, Hashable, Identifiable {
    public var file: String
    public var line: Int?
    public var message: String
    public var id: String { file + ":" + String(line ?? 0) + ":" + message }
    public var text: String { line.map { "\(file) line \($0): \(message)" } ?? "\(file): \(message)" }
}

/// Loads the YAML files from Application Support, validating each against
/// its JSON Schema. A file that fails keeps its last good value (or the
/// shipped default); it never crashes anything.
public struct ConfigLoader: Sendable {
    public static let files = ["groups", "layout", "thresholds", "modes", "sampling", "assistant", "api", "notifications", "retention"]

    public let userDir: URL
    public let defaultsDir: URL

    public init(userDir: URL, defaultsDir: URL) {
        self.userDir = userDir
        self.defaultsDir = defaultsDir
    }

    /// Copies shipped defaults that are missing (first run, or a new file in
    /// an update). Never overwrites what the user has.
    public func bootstrap() throws {
        let fm = FileManager.default
        try fm.createDirectory(at: userDir.appendingPathComponent("prompts"), withIntermediateDirectories: true)
        var names = Self.files.map { "\($0).yaml" }
        if let prompts = try? fm.contentsOfDirectory(atPath: defaultsDir.appendingPathComponent("prompts").path) {
            names += prompts.map { "prompts/\($0)" }
        }
        for n in names {
            let dst = userDir.appendingPathComponent(n)
            let src = defaultsDir.appendingPathComponent(n)
            if !fm.fileExists(atPath: dst.path), fm.fileExists(atPath: src.path) {
                try fm.copyItem(at: src, to: dst)
            }
        }
    }

    /// Paths watched for hot reload.
    public func watchedPaths(_ c: AppConfig?) -> [URL] {
        var out = Self.files.map { userDir.appendingPathComponent("\($0).yaml") }
        let a = c?.assistant
        out.append(userDir.appendingPathComponent(a?.system_prompt ?? "prompts/system.md"))
        out.append(userDir.appendingPathComponent(a?.request_template ?? "prompts/request.md.tmpl"))
        out.append(userDir.appendingPathComponent("prompts/text-tools.md"))
        out.append(userDir.appendingPathComponent("prompts/notices.md"))
        return out
    }

    func schema(_ name: String) -> JSONSchema? {
        guard let data = try? Data(contentsOf: defaultsDir.appendingPathComponent("schemas/\(name).schema.json")) else { return nil }
        return try? JSONSchema(data: data)
    }

    /// Validates YAML text for a file without loading it (Settings uses this
    /// before saving an edit).
    public func check(_ name: String, text: String) -> ConfigProblem? {
        do {
            let doc = try YAMLDocument.parse(text)
            if let s = schema(name), let v = s.validate(migrate(name, doc.value)) {
                return ConfigProblem(file: "\(name).yaml", line: doc.line(for: v.pointer), message: v.description)
            }
            _ = try decodeFile(name, migrate(name, doc.value))
            return nil
        } catch let e as YamlError {
            return ConfigProblem(file: "\(name).yaml", line: yamlLine(e), message: "\(e)")
        } catch let p as ConfigProblem {
            return p
        } catch {
            return ConfigProblem(file: "\(name).yaml", line: nil, message: error.localizedDescription)
        }
    }

    private func yamlLine(_ e: YamlError) -> Int? {
        switch e {
        case .scanner(_, _, let mark, _), .parser(_, _, let mark, _), .composer(_, _, let mark, _):
            return mark.line
        default:
            return nil
        }
    }

    /// Forward migrations keyed by file and from-version. Files carry
    /// `version:`; a migrated file is rewritten after a .vN.bak backup.
    public static let currentVersion = 1
    func migrate(_ name: String, _ v: JSONValue) -> JSONValue {
        // No migrations yet: version 1 is current. Add steps here as
        // `if version == 1 { v = upgrade1to2(v) }`.
        v
    }

    func decodeFile(_ name: String, _ v: JSONValue) throws -> Any {
        let data = try JSONEncoder().encode(v)
        let dec = JSONDecoder()
        switch name {
        case "groups":
            let f = try dec.decode(GroupsFile.self, from: data)
            // Duplicate ids would make every id-keyed lookup ambiguous: reject
            // the file so the last good one stays.
            var seen = Set<String>()
            for g in f.groups where !seen.insert(g.id).inserted {
                throw ConfigProblem(file: "\(name).yaml", line: nil, message: "duplicate group id \(g.id)")
            }
            return f
        case "layout": return try dec.decode(LayoutFile.self, from: data)
        case "thresholds":
            var out: [String: Threshold] = [:]
            for (k, val) in v.object ?? [:] where k != "version" {
                out[k] = try dec.decode(Threshold.self, from: JSONEncoder().encode(val))
            }
            return out
        case "modes": return try dec.decode(ModesFile.self, from: data)
        case "sampling": return try dec.decode(SamplingFile.self, from: data)
        case "assistant": return try dec.decode(AssistantFile.self, from: data)
        case "api": return try dec.decode(APIFile.self, from: data)
        case "notifications": return try dec.decode(NotificationsFile.self, from: data)
        case "retention": return try dec.decode(RetentionFile.self, from: data)
        default: throw ConfigProblem(file: name, line: nil, message: "unknown file")
        }
    }

    private func loadOne(_ name: String, from dir: URL) -> (Any?, ConfigProblem?) {
        let url = dir.appendingPathComponent("\(name).yaml")
        guard let text = try? String(contentsOf: url, encoding: .utf8) else {
            return (nil, ConfigProblem(file: "\(name).yaml", line: nil, message: "missing"))
        }
        do {
            let doc = try YAMLDocument.parse(text)
            let value = migrate(name, doc.value)
            if let s = schema(name), let v = s.validate(value) {
                return (nil, ConfigProblem(file: "\(name).yaml", line: doc.line(for: v.pointer), message: v.description))
            }
            return (try decodeFile(name, value), nil)
        } catch let e as YamlError {
            return (nil, ConfigProblem(file: "\(name).yaml", line: yamlLine(e), message: "\(e)"))
        } catch let p as ConfigProblem {
            return (nil, p)
        } catch {
            return (nil, ConfigProblem(file: "\(name).yaml", line: nil, message: "\(error)"))
        }
    }

    /// Loads everything. Each failing file falls back to its previous value,
    /// then to the shipped default.
    public func load(previous: AppConfig?) -> (AppConfig, [ConfigProblem]) {
        var problems: [ConfigProblem] = []
        var values: [String: Any] = [:]
        for name in Self.files {
            let (v, p) = loadOne(name, from: userDir)
            if let v { values[name] = v; continue }
            if let p { problems.append(p) }
            if let prev = previous, let pv = Self.value(of: name, in: prev) {
                values[name] = pv
            } else {
                let (d, dp) = loadOne(name, from: defaultsDir)
                if let d { values[name] = d } else if let dp { problems.append(ConfigProblem(file: "shipped " + dp.file, line: dp.line, message: dp.message)) }
            }
        }
        let assistant = (values["assistant"] as? AssistantFile) ?? AssistantFile(version: 1)
        func text(_ rel: String?, _ fallback: String) -> String {
            let rel = rel ?? fallback
            if rel.contains("..") || rel.hasPrefix("/") {
                problems.append(ConfigProblem(file: "assistant.yaml", line: nil, message: "\(rel) must be inside the config folder"))
                return (try? String(contentsOf: defaultsDir.appendingPathComponent(fallback), encoding: .utf8)) ?? ""
            }
            if let s = try? String(contentsOf: userDir.appendingPathComponent(rel), encoding: .utf8) { return s }
            return (try? String(contentsOf: defaultsDir.appendingPathComponent(fallback), encoding: .utf8)) ?? ""
        }
        let cfg = AppConfig(
            groups: (values["groups"] as? GroupsFile) ?? GroupsFile(version: 1, groups: []),
            layout: (values["layout"] as? LayoutFile) ?? LayoutFile(version: 1),
            thresholds: (values["thresholds"] as? [String: Threshold]) ?? [:],
            modes: (values["modes"] as? ModesFile) ?? ModesFile(version: 1, modes: [:]),
            sampling: (values["sampling"] as? SamplingFile) ?? SamplingFile(version: 1, rates: .init(view_open: Rates(), idle: Rates())),
            assistant: assistant,
            api: (values["api"] as? APIFile) ?? APIFile(version: 1),
            notifications: (values["notifications"] as? NotificationsFile) ?? NotificationsFile(version: 1),
            retention: (values["retention"] as? RetentionFile) ?? RetentionFile(version: 1),
            systemPrompt: text(assistant.system_prompt, "prompts/system.md"),
            requestTemplate: text(assistant.request_template, "prompts/request.md.tmpl"),
            textToolsPrompt: text("prompts/text-tools.md", "prompts/text-tools.md"),
            notices: Notices(text("prompts/notices.md", "prompts/notices.md"))
        )
        problems += crossCheck(cfg)
        return (cfg, problems)
    }

    static func value(of name: String, in c: AppConfig) -> Any? {
        switch name {
        case "groups": return c.groups
        case "layout": return c.layout
        case "thresholds": return c.thresholds
        case "modes": return c.modes
        case "sampling": return c.sampling
        case "assistant": return c.assistant
        case "api": return c.api
        case "notifications": return c.notifications
        case "retention": return c.retention
        default: return nil
        }
    }

    /// Cross-file references (a pinned group that doesn't exist, ...).
    func crossCheck(_ c: AppConfig) -> [ConfigProblem] {
        var out: [ConfigProblem] = []
        let ids = Set(c.groups.groups.map(\.id))
        for p in c.layout.pinned where !ids.contains(p) {
            out.append(ConfigProblem(file: "layout.yaml", line: nil, message: "pinned group \(p) is not in groups.yaml"))
        }
        if c.layout.pinned.count > 4 {
            out.append(ConfigProblem(file: "layout.yaml", line: nil, message: "at most four key groups"))
        }
        for (mode, m) in c.modes.modes {
            for g in (m.groups ?? [:]).keys where !ids.contains(g) {
                out.append(ConfigProblem(file: "modes.yaml", line: nil, message: "\(mode) binds unknown group \(g)"))
            }
        }
        for g in c.groups.groups where c.thresholds[g.id] == nil {
            out.append(ConfigProblem(file: "thresholds.yaml", line: nil, message: "no thresholds for \(g.id); its status stays grey"))
        }
        if ids.count != c.groups.groups.count {
            out.append(ConfigProblem(file: "groups.yaml", line: nil, message: "duplicate group ids"))
        }
        return out
    }

    /// Writes a file after validating it (Settings). Returns the problem, if any.
    @discardableResult
    public func save(_ name: String, text: String) -> ConfigProblem? {
        if let p = check(name, text: text) { return p }
        let url = userDir.appendingPathComponent("\(name).yaml")
        do {
            try text.write(to: url, atomically: true, encoding: .utf8)
            return nil
        } catch {
            return ConfigProblem(file: "\(name).yaml", line: nil, message: error.localizedDescription)
        }
    }

    /// Reads a file's current text (Settings editor).
    public func text(of name: String) -> String {
        (try? String(contentsOf: userDir.appendingPathComponent("\(name).yaml"), encoding: .utf8)) ?? ""
    }
}

extension ConfigProblem: Error {}

/// Publishes a new immutable AppConfig whenever a file changes.
@MainActor
@Observable
public final class ConfigStore {
    public private(set) var config: AppConfig
    public private(set) var problems: [ConfigProblem]
    public private(set) var generation = 0
    public let loader: ConfigLoader
    /// Called after every reload with the new value.
    @ObservationIgnored public var onChange: ((AppConfig) -> Void)?

    @ObservationIgnored private var mtimes: [String: Date] = [:]
    @ObservationIgnored private var timer: Timer?
    #if canImport(CoreServices)
    @ObservationIgnored private var watcher: DirectoryWatcher?
    #endif

    public init(loader: ConfigLoader) {
        self.loader = loader
        try? loader.bootstrap()
        let (c, p) = loader.load(previous: nil)
        config = c
        problems = p
        mtimes = currentMtimes()
    }

    /// Re-reads every file now.
    public func reload() {
        let (c, p) = loader.load(previous: config)
        config = c
        problems = p
        generation += 1
        mtimes = currentMtimes()
        onChange?(c)
    }

    private func currentMtimes() -> [String: Date] {
        var out: [String: Date] = [:]
        for u in loader.watchedPaths(config) {
            out[u.path] = (try? FileManager.default.attributesOfItem(atPath: u.path)[.modificationDate]) as? Date ?? .distantPast
        }
        return out
    }

    /// Reloads when a file changes: FSEvents on the config folder on macOS
    /// (implementation.md §2), a cheap mtime poll elsewhere. Either way the
    /// reload only happens when an mtime actually changed.
    public func startWatching(every interval: TimeInterval = 2) {
        stopWatching()
        #if canImport(CoreServices)
        watcher = DirectoryWatcher(paths: [loader.userDir.path]) { [weak self] in
            Task { @MainActor in self?.checkForChanges() }
        }
        if watcher != nil { return }
        #endif
        timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.checkForChanges() }
        }
    }

    public func stopWatching() {
        timer?.invalidate()
        timer = nil
        #if canImport(CoreServices)
        watcher = nil
        #endif
    }

    public func checkForChanges() {
        if currentMtimes() != mtimes { reload() }
    }

    /// Saves an edited file (validated first) and reloads.
    @discardableResult
    public func save(_ name: String, text: String) -> ConfigProblem? {
        let p = loader.save(name, text: text)
        if p == nil { reload() }
        return p
    }
}

#if canImport(CoreServices)
/// An FSEvents stream over a directory tree, delivering on the main queue.
final class DirectoryWatcher {
    private var stream: FSEventStreamRef?
    private let callback: () -> Void

    init?(paths: [String], latency: Double = 0.3, callback: @escaping () -> Void) {
        self.callback = callback
        var ctx = FSEventStreamContext(version: 0, info: nil, retain: nil, release: nil, copyDescription: nil)
        ctx.info = Unmanaged.passUnretained(self).toOpaque()
        let cb: FSEventStreamCallback = { _, info, _, _, _, _ in
            guard let info else { return }
            Unmanaged<DirectoryWatcher>.fromOpaque(info).takeUnretainedValue().callback()
        }
        let flags = FSEventStreamCreateFlags(kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer)
        guard let s = FSEventStreamCreate(kCFAllocatorDefault, cb, &ctx, paths as CFArray,
                                          FSEventStreamEventId(kFSEventStreamEventIdSinceNow), latency, flags) else { return nil }
        stream = s
        FSEventStreamSetDispatchQueue(s, DispatchQueue.main)
        FSEventStreamStart(s)
    }

    deinit {
        if let s = stream {
            FSEventStreamStop(s)
            FSEventStreamInvalidate(s)
            FSEventStreamRelease(s)
        }
    }
}
#endif
