import AppKit
import HeadroomKit
import Observation
import SwiftUI

/// View-only navigation state shared by the windows (which group is drilled
/// into, which item the Actions panel shows). No data lives here.
@MainActor
@Observable
final class UIState {
    var drillIn: String?
    var inspecting: ItemRef?
    var assistantScope: AssistantScope = .keyGroups
    var graphsSelection: Set<String> = []
}

/// Owns the floating panel and the app's windows; reports to the model
/// whether any view is open (sampling speeds up only then).
@MainActor
final class WindowManager: NSObject, NSWindowDelegate {
    let model: HeadroomModel
    let ui = UIState()
    private var panel: NSPanel?
    private var windows: [String: NSWindow] = [:]
    private var confirmWindow: NSPanel?

    init(model: HeadroomModel) {
        self.model = model
        super.init()
        trackChanges { [weak self] in
            guard let self else { return }
            let current = self.model.actions.flow.current
            self.syncConfirmation(current)
        }
        trackChanges { [weak self] in
            guard let self else { return }
            let onTop = self.model.config.layout.panel?.always_on_top ?? false
            self.panel?.level = onTop ? .floating : .normal
        }
    }

    var panelVisible: Bool { panel?.isVisible ?? false }

    // MARK: floating panel

    func togglePanel() {
        if let p = panel, p.isVisible {
            p.orderOut(nil)
            updateViewOpen()
            return
        }
        let p = panel ?? makePanel()
        panel = p
        if !restorePosition(p) { positionNearMenuBar(p) }
        p.orderFrontRegardless()
        p.makeKey()
        updateViewOpen()
    }

