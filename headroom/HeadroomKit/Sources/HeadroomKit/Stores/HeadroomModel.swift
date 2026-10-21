import Foundation
import Observation

/// What the small view, large view and menu bar render.
public struct PanelViewModel: Sendable {
    public var keyGroups: [GroupViewModel]
    public var allGroups: [GroupViewModel]
    /// Non-key groups that are amber or red (the alert strip).
    public var alerts: [GroupViewModel]
    public var mode: ModeViewModel?
    public var modes: [ModeViewModel]
    public var banners: [Banner]
    public var readout: (text: String, status: GroupStatus)?
    public var worst: GroupStatus
    public var connected: Bool
    public var paused: Bool
    public var onBattery: Bool
}

public struct Banner: Sendable, Hashable, Identifiable {
    public enum Kind: String, Sendable { case config, service, recipe }
    public var kind: Kind
    public var text: String
    public var id: String { kind.rawValue + text }
}

/// Composition root for L3–L5: the app creates one and its views observe
/// it (and the stores it holds). Nothing here names a group or recipe.
@MainActor
@Observable
public final class HeadroomModel {
    public let configStore: ConfigStore
    public let services: HeadroomServices
    public let groups: GroupStore
    public let actions: ActionStore
    public let recipes: RecipeStore
    public let history: HistoryStore
    /// The drill-in's history, apart from the Graphs window's (each has its own range).
    public let drillHistory: HistoryStore
    public let pairing: PairingStore
    public let setup: SetupStore
    public let explorer: ExplorerStore
    public let assistant: AssistantStore
    @ObservationIgnored public let notifications: NotificationStore

    public private(set) var mode: String
    public private(set) var keyIDs: [String] = []
    @ObservationIgnored var selector = KeyGroupSelector()
    @ObservationIgnored var ticker: Task<Void, Never>?
    @ObservationIgnored var reauthorizing = false
    @ObservationIgnored let consumerRowsSmall = 3

    public init(configStore: ConfigStore, services: HeadroomServices, notifier: Notifier?, summaries: SummaryHistory, log: AssistantLog) {
        self.configStore = configStore
        self.services = services
        let c = configStore.config
        groups = GroupStore(config: c)
        let flow = ConfirmationFlow(delay: c.assistant.confirmDelay)
        actions = ActionStore(client: services.actions, flow: flow)
        recipes = RecipeStore()
        history = HistoryStore()
        drillHistory = HistoryStore()
        pairing = PairingStore(admin: services.admin)
        setup = SetupStore(admin: services.admin)
        explorer = ExplorerStore()
        notifications = NotificationStore(notifier: notifier)
        assistant = AssistantStore(summaries: summaries, log: log)
        mode = c.modeOrder.first ?? "view"
        groups.setMode(mode)
        configStore.onChange = { [weak self] c in self?.configChanged(c) }
        groups.onUnauthorized = { [weak self] in self?.reauthorize() }
        assistant.onStatus = { [weak self] status, line in
            guard let self else { return }
            self.notifications.assistant(status: status.rawValue, line: line, config: self.config.notifications)
        }
        keyIDs = selector.select(c, statuses: [:])
    }

    public var config: AppConfig { configStore.config }

