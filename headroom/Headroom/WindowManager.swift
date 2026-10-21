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
    var settingsTab: SettingsTab = .general
    /// Height the panel may take under the icon on its screen.
    var panelMaxHeight: CGFloat = 800
}

/// Owns the panel that drops down from the menu bar icon and the app's
/// windows; reports to the model whether any view is open (sampling speeds
/// up only then).
@MainActor
final class WindowManager: NSObject, NSWindowDelegate {
    let model: HeadroomModel
    let ui = UIState()
    let permissions = SystemPermissions()
    private let statusItem: NSStatusItem
    private var panel: MenuPanel?
    private var outsideClicks: Any?
    private var windows: [String: NSWindow] = [:]
    private var confirmWindow: NSPanel?

    init(model: HeadroomModel, statusItem: NSStatusItem) {
        self.model = model
        self.statusItem = statusItem
        super.init()
        trackChanges { [weak self] in
            guard let self else { return }
            let current = self.model.actions.flow.current
            self.syncConfirmation(current)
        }
        trackChanges { [weak self] in
            guard let self else { return }
            let pinned = self.model.config.layout.panel?.always_on_top ?? false
            self.applyPinned(pinned)
        }
    }

    var panelVisible: Bool { panel?.isVisible ?? false }

    /// Pinned (layout.yaml panel.always_on_top): the panel stays open when
    /// you click elsewhere, above other windows.
    var pinned: Bool { model.config.layout.panel?.always_on_top ?? false }

    // MARK: panel

    func togglePanel() {
        panelVisible ? hidePanel() : showPanel()
    }

    func showPanel() {
        // Closing the last window hides the app; a hidden app shows no windows.
        if NSApp.isHidden { NSApp.unhideWithoutActivation() }
        let p = panel ?? makePanel()
        panel = p
        guard let button = statusItem.button, let window = button.window else { return }
        let spot = window.convertToScreen(button.convert(button.bounds, to: nil))
        let screen = NSScreen.screens.first { $0.frame.contains(spot.origin) }?.visibleFrame ?? NSScreen.main?.visibleFrame ?? .zero
        ui.panelMaxHeight = max(300, spot.minY - 6 - screen.minY - 8)
        p.present(below: spot)
        applyPinned(pinned)
        permissions.refresh()
        updateViewOpen()
    }

    func hidePanel() {
        panel?.orderOut(nil)
        stopWatchingOutsideClicks()
        updateViewOpen()
    }

    private func makePanel() -> MenuPanel {
        let p = MenuPanel(content: PanelRoot(model: model, ui: ui, windows: self) { [weak self] size in
            DispatchQueue.main.async { self?.panel?.fit(size) }     // not mid-update
        })
        p.onCancel = { [weak self] in self?.hidePanel() }
        return p
    }

    private func applyPinned(_ on: Bool) {
        guard let p = panel else { return }
        p.level = on ? .floating : .popUpMenu
        if on || !p.isVisible {
            stopWatchingOutsideClicks()
        } else if outsideClicks == nil {
            // A click anywhere else closes it, like a menu.
            outsideClicks = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] _ in
                MainActor.assumeIsolated { self?.hidePanel() }
            }
        }
    }

    private func stopWatchingOutsideClicks() {
        if let outsideClicks { NSEvent.removeMonitor(outsideClicks) }
        outsideClicks = nil
    }

    func setAlwaysOnTop(_ on: Bool) {
        applyPinned(on)
    }

    /// Runs `open` after closing the panel unless it's pinned (a window
    /// opened from the panel takes over).
    func fromPanel(_ open: () -> Void) {
        if !pinned { hidePanel() }
        open()
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
        w.toolbarStyle = .unified
        w.isReleasedWhenClosed = false
        w.minSize = minSize ?? NSSize(width: 480, height: 360)
        w.contentViewController = NSHostingController(rootView: content())
        w.setContentSize(size)
        // Where it was last time, else centred.
        if !w.setFrameUsingName("Headroom.\(id)") { w.center() }
        w.setFrameAutosaveName("Headroom.\(id)")
        w.delegate = self
        windows[id] = w
        w.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        updateViewOpen()
    }

    func showLarge(drillIn group: String? = nil) {
        ui.drillIn = group
        show("large", title: "Headroom Overview", size: NSSize(width: 980, height: 720), minSize: NSSize(width: 720, height: 520)) {
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

    func showSettings(tab: SettingsTab? = nil) {
        if let tab { ui.settingsTab = tab }
        show("settings", title: "Headroom Settings", size: NSSize(width: 760, height: 600), minSize: NSSize(width: 640, height: 480)) {
            SettingsView(model: model, windows: self)
        }
    }

    /// The Actions panel for an item (inspector in the large view).
    func inspect(_ item: ItemRef) {
        ui.inspecting = item
        if let w = windows["large"], w.isVisible {
            // Already open (maybe behind another app's window): bring it forward, keep its drill-in.
            w.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
        } else {
            showLarge(drillIn: item.groupID)
        }
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
        // The drop-down panel sits at menu level, above the sheet.
        if !pinned { hidePanel() }
        let p = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 460, height: 380),
                        styleMask: [.titled, .closable, .fullSizeContentView],
                        backing: .buffered, defer: false)
        p.title = "Confirm Action"
        p.titlebarAppearsTransparent = true
        p.level = .modalPanel
        // No Dock icon: a sheet hidden on deactivate couldn't be brought back.
        p.hidesOnDeactivate = false
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
        DispatchQueue.main.async {
            self.updateViewOpen()
            // Back to being a menu bar app once its windows are gone: the
            // app you were in before comes forward again.
            let anyOpen = self.windows.values.contains { $0.isVisible || $0.isMiniaturized } || self.confirmWindow != nil
            if !anyOpen && !self.panelVisible { NSApp.hide(nil) }
        }
    }

    func windowDidMiniaturize(_ notification: Notification) { updateViewOpen() }
    func windowDidDeminiaturize(_ notification: Notification) { updateViewOpen() }

    private func updateViewOpen() {
        let open = panelVisible || windows.values.contains { $0.isVisible && !$0.isMiniaturized }
        model.setViewOpen(open)
    }
}

