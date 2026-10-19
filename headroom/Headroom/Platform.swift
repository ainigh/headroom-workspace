import AppKit
import Carbon.HIToolbox
import HeadroomKit
import IOKit.ps
import ServiceManagement
import UserNotifications

/// macOS notifications for HeadroomKit's NotificationStore.
final class SystemNotifier: Notifier, @unchecked Sendable {
    /// UNUserNotificationCenter throws an Objective-C exception (a crash)
    /// in a process without a bundle identifier, e.g. a `swift run` build.
    private let available = Bundle.main.bundleIdentifier != nil

    init() {
        guard available else { return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
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