    /// Connects to sysqd/sysact and starts sampling and watching config.
    public func start() {
        configStore.startWatching()
        Task {
            await services.connect(config)
            attachClients()
            await actions.loadCatalog()
            await setup.refresh()
        }
        ticker?.cancel()
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                self?.tick()
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    func attachClients() {
        groups.attach(services.ui)
        recipes.attach(services.ui)
        history.attach(services.ui)
        drillHistory.attach(services.ui)
        explorer.attach(services.ui, config: config.groups.explorer)
        assistant.attach(sysq: services.assistant, chat: services.chat, actions: ActionBridge(actions), config: config)
    }

    func reauthorize() {
        if reauthorizing { return }
        reauthorizing = true
        Task {
            await services.reauthorize(config)
            attachClients()
            reauthorizing = false
        }
    }

    func configChanged(_ c: AppConfig) {
        groups.update(config: c)
        explorer.attach(services.ui, config: c.groups.explorer)
        actions.flow.delay = c.assistant.confirmDelay
        let gen = services.generation
        Task {
            await services.configChanged(c)
            if services.generation != gen { attachClients() } else { assistant.update(config: c) }
        }
        if c.modes.modes[mode] == nil { setMode(c.modeOrder.first ?? "view") }
        keyIDs = selector.select(c, statuses: statuses())
    }

    func statuses() -> [String: GroupStatus] {
        Dictionary(uniqueKeysWithValues: config.groups.groups.map { ($0.id, groups.state($0.id)?.status ?? .unknown) })
    }

    /// Periodic work: auto key groups and notifications.
    func tick() {
        let next = selector.select(config, statuses: statuses())
        if next != keyIDs { keyIDs = next }
        notifications.observe(allGroupViewModels(limit: 1), config: config.notifications)
    }

    // MARK: user intents

    public func setMode(_ m: String) {
        guard config.modes.modes[m] != nil else { return }
        mode = m
        groups.setMode(m)
    }

    public func setViewOpen(_ open: Bool) {
        groups.setViewOpen(open)
        Task { await services.reportState(viewOpen: open, onBattery: groups.onBattery) }
    }

    public func setOnBattery(_ b: Bool) {
        groups.setOnBattery(b)
        Task { await services.reportState(viewOpen: groups.viewOpen, onBattery: b) }
    }

    public func setPaused(_ p: Bool) { groups.setPaused(p) }

    public func retryConnection() {
        Task {
            await services.connect(config)
            attachClients()
            await actions.loadCatalog()
            await setup.refresh()
        }
    }

    // MARK: view models

    func allGroupViewModels(limit: Int) -> [GroupViewModel] {
        let hidden = Set(config.layout.hidden_groups ?? [])
        return config.orderedGroups.filter { !hidden.contains($0.id) }.map {
            ViewModels.group($0, state: groups.state($0.id), config: config, mode: mode, isKey: keyIDs.contains($0.id), consumerLimit: limit)
        }
    }

    public func groupViewModel(_ id: String, consumerLimit: Int = 100) -> GroupViewModel? {
        guard let g = config.group(id) else { return nil }
        return ViewModels.group(g, state: groups.state(id), config: config, mode: mode, isKey: keyIDs.contains(id), consumerLimit: consumerLimit)
    }

    public var modeViewModel: ModeViewModel? { ViewModels.modes(config).first { $0.id == mode } }

    public func panel(consumerRows: Int? = nil) -> PanelViewModel {
        let modeVM = modeViewModel
        let rows = consumerRows ?? modeVM?.consumerCount.map { min($0, 10) } ?? consumerRowsSmall
        let all = ViewModels.ordered(allGroupViewModels(limit: rows), mode: modeVM)
        let byID = Dictionary(uniqueKeysWithValues: all.map { ($0.id, $0) })
        let key = keyIDs.compactMap { byID[$0] }
        var banners = configStore.problems.map { Banner(kind: .config, text: $0.text) }
        if let m = services.sysq.message { banners.append(Banner(kind: .service, text: "sysqd: \(m)")) }
        if let m = services.sysact.message { banners.append(Banner(kind: .service, text: "sysact: \(m) — actions are unavailable")) }
        let missing = recipes.missingRecipes(config)
        if !missing.isEmpty { banners.append(Banner(kind: .recipe, text: "Recipes not found in sysq: \(missing.joined(separator: ", "))")) }
        return PanelViewModel(
            keyGroups: key,
            allGroups: all,
            alerts: all.filter { !keyIDs.contains($0.id) && $0.status.isAlert },
            mode: modeVM,
            modes: ViewModels.modes(config),
            banners: banners,
            readout: ViewModels.readout(config, vms: all),
            worst: all.map(\.status).max() ?? .unknown,
            connected: services.sysq.isRunning,
            paused: groups.paused,
            onBattery: groups.onBattery)
    }

    /// Key groups chosen by hand (large view star / reorder) are written back
    /// to layout.yaml so the file stays the source of truth.
    public func setPinned(_ ids: [String]) -> ConfigProblem? {
        setLayout { $0.pinnedText(ids) }
    }

    public func setGroupOrder(_ ids: [String]) -> ConfigProblem? {
        setLayout { $0.orderText(ids) }
    }

    /// Sets one top-level key in a config file (keeps comments), validated.
    public func setTopLevel(_ file: String, key: String, value: String) -> ConfigProblem? {
        let text = configStore.loader.text(of: file)
        return configStore.save(file, text: LayoutEditor(text: text).replacing(key, indent: "", with: value))
    }

    /// Sets `parent.key` (block style) in a config file, validated.
    public func setNested(_ file: String, parent: String, key: String, value: String) -> ConfigProblem? {
        let text = configStore.loader.text(of: file)
        return configStore.save(file, text: LayoutEditor(text: text).replacing(key, indent: "  ", with: value, under: parent))
    }

    public func setAlwaysOnTop(_ on: Bool) -> ConfigProblem? {
        setLayout { $0.flagText("always_on_top", on, under: "panel") }
    }

    func setLayout(_ edit: (LayoutEditor) -> String?) -> ConfigProblem? {
        let text = configStore.loader.text(of: "layout")
        guard let next = edit(LayoutEditor(text: text)) else { return nil }
        return configStore.save("layout", text: next)
    }
}

/// Small line-level edits to layout.yaml that keep the user's comments.
public struct LayoutEditor: Sendable {
    public var text: String