/// The panel that drops down from the menu bar icon: glass, rounded, with a
/// shadow. It takes clicks without taking the focus from the app you're in,
/// and sizes itself to its content (top staying under the icon).
@MainActor
final class MenuPanel: NSPanel {
    private let hosting: NSHostingView<AnyView>
    /// Escape (when nothing in it takes it).
    var onCancel: (() -> Void)?
    /// Where it hangs from: the icon's spot on the screen.
    private var anchor: NSRect = .zero
    static let width: CGFloat = 380

    init<V: View>(content: V) {
        hosting = NSHostingView(rootView: AnyView(content))
        super.init(contentRect: NSRect(x: 0, y: 0, width: Self.width, height: 600),
                   styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        isFloatingPanel = true
        level = .popUpMenu
        backgroundColor = .clear
        isOpaque = false
        hasShadow = true
        hidesOnDeactivate = false
        isReleasedWhenClosed = false
        canHide = false
        hosting.sizingOptions = [.intrinsicContentSize]
        // The window's height clamp (fit) wins over the content's wish to grow.
        hosting.setContentCompressionResistancePriority(.init(490), for: .vertical)
        collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .transient]
        let glass = NSVisualEffectView()
        glass.material = .popover
        glass.blendingMode = .behindWindow
        glass.state = .active
        glass.wantsLayer = true
        glass.layer?.cornerRadius = 16
        glass.layer?.masksToBounds = true
        hosting.translatesAutoresizingMaskIntoConstraints = false
        glass.addSubview(hosting)
        NSLayoutConstraint.activate([
            hosting.leadingAnchor.constraint(equalTo: glass.leadingAnchor),
            hosting.trailingAnchor.constraint(equalTo: glass.trailingAnchor),
            hosting.topAnchor.constraint(equalTo: glass.topAnchor),
            hosting.bottomAnchor.constraint(equalTo: glass.bottomAnchor),
        ])
        contentView = glass
    }

    override var canBecomeKey: Bool { true }

    /// Shows it under `spot` (the icon, in screen coordinates), centred under
    /// the icon and kept on that screen.
    func present(below spot: NSRect) {
        anchor = spot
        fit(hosting.fittingSize)
        makeKeyAndOrderFront(nil)
    }

    /// Takes `size`, its top staying where it is.
    func fit(_ size: CGSize) {
        guard size.width > 0, size.height > 0, anchor != .zero else { return }
        let screen = NSScreen.screens.first { $0.frame.contains(anchor.origin) }?.visibleFrame ?? NSScreen.main?.visibleFrame ?? .zero
        var x = anchor.midX - size.width / 2
        x = min(max(x, screen.minX + 8), screen.maxX - size.width - 8)
        let top = anchor.minY - 6
        let height = min(size.height, top - screen.minY - 8)
        let frame = NSRect(x: x, y: top - height, width: size.width, height: height)
        if frame != self.frame {
            setFrame(frame, display: true)
            invalidateShadow()
        }
    }

    override func cancelOperation(_ sender: Any?) {
        onCancel?()
    }
}
