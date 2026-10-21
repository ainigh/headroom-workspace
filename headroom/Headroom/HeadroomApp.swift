import AppKit
import HeadroomKit
import SwiftUI

/// A menu bar app with no Dock icon. The status item's left click drops the
/// panel down from the icon; right click (or ⌃-click) shows the menu: mode,
/// windows, pause, settings, quit. ⌃⌥H toggles the panel from anywhere.
@main
@MainActor
enum Main {
    static let delegate = AppDelegate()

    static func main() {
        let app = NSApplication.shared
        app.delegate = delegate
        app.setActivationPolicy(.accessory)
        app.run()
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private(set) var model: HeadroomModel?
    private(set) var windows: WindowManager?
    private var item: NSStatusItem!
    private let power = PowerMonitor()
    private var hotKey: HotKey?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.mainMenu = MainMenu.make(target: self)
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        item.button?.target = self
        item.button?.action = #selector(statusItemClicked)
        item.button?.sendAction(on: [.leftMouseUp, .rightMouseUp])
        item.button?.imagePosition = .imageLeading
        setIcon(symbol: "gauge.with.dots.needle.33percent", readout: nil, status: .unknown, tip: "Headroom: starting…")

        let paths = Paths()
        let resources = Bundle.main.resourceURL ?? URL(fileURLWithPath: ".")
        // A dev build (swift run) has no bundle resources: use the repo's config.
        var defaults = resources.appendingPathComponent("config")
        if !FileManager.default.fileExists(atPath: defaults.path) {
            defaults = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("config")
        }
        let bin = resources.appendingPathComponent("bin")
        let config = ConfigStore(loader: ConfigLoader(userDir: paths.config, defaultsDir: defaults))
        let services = HeadroomServices(paths: paths, binDir: FileManager.default.fileExists(atPath: bin.path) ? bin : nil,
                                        launcher: LaunchAgentLauncher())
        let (summaries, log) = AssistantPersistence.open(dir: paths.headroom)
        let model = HeadroomModel(configStore: config, services: services, notifier: SystemNotifier(), summaries: summaries, log: log)
        self.model = model
        let windows = WindowManager(model: model, statusItem: item)
        self.windows = windows
        // The icon and readout follow the model.
        trackChanges { [weak self] in self?.refreshIcon() }
        power.onChange = { onBattery in model.setOnBattery(onBattery) }
        power.start()
        hotKey = HotKey { [weak windows] in windows?.togglePanel() }
        model.start()
        Updater.shared.start()
        windows.permissions.refresh()
        // After sleep the update streams can sit dead until they time out: reconnect them.
        NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { model.groups.refreshAll() }
        }
        if config.config.layout.panel?.remember_position != false,
           config.config.layout.panel?.always_on_top == true,
           UserDefaults.standard.bool(forKey: "panelWasOpen") {
            // A pinned panel that was open when Headroom quit comes back.
            DispatchQueue.main.async { windows.showPanel() }
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        UserDefaults.standard.set(windows?.panelVisible ?? false, forKey: "panelWasOpen")
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        windows?.showPanel()
        return false
    }

    // MARK: status item

    @objc private func statusItemClicked() {
        let event = NSApp.currentEvent
        if event?.type == .rightMouseUp || event?.modifierFlags.contains(.control) == true {
            showMenu()
        } else {
            windows?.togglePanel()
        }
    }

    private func showMenu() {
        windows?.hidePanel()
        let menu = buildMenu()
        menu.delegate = self
        item.menu = menu
        item.button?.performClick(nil)       // opens the menu under the icon
    }

    func menuDidClose(_ menu: NSMenu) {
        // Detach it again, so the next left click toggles the panel.
        DispatchQueue.main.async { [weak self] in self?.item.menu = nil }
    }

