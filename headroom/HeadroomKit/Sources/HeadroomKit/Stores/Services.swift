import Foundation
import Observation
import Yams

// Starts sysqd and sysact (as per-user LaunchAgents running the binaries in
// the app bundle), registers Headroom's own sysqd clients over the admin
// socket, and writes config through (api.yaml → client table,
// sampling.yaml → schedules.yaml).

/// A background service Headroom keeps running.
public struct ServiceSpec: Sendable, Equatable {
    public var label: String
    public var executable: String
    public var arguments: [String]
    public var logPath: String

    public init(label: String, executable: String, arguments: [String], logPath: String) {
        self.label = label; self.executable = executable; self.arguments = arguments; self.logPath = logPath
    }
}

public protocol ServiceLauncher: Sendable {
    /// Installs (or updates) and starts the service; idempotent.
    func ensure(_ spec: ServiceSpec) async throws
    func restart(_ label: String) async throws
}

/// LaunchAgents under ~/Library/LaunchAgents: the services survive Headroom
/// quitting (history keeps recording) and restart after a crash. A plist is
/// rewritten only when its program changes (e.g. the app moved or updated).
public struct LaunchAgentLauncher: ServiceLauncher {
    public let agentsDir: URL
    /// Runs launchctl (a seam for tests; nil runs /bin/launchctl).
    let runner: (@Sendable ([String]) async throws -> String)?

    public init(home: URL = FileManager.default.homeDirectoryForCurrentUser) {
        self.init(home: home, runner: nil)
    }

    init(home: URL, runner: (@Sendable ([String]) async throws -> String)?) {
        agentsDir = home.appendingPathComponent("Library/LaunchAgents")
        self.runner = runner
    }

    /// The executable's modification time. An update replaces the binary at
    /// the same path; with this in the plist the plist changes too, so the
    /// running service is restarted onto the new binary.
    func stamp(_ s: ServiceSpec) -> String {
        guard let d = (try? FileManager.default.attributesOfItem(atPath: s.executable))?[.modificationDate] as? Date else { return "" }
        return String(Int64(d.timeIntervalSince1970))
    }

    func plist(_ s: ServiceSpec) -> String {
        let args = ([s.executable] + s.arguments).map { "    <string>\(xml($0))</string>" }.joined(separator: "\n")
        return """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <!-- Written by Headroom; binary modified \(stamp(s)) -->
        <plist version="1.0">
        <dict>
          <key>Label</key><string>\(xml(s.label))</string>
          <key>ProgramArguments</key>
          <array>
        \(args)
          </array>
          <key>RunAtLoad</key><true/>
          <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
          <key>ProcessType</key><string>Background</string>
          <key>LowPriorityIO</key><true/>
          <key>Nice</key><integer>5</integer>
          <key>StandardOutPath</key><string>\(xml(s.logPath))</string>
          <key>StandardErrorPath</key><string>\(xml(s.logPath))</string>
        </dict>
        </plist>

        """
    }

    func xml(_ s: String) -> String {
        s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;").replacingOccurrences(of: ">", with: "&gt;")
    }

    public func ensure(_ spec: ServiceSpec) async throws {
        let fm = FileManager.default
        try fm.createDirectory(at: agentsDir, withIntermediateDirectories: true)
        try fm.createDirectory(at: URL(fileURLWithPath: spec.logPath).deletingLastPathComponent(), withIntermediateDirectories: true)
        let url = agentsDir.appendingPathComponent("\(spec.label).plist")
        let text = plist(spec)
        let old = try? String(contentsOf: url, encoding: .utf8)
        let domain = "gui/\(getuid())"
        if old != text {
            _ = try? await launchctl(["bootout", "\(domain)/\(spec.label)"])
            try text.write(to: url, atomically: true, encoding: .utf8)
            _ = try await launchctl(["bootstrap", domain, url.path])
        } else if (try? await launchctl(["print", "\(domain)/\(spec.label)"])) == nil {
            _ = try await launchctl(["bootstrap", domain, url.path])
        } else {
            // Loaded but maybe not running: a clean exit (SIGTERM from
            // Activity Monitor exits 0) isn't restarted by KeepAlive's
            // SuccessfulExit=false. kickstart without -k starts it only if
            // it isn't running.
            _ = try? await launchctl(["kickstart", "\(domain)/\(spec.label)"])
        }
    }

