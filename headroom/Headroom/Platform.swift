import AppKit
import Carbon.HIToolbox
import HeadroomKit
import IOKit.ps
import Observation
import ServiceManagement
import UserNotifications

/// macOS notifications for HeadroomKit's NotificationStore.
final class SystemNotifier: NSObject, Notifier, UNUserNotificationCenterDelegate, @unchecked Sendable {
    /// UNUserNotificationCenter throws an Objective-C exception (a crash)
    /// in a process without a bundle identifier, e.g. a `swift run` build.
    private let available = Bundle.main.bundleIdentifier != nil

    override init() {
        super.init()
        guard available else { return }
        UNUserNotificationCenter.current().delegate = self
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    /// Without this, macOS drops a notification while one of Headroom's
    /// windows is in front.
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }

    func post(id: String, title: String, body: String) {
        guard available else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        let req = UNNotificationRequest(identifier: "\(id).\(UUID().uuidString)", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req) { _ in }
    }
}

/// What macOS lets Headroom do, for the permissions view. Only reads; the
/// user grants things in System Settings (or in macOS's own prompt).
@MainActor
@Observable
final class SystemPermissions {
    enum NotificationState { case unknown, notAsked, allowed, denied }

    private(set) var notifications: NotificationState = .unknown
    /// UNUserNotificationCenter needs a bundle identifier (not a `swift run` build).
    let canNotify = Bundle.main.bundleIdentifier != nil

    func refresh() {
        guard canNotify else { return }
        UNUserNotificationCenter.current().getNotificationSettings { [weak self] settings in
            let state: NotificationState
            switch settings.authorizationStatus {
            case .notDetermined: state = .notAsked
            case .denied: state = .denied
            default: state = .allowed
            }
            Task { @MainActor in self?.notifications = state }
        }
    }

    func requestNotifications() {
        guard canNotify else { return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { [weak self] _, _ in
            Task { @MainActor in self?.refresh() }
        }
    }

    /// The items that need the user, for the panel's setup line.
    func needsAttention(_ setup: SetupStore) -> Int {
        var n = 0
        if canNotify, notifications == .notAsked || notifications == .denied { n += 1 }
        if setup.loaded, !setup.sudoReady { n += 1 }
        return n
    }

    enum Pane { case notifications, fullDiskAccess, automation, loginItems }

    static func open(_ pane: Pane) {
        let url: String
        switch pane {
        case .notifications: url = "x-apple.systempreferences:com.apple.preference.notifications"
        case .fullDiskAccess: url = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"
        case .automation: url = "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation"
        case .loginItems: url = "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"
        }
        if let u = URL(string: url) { NSWorkspace.shared.open(u) }
    }
}

/// Reports whether the Mac runs on battery (sampling slows down).
@MainActor
final class PowerMonitor {
    var onChange: ((Bool) -> Void)?
    private var last: Bool?
    private var timer: Timer?

    static func onBattery() -> Bool {
        guard let info = IOPSCopyPowerSourcesInfo()?.takeRetainedValue(),
              let type = IOPSGetProvidingPowerSourceType(info)?.takeUnretainedValue() else { return false }
        return (type as String) == "Battery Power" // kIOPSBatteryPowerValue
    }

    func start() {
        check()
        timer = Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.check() }
        }
    }

    func check() {
        let b = Self.onBattery()
        if b != last {
            last = b
            onChange?(b)
        }
    }
}

/// ⌃⌥H toggles the small view from anywhere (Carbon hot key: no
/// accessibility permission needed; ⌥⌘H would take "Hide Others").
final class HotKey {
    private var ref: EventHotKeyRef?
    private var handler: EventHandlerRef?
    private static var action: (() -> Void)?

    init(_ action: @escaping () -> Void) {
        HotKey.action = action
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        InstallEventHandler(GetApplicationEventTarget(), { _, _, _ in
            DispatchQueue.main.async { HotKey.action?() }
            return noErr
        }, 1, &spec, nil, &handler)
        let id = EventHotKeyID(signature: OSType(0x4844_524D), id: 1) // "HDRM"
        RegisterEventHotKey(UInt32(kVK_ANSI_H), UInt32(controlKey | optionKey), id, GetApplicationEventTarget(), 0, &ref)
    }

    deinit {
        if let ref { UnregisterEventHotKey(ref) }
        if let handler { RemoveEventHandler(handler) }
    }
}

enum LaunchAtLogin {
    static var enabled: Bool { SMAppService.mainApp.status == .enabled }
    /// Registered, but macOS wants the user to allow it in Login Items.
    static var needsApproval: Bool { SMAppService.mainApp.status == .requiresApproval }

    static func set(_ on: Bool) -> String? {
        do {
            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            return nil
        } catch {
            return error.localizedDescription
        }
    }
}

@MainActor
enum Finder {
    static func reveal(_ path: String) {
        let p = (path as NSString).expandingTildeInPath
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: p)])
    }

    static func open(_ path: String) {
        NSWorkspace.shared.open(URL(fileURLWithPath: (path as NSString).expandingTildeInPath))
    }

    static func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    static func openTerminal() {
        if let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.apple.Terminal") {
            NSWorkspace.shared.openApplication(at: url, configuration: NSWorkspace.OpenConfiguration())
        }
    }

    /// One of the tools inside Headroom.app (sysqd, sysq, sysact), if this is the app.
    static func bundledTool(_ name: String) -> String? {
        guard let url = Bundle.main.resourceURL?.appendingPathComponent("bin").appendingPathComponent(name),
              FileManager.default.isExecutableFile(atPath: url.path) else { return nil }
        return url.path
    }

    /// sysq's policy.yaml (its folder when there's no file yet).
    static func openPolicy() {
        let dir = Paths().sysq
        let file = dir.appendingPathComponent("policy.yaml")
        if FileManager.default.fileExists(atPath: file.path) {
            NSWorkspace.shared.open(file)
        } else if FileManager.default.fileExists(atPath: dir.path) {
            NSWorkspace.shared.open(dir)
        }
    }

    static func activityMonitor() {
        if let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.apple.ActivityMonitor") {
            NSWorkspace.shared.openApplication(at: url, configuration: NSWorkspace.OpenConfiguration())
        }
    }

    /// The app icon for a process or path, when it resolves.
    static func icon(kind: String?, id: String?, name: String) -> NSImage? {
        if kind == "process", let id, let pid = Int32(id), let app = NSRunningApplication(processIdentifier: pid) { return app.icon }
        if kind == "path", let id { return NSWorkspace.shared.icon(forFile: (id as NSString).expandingTildeInPath) }
        if kind == "app", let id, let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: id) {
            return NSWorkspace.shared.icon(forFile: url.path)
        }
        return nil
    }
}