    private func buildMenu() -> NSMenu {
        let menu = NSMenu()
        guard let model, let windows else {
            menu.addItem(withTitle: "Starting…", action: nil, keyEquivalent: "")
            menu.addItem(.separator())
            menu.addItem(withTitle: "Quit Headroom", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
            return menu
        }
        let panel = MenuAction.item(windows.panelVisible ? "Hide Panel" : "Show Panel", key: "h") { windows.togglePanel() }
        panel.keyEquivalentModifierMask = [.control, .option]
        menu.addItem(panel)
        menu.addItem(.separator())
        let modes = NSMenuItem(title: "Mode", action: nil, keyEquivalent: "")
        let sub = NSMenu()
        for m in ViewModels.modes(model.config) {
            let i = MenuAction.item(m.label) { model.setMode(m.id) }
            i.state = model.mode == m.id ? .on : .off
            sub.addItem(i)
        }
        modes.submenu = sub
        menu.addItem(modes)
        menu.addItem(.separator())
        menu.addItem(MenuAction.item("Overview") { windows.showLarge() })
        menu.addItem(MenuAction.item("Graphs") { windows.showGraphs() })
        if model.explorer.available { menu.addItem(MenuAction.item("Storage Explorer") { windows.showExplorer() }) }
        let ask = MenuAction.item("Ask AI") { windows.showAssistant(scope: .keyGroups) }
        ask.isEnabled = model.assistant.available
        menu.addItem(ask)
        menu.addItem(MenuAction.item("Summary History") { windows.showSummaries() })
        menu.addItem(.separator())
        menu.addItem(MenuAction.item(model.groups.paused ? "Resume Sampling" : "Pause Sampling") { model.setPaused(!model.groups.paused) })
        menu.addItem(MenuAction.item(updateTitle) { Self.updateAction() })
        menu.addItem(MenuAction.item("Settings…", key: ",") { windows.showSettings() })
        menu.addItem(.separator())
        menu.addItem(withTitle: "Quit Headroom", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        return menu
    }

    private var updateTitle: String {
        switch Updater.shared.state {
        case .available(let u): return u.release.map { "Update to \($0.tag)" } ?? "Update (build here)"
        case .installing(let step): return step
        case .checking: return "Checking for Updates…"
        default: return "Check for Updates (v\(Updater.version))"
        }
    }

    private static func updateAction() {
        if case .available = Updater.shared.state { Updater.shared.install() } else { Updater.shared.check(userInitiated: true) }
    }

    /// The icon says how the Mac is doing; the readout (layout.yaml
    /// menu_bar_readout) sits next to it and turns amber/red with its status.
    private func refreshIcon() {
        guard let model else { return }
        let p = model.panel()
        let symbol: String
        if !p.connected {
            symbol = "gauge.with.dots.needle.bottom.0percent"
        } else if p.paused {
            symbol = "pause.circle"
        } else {
            switch p.worst {
            case .red: symbol = "exclamationmark.triangle.fill"
            case .amber: symbol = "gauge.with.dots.needle.67percent"
            default: symbol = "gauge.with.dots.needle.33percent"
            }
        }
        var tip = "Headroom"
        if !p.connected { tip += ": sysqd isn't running" } else if p.paused { tip += ": sampling paused" }
        let alerts = p.allGroups.filter { $0.status.isAlert }
        if !alerts.isEmpty { tip += "\n" + alerts.map { "\($0.name): \($0.status.label)" }.joined(separator: "\n") }
        tip += "\nClick for the panel, right-click for the menu (⌃⌥H)"
        setIcon(symbol: symbol, readout: p.readout?.text, status: p.readout?.status ?? .unknown, tip: tip)
    }

    private func setIcon(symbol: String, readout: String?, status: GroupStatus, tip: String) {
        guard let button = item.button else { return }
        let image = NSImage(systemSymbolName: symbol, accessibilityDescription: "Headroom")
        image?.isTemplate = true
        button.image = image
        if let readout, !readout.isEmpty {
            var attrs: [NSAttributedString.Key: Any] = [.font: NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .regular)]
            switch status {
            case .red: attrs[.foregroundColor] = NSColor.systemRed
            case .amber: attrs[.foregroundColor] = NSColor.systemOrange
            default: break
            }
            button.attributedTitle = NSAttributedString(string: " " + readout, attributes: attrs)
        } else {
            button.title = ""
        }
        button.toolTip = tip
    }

    // MARK: main menu actions (key equivalents while a window is in front)

    @objc func showSettingsFromMenu(_ sender: Any?) { windows?.showSettings() }
    @objc func showAboutFromMenu(_ sender: Any?) {
        NSApp.activate(ignoringOtherApps: true)
        NSApp.orderFrontStandardAboutPanel(options: [.applicationVersion: Updater.version])
    }
}

/// Menu items that run a closure.
@MainActor
final class MenuAction: NSObject {
    private let run: () -> Void
    private static var live: [MenuAction] = []

    private init(_ run: @escaping () -> Void) { self.run = run }

    static func item(_ title: String, key: String = "", _ run: @escaping () -> Void) -> NSMenuItem {
        let target = MenuAction(run)
        // Menus are rebuilt each time they open; keep only the latest targets.
        if live.count > 64 { live.removeFirst(live.count - 64) }
        live.append(target)
        let i = NSMenuItem(title: title, action: #selector(MenuAction.fire), keyEquivalent: key)
        i.target = target
        return i
    }

    @objc private func fire() { run() }
}

/// The main menu isn't shown (no Dock icon), but it gives the windows their
/// key equivalents: ⌘C/⌘V/⌘A in text fields, ⌘W, ⌘M, ⌘, and ⌘Q.
@MainActor
enum MainMenu {
    static func make(target: AppDelegate) -> NSMenu {
        let main = NSMenu()

        let appItem = NSMenuItem()
        let app = NSMenu(title: "Headroom")
        let about = NSMenuItem(title: "About Headroom", action: #selector(AppDelegate.showAboutFromMenu(_:)), keyEquivalent: "")
        about.target = target
        app.addItem(about)
        app.addItem(.separator())
        let settings = NSMenuItem(title: "Settings…", action: #selector(AppDelegate.showSettingsFromMenu(_:)), keyEquivalent: ",")
        settings.target = target
        app.addItem(settings)
        app.addItem(.separator())
        app.addItem(withTitle: "Hide Headroom", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        app.addItem(.separator())
        app.addItem(withTitle: "Quit Headroom", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = app
        main.addItem(appItem)

        let editItem = NSMenuItem()
        let edit = NSMenu(title: "Edit")
        edit.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        let redo = edit.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "z")
        redo.keyEquivalentModifierMask = [.command, .shift]
        edit.addItem(.separator())
        edit.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        edit.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        edit.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        edit.addItem(.separator())
        edit.addItem(withTitle: "Find…", action: #selector(NSTextView.performFindPanelAction(_:)), keyEquivalent: "f").tag = Int(NSFindPanelAction.showFindPanel.rawValue)
        editItem.submenu = edit
        main.addItem(editItem)

        let windowItem = NSMenuItem()
        let window = NSMenu(title: "Window")
        window.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        window.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowItem.submenu = window
        main.addItem(windowItem)
        NSApp.windowsMenu = window
        return main
    }
}

/// Re-runs `body` whenever an observed value it read changes. (Not named
/// `observe`: NSObject subclasses would resolve that to KVO.)
@MainActor
func trackChanges(_ body: @escaping @MainActor @Sendable () -> Void) {
    withObservationTracking {
        body()
    } onChange: {
        Task { @MainActor in trackChanges(body) }
    }
}