    public func restart(_ label: String) async throws {
        _ = try await launchctl(["kickstart", "-k", "gui/\(getuid())/\(label)"])
    }

    @discardableResult
    func launchctl(_ args: [String]) async throws -> String {
        if let runner { return try await runner(args) }
        return try await Task.detached {
            let p = Process()
            p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
            p.arguments = args
            let out = Pipe()
            p.standardOutput = out
            p.standardError = out
            try p.run()
            let data = out.fileHandleForReading.readDataToEndOfFile()
            p.waitUntilExit()
            let text = String(decoding: data, as: UTF8.self)
            if p.terminationStatus != 0 {
                throw ClientError(.failed, "launchctl \(args.first ?? ""): \(text.trimmingCharacters(in: .whitespacesAndNewlines))")
            }
            return text
        }.value
    }
}

/// Builds sysqd's schedules.yaml from groups.yaml + sampling.yaml: one
/// schedule per headline/sparkline recipe and one per consumer list.
public enum ScheduleWriter {
    public static func document(_ c: AppConfig) -> JSONValue {
        let r = c.sampling.rates
        let hidden = Set(c.layout.hidden_groups ?? [])
        var seen: [String: Int] = [:]
        var schedules: [JSONValue] = []

        func add(name: String?, recipe: String, params: [String: JSONValue]?, kind: KeyPath<Rates, String?>, isHidden: Bool, extra: [String: JSONValue] = [:]) {
            let id = name ?? recipe
            var every = r.view_open[keyPath: kind] ?? "5s"
            var idle = r.idle[keyPath: kind] ?? "60s"
            var battery = r.on_battery?[keyPath: kind] ?? idle
            if isHidden, let h = c.sampling.hidden_groups?[keyPath: kind] { every = h; idle = h; battery = h }
            var s: [String: JSONValue] = ["recipe": .string(recipe), "every": .string(every), "idle_every": .string(idle), "battery_every": .string(battery)]
            if let name { s["name"] = .string(name) }
            if let params, !params.isEmpty { s["params"] = .object(params) }
            for (k, v) in extra { s[k] = v }
            if let i = seen[id] {
                // The same series bound by two groups: sample at the faster rate.
                if case .object(var prev) = schedules[i], !isHidden {
                    for k in ["every", "idle_every", "battery_every"] where seconds(s[k]?.string, default: 1e9) < seconds(prev[k]?.string, default: 1e9) {
                        prev[k] = s[k]
                    }
                    schedules[i] = .object(prev)
                }
                return
            }
            seen[id] = schedules.count
            schedules.append(.object(s))
        }

        for g in c.orderedGroups {
            let h = hidden.contains(g.id)
            add(name: nil, recipe: g.headline.recipe, params: g.headline.params, kind: \.headline, isHidden: h)
            if g.sparkline.recipe != g.headline.recipe {
                add(name: nil, recipe: g.sparkline.recipe, params: nil, kind: \.headline, isHidden: h)
            }
            var extra: [String: JSONValue] = ["keep_table": true, "columns": [.string(g.consumers.value)]]
            if let id = g.consumers.id { extra["key"] = .string(id) }
            add(name: "consumers.\(g.id)", recipe: g.consumers.recipe, params: g.consumers.params, kind: \.consumers, isHidden: h, extra: extra)
        }
        var doc: [String: JSONValue] = ["version": 1, "schedules": .array(schedules)]
        if let n = c.sampling.narrative { doc["narrative"] = .array(n) }
        if let e = c.sampling.events { doc["events"] = .array(e) }
        return .object(doc)
    }

    public static func yaml(_ c: AppConfig) -> String {
        let header = "# Written by Headroom from groups.yaml and sampling.yaml (edit those; this file is rewritten).\n"
        return header + emit(document(c), order: ["version", "schedules", "narrative", "events", "name", "recipe", "params", "every", "idle_every", "battery_every", "keep_table", "columns", "key"])
    }