    private func makePanel() -> NSPanel {
        let p = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 380, height: 560),
                        styleMask: [.titled, .closable, .fullSizeContentView, .nonactivatingPanel, .resizable],
                        backing: .buffered, defer: false)
        p.titleVisibility = .hidden
        p.titlebarAppearsTransparent = true
        p.isMovableByWindowBackground = true
        p.hidesOnDeactivate = false
        p.isReleasedWhenClosed = false
        p.level = (model.config.layout.panel?.always_on_top ?? false) ? .floating : .normal
        p.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        p.minSize = NSSize(width: 380, height: 300)
        p.maxSize = NSSize(width: 380, height: 1400)
        p.delegate = self
        p.contentView = NSHostingView(rootView: SmallPanelView(model: model, ui: ui, windows: self))
        return p
    }

    private func positionNearMenuBar(_ p: NSPanel) {
        guard let screen = NSScreen.main else { return }
        let f = screen.visibleFrame
        p.setFrameTopLeftPoint(NSPoint(x: f.maxX - p.frame.width - 12, y: f.maxY - 8))
    }

    private func restorePosition(_ p: NSPanel) -> Bool {
        guard model.config.layout.panel?.remember_position != false,
              let s = UserDefaults.standard.string(forKey: "panelFrame") else { return false }
        let r = NSRectFromString(s)
        guard r.width > 0, NSScreen.screens.contains(where: { $0.visibleFrame.intersects(r) }) else { return false }
        p.setFrame(r, display: false)
        return true
    }

    func setAlwaysOnTop(_ on: Bool) {
        panel?.level = on ? .floating : .normal
    }

    // MARK: windows

    private func show<V: View>(_ id: String, title: String, size: NSSize, minSize: NSSize? = nil, @ViewBuilder _ content: () -> V) {
        if let w = windows[id] {
            w.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            updateViewOpen()
            return
        }
        let w = NSWindow(contentRect: NSRect(origin: .zero, size: size),
                         styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
                         backing: .buffered, defer: false)
        w.title = title
        w.isReleasedWhenClosed = false
        w.minSize = minSize ?? NSSize(width: 480, height: 360)
        w.contentViewController = NSHostingController(rootView: content())
        w.setContentSize(size)
        w.center()
        w.setFrameAutosaveName("Headroom.\(id)")
        w.delegate = self
        windows[id] = w
        w.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        updateViewOpen()
    }

    func showLarge(drillIn group: String? = nil) {
        ui.drillIn = group
        show("large", title: "Headroom", size: NSSize(width: 980, height: 720), minSize: NSSize(width: 720, height: 520)) {
            LargeView(model: model, ui: ui, windows: self)
        }
    }

    func showGraphs(_ groups: [String]? = nil) {
        if let groups { ui.graphsSelection = Set(groups) }
        if ui.graphsSelection.isEmpty { ui.graphsSelection = Set(model.keyIDs) }
        show("graphs", title: "Graphs", size: NSSize(width: 900, height: 640)) {
            GraphsView(model: model, ui: ui)
        }
    }

    func showExplorer() {
        show("explorer", title: "Storage Explorer", size: NSSize(width: 960, height: 700), minSize: NSSize(width: 720, height: 480)) {
            ExplorerWindow(model: model, windows: self)
        }
    }

    func showAssistant(scope: AssistantScope) {
        if ui.assistantScope != scope { model.assistant.reset() }
        ui.assistantScope = scope
        show("assistant", title: "Ask AI", size: NSSize(width: 560, height: 720), minSize: NSSize(width: 420, height: 420)) {
            AssistantView(model: model, ui: ui, windows: self)
        }
    }

    func showSummaries() {
        show("summaries", title: "Summary History", size: NSSize(width: 760, height: 560)) {
            SummaryHistoryView(model: model)
        }
    }

    func showSettings() {
        show("settings", title: "Headroom Settings", size: NSSize(width: 760, height: 600), minSize: NSSize(width: 640, height: 480)) {
            SettingsView(model: model, windows: self)
        }
    }

    /// The Actions panel for an item (inspector in the large view).
    func inspect(_ item: ItemRef) {
        ui.inspecting = item
        if windows["large"]?.isVisible != true { showLarge(drillIn: item.groupID) }
    }

    // MARK: confirmation

    /// One sheet per confirmation, in its own panel above everything, so it
    /// works whichever window the request came from.
    private func syncConfirmation(_ req: ConfirmationRequest?) {
        if req == nil {
            confirmWindow?.orderOut(nil)
            confirmWindow = nil
            return
        }
        guard confirmWindow == nil else { return }
        let p = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 460, height: 380),
                        styleMask: [.titled, .closable, .fullSizeContentView],
                        backing: .buffered, defer: false)
        p.title = "Confirm Action"
        p.titlebarAppearsTransparent = true
        p.level = .modalPanel
        p.isReleasedWhenClosed = false
        p.delegate = self
        p.contentViewController = NSHostingController(rootView: ConfirmationSheet(flow: model.actions.flow, catalog: model.actions))
        p.center()
        confirmWindow = p
        NSApp.activate(ignoringOtherApps: true)
        p.makeKeyAndOrderFront(nil)
    }

    // MARK: NSWindowDelegate

    func windowWillClose(_ notification: Notification) {
        guard let w = notification.object as? NSWindow else { return }
        if w === confirmWindow {
            confirmWindow = nil
            model.actions.flow.decline()
        }
        if w === windows["assistant"] { model.assistant.reset() }
        DispatchQueue.main.async { self.updateViewOpen() }
    }

    func windowDidMove(_ notification: Notification) {
        if let p = panel, (notification.object as? NSWindow) === p {
            UserDefaults.standard.set(NSStringFromRect(p.frame), forKey: "panelFrame")
        }
    }

    func windowDidResize(_ notification: Notification) { windowDidMove(notification) }

    func windowDidMiniaturize(_ notification: Notification) { updateViewOpen() }
    func windowDidDeminiaturize(_ notification: Notification) { updateViewOpen() }

    private func updateViewOpen() {
        let open = panelVisible || windows.values.contains { $0.isVisible && !$0.isMiniaturized }
        model.setViewOpen(open)
    }
}