    func list(_ ids: [String]) -> String { "[" + ids.joined(separator: ", ") + "]" }

    func replacing(_ key: String, indent: String, with value: String, under parent: String? = nil) -> String {
        var lines = text.components(separatedBy: "\n")
        var inParent = parent == nil
        for (i, l) in lines.enumerated() {
            if let parent, l.hasPrefix(parent + ":") { inParent = true; continue }
            if parent != nil, inParent, !l.hasPrefix(" "), !l.isEmpty, !l.hasPrefix("#") { inParent = false }
            if inParent, l.hasPrefix(indent + key + ":") {
                let comment = l.range(of: " #").map { String(l[$0.lowerBound...]) } ?? ""
                let old = l.dropFirst(indent.count + key.count + 1).prefix { $0 != "#" }.trimmingCharacters(in: .whitespaces)
                lines[i] = indent + key + ": " + value + comment
                // A block-style list under the key ("pinned:\n  - memory") goes
                // with the old value, or the result isn't valid YAML.
                if old.isEmpty {
                    var j = i + 1
                    while j < lines.count {
                        let t = lines[j].trimmingCharacters(in: .whitespaces)
                        let lead = lines[j].prefix { $0 == " " }.count
                        if (t.hasPrefix("-") && lead >= indent.count) || (t.hasPrefix("#") && lead > indent.count) { lines.remove(at: j) } else { break }
                    }
                }
                return lines.joined(separator: "\n")
            }
        }
        if let parent {
            if let i = lines.firstIndex(where: { $0.hasPrefix(parent + ":") }) {
                lines.insert(indent + key + ": " + value, at: i + 1)
            } else {
                lines.append(parent + ":")
                lines.append(indent + key + ": " + value)
            }
        } else {
            lines.append(key + ": " + value)
        }
        return lines.joined(separator: "\n")
    }

    public func pinnedText(_ ids: [String]) -> String? {
        replacing("pinned", indent: "  ", with: list(Array(ids.prefix(KeyGroupSelector.slots))), under: "key_groups")
    }

    public func orderText(_ ids: [String]) -> String? { replacing("group_order", indent: "", with: list(ids)) }

    /// Sets a boolean wherever the key is written (block or flow style),
    /// else adds it under `parent`.
    public func flagText(_ key: String, _ on: Bool, under parent: String) -> String? {
        let pattern = "(\\b\(key):\\s*)(true|false)"
        if let r = text.range(of: pattern, options: .regularExpression) {
            let old = String(text[r])
            let new = old.replacingOccurrences(of: "(true|false)$", with: on ? "true" : "false", options: .regularExpression)
            return text.replacingCharacters(in: r, with: new)
        }
        return replacing(key, indent: "  ", with: on ? "true" : "false", under: parent)
    }
}