    /// Minimal YAML emitter with stable key order (Yams serializes the node).
    static func emit(_ v: JSONValue, order: [String]) -> String {
        func node(_ v: JSONValue, flow: Bool) -> Node {
            switch v {
            case .null: return Node("null", Tag(.null))
            case .bool(let b): return Node(b ? "true" : "false", Tag(.bool))
            case .number(let n): return Node(Units.number(n).replacingOccurrences(of: ",", with: ""), Tag(n.rounded() == n ? .int : .float))
            case .string(let s): return Node(s, Tag(.str), .plain)
            case .array(let a): return Node(a.map { node($0, flow: true) }, Tag(.seq), flow ? .flow : .block)
            case .object(let o):
                let keys = o.keys.sorted { a, b in
                    let ia = order.firstIndex(of: a) ?? Int.max, ib = order.firstIndex(of: b) ?? Int.max
                    return ia == ib ? a < b : ia < ib
                }
                return Node(keys.map { (Node($0), node(o[$0]!, flow: true)) }, Tag(.map), flow ? .flow : .block)
            }
        }
        // Top level and list items in block style; entries inline.
        guard case .object(let top) = v else { return (try? Yams.serialize(node: node(v, flow: false))) ?? "" }
        var out = ""
        for k in top.keys.sorted(by: { (order.firstIndex(of: $0) ?? 99) < (order.firstIndex(of: $1) ?? 99) }) {
            if case .array(let items) = top[k]! {
                out += "\(k):\n"
                for it in items {
                    let s = (try? Yams.serialize(node: node(it, flow: true))) ?? "{}\n"
                    out += "  - " + s.trimmingCharacters(in: .whitespacesAndNewlines) + "\n"
                }
            } else {
                out += (try? Yams.serialize(node: Node([(Node(k), node(top[k]!, flow: true))], Tag(.map), .block))) ?? ""
            }
        }
        return out
    }
}

/// Connection state of one service, for the status banner.
public enum ServiceState: Sendable, Equatable {
    case starting
    case running(String)
    case failed(String)

    public var isRunning: Bool { if case .running = self { return true }; return false }
    public var message: String? { if case .failed(let m) = self { return m }; return nil }
}

/// Everything below the stores: the clients and how to reach sysqd/sysact.
@MainActor
@Observable
public final class HeadroomServices {
    public private(set) var sysq: ServiceState = .starting
    public private(set) var sysact: ServiceState = .starting
    public private(set) var apiPort: Int = 11435
    /// The UI's sysqd client (privacy "files": everything except secret).
    public private(set) var ui: SysqClientProtocol?
    /// The assistant's sysqd client: assistant.yaml's privacy, scripts on.
    public private(set) var assistant: SysqClientProtocol?
    public let admin: SysqAdminProtocol
    public let actions: SysactClientProtocol
    public private(set) var chat: ChatBackend
    /// Bumped whenever the clients change (stores resubscribe).
    public private(set) var generation = 0

    @ObservationIgnored let paths: Paths
    @ObservationIgnored let launcher: ServiceLauncher?
    @ObservationIgnored let binDir: URL?
    @ObservationIgnored let makeSysq: @Sendable (URL, String, String) -> SysqClientProtocol
    @ObservationIgnored private var lastAPI: APISettings?
    @ObservationIgnored private var lastSchedules: String?
    @ObservationIgnored private var lastAssistantPrivacy: String?
    @ObservationIgnored private var connecting = false

    public init(paths: Paths, binDir: URL?, launcher: ServiceLauncher?, admin: SysqAdminProtocol? = nil,
                actions: SysactClientProtocol? = nil, chat: ChatBackend? = nil,
                makeSysq: (@Sendable (URL, String, String) -> SysqClientProtocol)? = nil) {
        self.paths = paths
        self.binDir = binDir
        self.launcher = launcher
        self.admin = admin ?? SysqAdminClient(socketPath: paths.sysqAdminSocket)
        self.actions = actions ?? SysactClient(socketPath: paths.sysactSocket, signer: HostKeySigner(keyPath: paths.sysactHostKey))
        self.chat = chat ?? OllamaClient(baseURL: URL(string: "http://127.0.0.1:11434")!)
        self.makeSysq = makeSysq ?? { url, token, name in SysqClient(baseURL: url, token: token, clientName: name) }
    }

    /// Starts the services if needed and registers Headroom's clients.
    public func connect(_ config: AppConfig) async {
        if connecting { return }
        connecting = true
        defer { connecting = false }
        if let url = URL(string: config.assistant.ollama_url ?? ""), (chat as? OllamaClient)?.baseURL != url, chat is OllamaClient {
            chat = OllamaClient(baseURL: url)
        }
        await startServices()
        await registerClients(config)
        await checkSysact()
    }

