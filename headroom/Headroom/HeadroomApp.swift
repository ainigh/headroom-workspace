import AppKit
import HeadroomKit
import SwiftUI

/// The menu bar item is a MenuBarExtra (implementation.md §2); its first
/// item toggles the floating panel. Everything else lives in windows the
/// AppDelegate manages, so the app has no Dock icon (LSUIElement).
@main
struct HeadroomApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate

    var body: some Scene {
        MenuBarExtra {
            MenuContent(app: delegate)
        } label: {
            MenuBarLabel(app: delegate)
        }
        .menuBarExtraStyle(.menu)
    }
}

/// Icon plus the optional readout (layout.yaml menu_bar_readout).
struct MenuBarLabel: View {
    @ObservedObject var app: AppDelegate

    var body: some View {
        if let model = app.model, let r = model.panel().readout {
            HStack(spacing: 3) {
                Image(systemName: symbol(r.status))
                Text(r.text).monospacedDigit()
            }
        } else {
            Image(systemName: "gauge.with.dots.needle.33percent")
        }
    }

    func symbol(_ s: GroupStatus) -> String {
        switch s {
        case .red: return "exclamationmark.triangle.fill"
        case .amber: return "gauge.with.dots.needle.67percent"
        default: return "gauge.with.dots.needle.33percent"
        }
    }
}

struct MenuContent: View {
    @ObservedObject var app: AppDelegate

    var body: some View {
        if let model = app.model {
            Button(app.windows?.panelVisible == true ? "Hide Headroom" : "Show Headroom") { app.windows?.togglePanel() }
                .keyboardShortcut("h", modifiers: [.command, .option])
            Divider()
            Picker("Mode", selection: Binding(get: { model.mode }, set: { model.setMode($0) })) {
                ForEach(model.panel().modes) { m in Text(m.label).tag(m.id) }
            }
            Divider()
            Button("Open Large View") { app.windows?.showLarge() }
            Button("Graphs") { app.windows?.showGraphs() }
            Button("Storage Explorer") { app.windows?.showExplorer() }
            Button("Ask AI") { app.windows?.showAssistant(scope: .keyGroups) }
            Button("Summary History") { app.windows?.showSummaries() }
            Divider()
            Button(model.groups.paused ? "Resume Sampling" : "Pause Sampling") { model.setPaused(!model.groups.paused) }
            Button("Settings…") { app.windows?.showSettings() }
                .keyboardShortcut(",")
            Divider()
            Button("Quit Headroom") { NSApp.terminate(nil) }
                .keyboardShortcut("q")
        } else {
            Text("Starting…")
            Button("Quit Headroom") { NSApp.terminate(nil) }
        }
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, ObservableObject {
    @Published private(set) var model: HeadroomModel?
    private(set) var windows: WindowManager?
    private let power = PowerMonitor()
    private var hotKey: HotKey?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
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
        let windows = WindowManager(model: model)
        self.windows = windows
        // Re-render the menu bar label when the model changes.
        trackChanges { [weak self] in
            _ = model.panel().readout?.text
            _ = model.mode
            self?.objectWillChange.send()
        }
        power.onChange = { onBattery in model.setOnBattery(onBattery) }
        power.start()
        hotKey = HotKey { [weak windows] in windows?.togglePanel() }
        model.start()
        Updater.shared.start()
        if config.config.layout.panel?.remember_position != false, UserDefaults.standard.bool(forKey: "panelWasOpen") {
            windows.togglePanel()
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        UserDefaults.standard.set(windows?.panelVisible ?? false, forKey: "panelWasOpen")
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        windows?.togglePanel()
        return false
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