    func startServices() async {
        guard let launcher, let binDir else { return }
        let fm = FileManager.default
        let logs = paths.logs.path
        let specs = [
            ServiceSpec(label: "com.ainigh.headroom.sysqd", executable: binDir.appendingPathComponent("sysqd").path, arguments: [], logPath: logs + "/sysqd.log"),
            ServiceSpec(label: "com.ainigh.headroom.sysact", executable: binDir.appendingPathComponent("sysact").path, arguments: ["serve"], logPath: logs + "/sysact.log"),
        ]
        for s in specs where fm.isExecutableFile(atPath: s.executable) {
            do { try await launcher.ensure(s) } catch {
                if s.label.hasSuffix("sysqd") { sysq = .failed(error.localizedDescription) } else { sysact = .failed(error.localizedDescription) }
            }
        }
    }

    /// Waits for the admin socket, then (re)creates the two host clients.
    /// Tokens are not stored: every launch mints fresh ones.
    func registerClients(_ config: AppConfig) async {
        var status: JSONValue?
        for attempt in 0..<40 {
            if let s = try? await admin.status() { status = s; break }
            if attempt == 0 { sysq = .starting }
            try? await Task.sleep(nanoseconds: 250_000_000)
        }
        guard let status else {
            sysq = .failed("sysqd isn't answering on \(paths.sysqAdminSocket)")
            return
        }
        do {
            try await applyAPI(config, status: status)
            let port = status["api"]?["port"]?.double.map { Int($0) } ?? config.api.port ?? 11435
            apiPort = port
            let base = URL(string: "http://127.0.0.1:\(port)")!
            let uiToken = try await admin.upsert(HostClient(id: "headroom-ui", name: "Headroom", privacy: "files", allow_scripts: false))
            let privacy = config.assistant.privacy
            let aToken = try await admin.upsert(HostClient(id: "headroom-assistant", name: "Headroom assistant", privacy: privacy, allow_scripts: true))
            lastAssistantPrivacy = privacy
            ui = makeSysq(base, uiToken, "Headroom")
            assistant = makeSysq(base, aToken, "Headroom assistant")
            generation += 1
            try? await writeSchedules(config)
            sysq = .running(status["version"]?.string ?? "")
        } catch {
            sysq = .failed(error.localizedDescription)
        }
    }

    func checkSysact() async {
        for _ in 0..<20 {
            if (try? await actions.status()) != nil {
                sysact = .running("")
                return
            }
            try? await Task.sleep(nanoseconds: 250_000_000)
        }
        sysact = .failed("sysact isn't answering on \(paths.sysactSocket)")
    }

    func applyAPI(_ config: AppConfig, status: JSONValue? = nil) async throws {
        let want = APISettings(enabled: config.api.enabled ?? false, port: config.api.port ?? 11435, exposed: config.api.exposed_recipes ?? [])
        if want == lastAPI { return }
        try await admin.setAPI(want)
        lastAPI = want
    }

    func writeSchedules(_ config: AppConfig) async throws {
        let text = ScheduleWriter.yaml(config)
        if text == lastSchedules { return }
        try await admin.putSchedules(text)
        lastSchedules = text
    }

    /// Config changed: write through what depends on it.
    public func configChanged(_ config: AppConfig) async {
        guard sysq.isRunning else { await connect(config); return }
        try? await applyAPI(config)
        try? await writeSchedules(config)
        if config.assistant.privacy != lastAssistantPrivacy {
            await registerClients(config)
        }
        if let url = URL(string: config.assistant.ollama_url ?? ""), let o = chat as? OllamaClient, o.baseURL != url {
            chat = OllamaClient(baseURL: url)
        }
    }

    /// A call came back unauthorized (sysqd restarted with a fresh table, or
    /// a client was revoked): register again.
    public func reauthorize(_ config: AppConfig) async {
        await registerClients(config)
    }

    /// Tells sysqd whether a view is open and whether we're on battery
    /// (its sampler slows down otherwise).
    public func reportState(viewOpen: Bool, onBattery: Bool?) async {
        try? await admin.setState(viewOpen: viewOpen, onBattery: onBattery)
    }
}
