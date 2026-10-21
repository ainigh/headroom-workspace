import AppKit
import HeadroomKit
import SwiftUI

/// Settings (headroom-spec §12, ui-design §12): every tab edits a YAML
/// file — validated against its schema before it's saved — and offers
/// "Edit file…"; external edits show up live.
enum SettingsTab: String, Hashable {
    case general, permissions, groups, modes, thresholds, assistant, api, notifications, advanced
}

@MainActor
struct SettingsView: View {
    let model: HeadroomModel
    let windows: WindowManager

    var body: some View {
        TabView(selection: Binding(get: { windows.ui.settingsTab }, set: { windows.ui.settingsTab = $0 })) {
            GeneralTab(model: model).tabItem { Label("General", systemImage: "gearshape") }.tag(SettingsTab.general)
            PermissionsTab(model: model, permissions: windows.permissions).tabItem { Label("Permissions", systemImage: "checkmark.shield") }.tag(SettingsTab.permissions)
            YAMLEditor(model: model, file: "groups").tabItem { Label("Groups", systemImage: "square.grid.2x2") }.tag(SettingsTab.groups)
            YAMLEditor(model: model, file: "modes").tabItem { Label("Modes", systemImage: "slider.horizontal.3") }.tag(SettingsTab.modes)
            YAMLEditor(model: model, file: "thresholds").tabItem { Label("Thresholds", systemImage: "gauge.with.dots.needle.67percent") }.tag(SettingsTab.thresholds)
            AssistantTab(model: model).tabItem { Label("Assistant", systemImage: "sparkles") }.tag(SettingsTab.assistant)
            APITab(model: model).tabItem { Label("API", systemImage: "network") }.tag(SettingsTab.api)
            YAMLEditor(model: model, file: "notifications").tabItem { Label("Notifications", systemImage: "bell") }.tag(SettingsTab.notifications)
            AdvancedTab(model: model).tabItem { Label("Advanced", systemImage: "wrench.and.screwdriver") }.tag(SettingsTab.advanced)
        }
        .padding(12)
    }
}

/// A YAML file in a text editor: Save validates first and reports the line.
@MainActor
struct YAMLEditor: View {
    let model: HeadroomModel
    let file: String
    @State private var text = ""
    @State private var problem: ConfigProblem?
    @State private var loadedGeneration = -1
    @State private var saved = false

    var url: URL { model.configStore.loader.userDir.appendingPathComponent("\(file).yaml") }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("\(file).yaml").font(.system(size: 12, weight: .semibold, design: .monospaced))
                if let p = model.configStore.problems.first(where: { $0.file == "\(file).yaml" }) {
                    Label(p.text, systemImage: "exclamationmark.triangle").font(.system(size: 11)).foregroundStyle(.orange).lineLimit(2)
                }
                Spacer()
                Button("Edit file…") { NSWorkspace.shared.open(url) }
                Button("Revert") { load() }
                Button("Save") { save() }.keyboardShortcut("s").buttonStyle(.borderedProminent)
            }
            TextEditor(text: $text)
                .font(.system(size: 12, design: .monospaced))
                .autocorrectionDisabled()
                .border(Color.secondary.opacity(0.2))
            if let p = problem {
                Label(p.text, systemImage: "xmark.octagon").foregroundStyle(.red).font(.system(size: 12)).textSelection(.enabled)
            } else if saved {
                Label("Saved and applied.", systemImage: "checkmark.circle").foregroundStyle(.green).font(.system(size: 12))
            }
        }
        .onAppear { load() }
        .onChange(of: model.configStore.generation) { _, _ in
            // An external edit: reload unless there are unsaved changes.
            // (Our own save also bumps the generation; the file then matches
            // and "Saved and applied." stays up.)
            if text == lastSavedText, model.configStore.loader.text(of: file) != text { load() }
        }
    }

    @State private var lastSavedText = ""

    func load() {
        text = model.configStore.loader.text(of: file)
        lastSavedText = text
        problem = nil
        saved = false
    }

    func save() {
        problem = model.configStore.save(file, text: text)
        saved = problem == nil
        if saved { lastSavedText = text }
    }
}

@MainActor
struct GeneralTab: View {
    let model: HeadroomModel
    @State private var error: String?

    var body: some View {
        let layout = model.config.layout
        Form {
            Section {
                LabeledContent("Version") { UpdateButton() }
                LabeledContent("Start") { LaunchAtLoginToggle() }
                if let e = error { Text(e).foregroundStyle(.red).font(.system(size: 11)) }
            }
            Section("Key groups (layout.yaml)") {
                Picker("Choose", selection: Binding(get: { layout.keyMode }, set: { m in
                    error = model.setNested("layout", parent: "key_groups", key: "mode", value: m)?.text
                })) {
                    Text("Pinned").tag("pinned")
                    Text("Auto (most stressed)").tag("auto")
                }
                Text("Pinned: \(layout.pinned.joined(separator: ", ")). Right-click a group in the Overview to add or remove it.")
                    .font(.system(size: 11)).foregroundStyle(.secondary)
                Toggle("Pin the panel (stays open and on top)", isOn: Binding(get: { layout.panel?.always_on_top ?? false }, set: { on in
                    error = model.setAlwaysOnTop(on)?.text
                }))
            }
            Section("Files") {
                Button("Open the config folder") { NSWorkspace.shared.open(model.configStore.loader.userDir) }
                YAMLEditor(model: model, file: "layout").frame(height: 220)
            }
        }
        .formStyle(.grouped)
    }
}

@MainActor
struct AssistantTab: View {
    let model: HeadroomModel
    @State private var error: String?

    var body: some View {
        let a = model.config.assistant
        VStack(alignment: .leading, spacing: 10) {
            Form {
                Picker("Model", selection: Binding(get: { model.assistant.model }, set: { m in
                    model.assistant.setModel(m)
                    error = model.setTopLevel("assistant", key: "model", value: "\"\(m)\"")?.text
                })) {
                    if !model.assistant.models.contains(where: { $0.name == model.assistant.model }) {
                        Text(model.assistant.model.isEmpty ? "Choose…" : model.assistant.model).tag(model.assistant.model)
                    }
                    ForEach(model.assistant.models) { m in
                        Text(m.size.map { "\(m.name) (\(Units.bytes($0)))" } ?? m.name).tag(m.name)
                    }
                }
                Picker("Privacy", selection: Binding(get: { a.privacy }, set: { p in
                    error = model.setTopLevel("assistant", key: "privacy_level", value: p)?.text
                })) {
                    Text("Aggregates only").tag("aggregates")
                    Text("+ consumers (process names)").tag("consumers")
                    Text("+ files (paths)").tag("files")
                }
                Text("The assistant never gets secret-class data, whatever this says.").font(.system(size: 11)).foregroundStyle(.secondary)
                if let e = error { Text(e).foregroundStyle(.red).font(.system(size: 11)) }
                if let w = model.assistant.warning { Text(w).foregroundStyle(.orange).font(.system(size: 11)) }
            }
            .formStyle(.grouped)
            .frame(height: 210)
            HStack {
                Button("Edit system prompt…") {
                    NSWorkspace.shared.open(model.configStore.loader.userDir.appendingPathComponent(a.system_prompt ?? "prompts/system.md"))
                }
                Button("Edit request template…") {
                    NSWorkspace.shared.open(model.configStore.loader.userDir.appendingPathComponent(a.request_template ?? "prompts/request.md.tmpl"))
                }
            }
            YAMLEditor(model: model, file: "assistant")
        }
        .task { await model.assistant.loadModels() }
    }
}

/// API settings and pairing (headroom-spec §11): approve first-contact
/// requests by their code, list and revoke clients.
@MainActor
struct APITab: View {
    let model: HeadroomModel
    @State private var codes: [String: String] = [:]
    @State private var privacy: [String: String] = [:]
    @State private var scripts: [String: Bool] = [:]

    var body: some View {
        let pairing = model.pairing
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(model.config.api.enabled == true
                     ? "API on at http://127.0.0.1:\(model.services.apiPort) — new clients need your approval."
                     : "API pairing is off (api.yaml enabled: false). Paired clients keep working; new ones are refused.")
                    .font(.system(size: 12))
                Spacer()
            }
            GroupBox("Waiting for approval") {
                if pairing.pending.isEmpty {
                    Text("No requests. A new client's first call shows up here with a code.").font(.system(size: 11)).foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                ForEach(pairing.pending) { p in
                    HStack {
                        VStack(alignment: .leading) {
                            Text(p.name).font(.system(size: 12, weight: .medium))
                            Text(p.origin ?? "no browser origin").font(.system(size: 10)).foregroundStyle(.secondary)
                        }
                        Spacer()
                        TextField("Code", text: Binding(get: { codes[p.id] ?? "" }, set: { codes[p.id] = $0 })).frame(width: 90)
                        Picker("", selection: Binding(get: { privacy[p.id] ?? (model.config.api.default_privacy ?? "aggregates") }, set: { privacy[p.id] = $0 })) {
                            Text("Aggregates").tag("aggregates")
                            Text("Consumers").tag("consumers")
                            Text("Files").tag("files")
                        }
                        .labelsHidden().frame(width: 120)
                        Toggle("Scripts", isOn: Binding(get: { scripts[p.id] ?? false }, set: { scripts[p.id] = $0 })).controlSize(.small)
                        Button("Approve") {
                            Task {
                                _ = await pairing.approve(p, code: codes[p.id] ?? "", privacy: privacy[p.id] ?? (model.config.api.default_privacy ?? "aggregates"),
                                                          allowScripts: scripts[p.id] ?? false, origins: p.origin.map { [$0] } ?? [])
                            }
                        }
                        .disabled((codes[p.id] ?? "").isEmpty)
                    }
                }
            }
            GroupBox("Connected clients") {
                if pairing.clients.isEmpty {
                    Text("None.").font(.system(size: 11)).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
                }
                ForEach(pairing.clients) { c in
                    HStack {
                        Text(c.name).font(.system(size: 12))
                        Badge(text: c.privacy)
                        if c.allow_scripts { Badge(text: "scripts") }
                        if let s = c.last_seen { Text("last seen \(s)").font(.system(size: 10)).foregroundStyle(.secondary) }
                        Spacer()
                        Button("Revoke", role: .destructive) { Task { await pairing.revoke(c.id) } }
                    }
                }
            }
            if let e = pairing.error { Text(e).foregroundStyle(.red).font(.system(size: 11)) }
            YAMLEditor(model: model, file: "api")
        }
        .onAppear { pairing.watch(true) }
        .onDisappear { pairing.watch(false) }
    }
}

@MainActor
struct AdvancedTab: View {
    let model: HeadroomModel
    @State private var status: JSONValue?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            GroupBox("Services") {
                VStack(alignment: .leading, spacing: 6) {
                    row("sysqd", model.services.sysq)
                    row("sysact", model.services.sysact)
                    if let s = status {
                        let classes: String = s["enabled_sensitivity"]?.array?.compactMap(\.string).joined(separator: ", ") ?? "—"
                        let sudo: String = s["sudo"]?.json ?? "none"
                        Text("Enabled sensitivity: " + classes).font(.system(size: 11))
                        Text("sudo primitives: " + sudo).font(.system(size: 11)).lineLimit(3)
                        if s["fixtures"]?.bool == true { Text("sysqd is serving recorded fixtures, not live data.").font(.system(size: 11)).foregroundStyle(.orange) }
                    }
                    HStack {
                        Button("Reconnect") { model.retryConnection() }
                        Button("Open sysq audit log") { NSWorkspace.shared.open(Paths().sysq.appendingPathComponent("audit")) }
                        Button("Open sysact audit log") { NSWorkspace.shared.open(Paths().sysact.appendingPathComponent("audit")) }
                        Button("Open logs") { NSWorkspace.shared.open(Paths().logs) }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack {
                Picker("", selection: $file) {
                    ForEach(["sampling", "retention"], id: \.self) { Text("\($0).yaml").tag($0) }
                }
                .pickerStyle(.segmented).labelsHidden().frame(width: 260)
                Spacer()
            }
            YAMLEditor(model: model, file: file).id(file)
        }
        .task { status = try? await model.services.admin.status() }
    }

    @State private var file = "sampling"

    func row(_ name: String, _ s: ServiceState) -> some View {
        HStack(spacing: 6) {
            Circle().fill(s.isRunning ? Color.green : (s.message == nil ? Color.gray : Color.red)).frame(width: 8, height: 8)
            Text(name).font(.system(size: 12, weight: .medium))
            switch s {
            case .running(let v): Text(v.isEmpty ? "running" : "running \(v)").font(.system(size: 11)).foregroundStyle(.secondary)
            case .starting: Text("starting…").font(.system(size: 11)).foregroundStyle(.secondary)
            case .failed(let m): Text(m).font(.system(size: 11)).foregroundStyle(.red).lineLimit(2)
            }
        }
    }
}

/// Permissions and one-time setup (README "What needs your Mac"): what each
/// is for, whether it's in place, and where to grant it. Headroom changes
/// nothing here itself; it shows the way.
@MainActor
struct PermissionsTab: View {
    let model: HeadroomModel
    let permissions: SystemPermissions
    @State private var copied = false

    var body: some View {
        let setup = model.setup
        Form {
            Section {
                PermissionRow(symbol: "bell.badge.fill", title: "Notifications",
                              detail: "Tells you when a group turns amber or red (notifications.yaml sets which, how often, and quiet hours).",
                              state: notificationState) {
                    switch permissions.notifications {
                    case .notAsked: Button("Allow…") { permissions.requestNotifications() }
                    case .denied: Button("Open Settings") { SystemPermissions.open(.notifications) }
                    default: EmptyView()
                    }
                }
                PermissionRow(symbol: "bolt.fill", title: "Power metrics (sudo rule)",
                              detail: "Neural Engine activity, thermal pressure and power come from powermetrics, which needs root. sysq runs it only with a sudoers rule for its exact flags, and never asks for a password.",
                              state: sudoState(setup)) {
                    Button("Check again") { Task { await setup.refresh(recheck: true) } }
                }
                if let line = setup.sudoersLine(user: NSUserName()) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("In Terminal, run `sudo visudo -f /etc/sudoers.d/sysq` and add this line:").font(.system(size: 11)).foregroundStyle(.secondary)
                        Text(line).font(.system(size: 10, design: .monospaced)).textSelection(.enabled)
                            .padding(6).frame(maxWidth: .infinity, alignment: .leading)
                            .background(RoundedRectangle(cornerRadius: 6).fill(Color.primary.opacity(0.06)))
                        HStack {
                            Button(copied ? "Copied" : "Copy line") {
                                Finder.copy(line)
                                copied = true
                            }
                            Button("Open Terminal") { Finder.openTerminal() }
                        }
                        .controlSize(.small)
                    }
                    .padding(.leading, 30)
                }
            }
            Section("Optional") {
                PermissionRow(symbol: "externaldrive.fill.badge.checkmark", title: "Full Disk Access for sysqd",
                              detail: "Lets the Storage Explorer size folders macOS protects (Mail, Messages, other apps' data). Without it those show smaller than they are. In Settings, click +, then choose sysqd (Show sysqd finds it).",
                              state: .optional("Optional")) {
                    Button("Open Settings") { SystemPermissions.open(.fullDiskAccess) }
                    if let sysqd = Finder.bundledTool("sysqd") { Button("Show sysqd") { Finder.reveal(sysqd) } }
                }
                PermissionRow(symbol: "trash.fill", title: "Automation (Finder)",
                              detail: "Moving things to the Trash goes through Finder, so they can be put back. macOS asks once, the first time you confirm a Trash action.",
                              state: .optional("Asked on first use")) {
                    Button("Open Settings") { SystemPermissions.open(.automation) }
                }
                PermissionRow(symbol: "brain", title: "Local model servers",
                              detail: "Which models are loaded comes from the local Ollama / LM Studio servers. sysq only talks to them when they're listed under network_primitives in its policy.yaml.",
                              state: setup.networkPrimitives.isEmpty ? .optional("Off") : .ok("On: \(setup.networkPrimitives.joined(separator: ", "))")) {
                    Button("Edit policy.yaml") { Finder.openPolicy() }
                }
                PermissionRow(symbol: "power", title: "Open at login",
                              detail: "Start Headroom when you log in (sysqd keeps recording history either way).",
                              state: LaunchAtLogin.needsApproval ? .needed("Waiting for your approval") : (LaunchAtLogin.enabled ? .ok("On") : .optional("Off"))) {
                    LaunchAtLoginToggle()
                    if LaunchAtLogin.needsApproval { Button("Open Login Items") { SystemPermissions.open(.loginItems) } }
                }
            }
            if let e = setup.error {
                Text("Couldn't ask sysqd: \(e)").font(.system(size: 11)).foregroundStyle(.orange)
            }
        }
        .formStyle(.grouped)
        .onAppear {
            permissions.refresh()
            Task { await setup.refresh() }
        }
    }

    var notificationState: PermissionState {
        switch permissions.notifications {
        case .allowed: return .ok("Allowed")
        case .denied: return .needed("Off in System Settings")
        case .notAsked: return .needed("Not allowed yet")
        case .unknown: return permissions.canNotify ? .unknown("Checking…") : .unknown("Needs the app (not a dev build)")
        }
    }

    func sudoState(_ setup: SetupStore) -> PermissionState {
        if !setup.loaded { return .unknown("Checking…") }
        if setup.error != nil { return .unknown("sysqd isn't answering") }
        if setup.fixtures { return .unknown("sysqd is serving fixtures") }
        if setup.sudo.isEmpty { return .ok("Not needed") }
        let missing = setup.sudo.filter { !$0.allowed }.count
        return missing == 0 ? .ok("In place") : .needed(missing == 1 ? "1 rule missing" : "\(missing) rules missing")
    }
}

enum PermissionState {
    case ok(String), needed(String), optional(String), unknown(String)

    var text: String {
        switch self {
        case .ok(let t), .needed(let t), .optional(let t), .unknown(let t): return t
        }
    }

    var color: Color {
        switch self {
        case .ok: return Palette.green
        case .needed: return Palette.orange
        case .optional, .unknown: return .gray
        }
    }
}

@MainActor
struct PermissionRow<Actions: View>: View {
    var symbol: String
    var title: String
    var detail: String
    var state: PermissionState
    @ViewBuilder var actions: Actions

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: symbol).font(.system(size: 13, weight: .semibold)).foregroundStyle(.white)
                .frame(width: 22, height: 22)
                .background(RoundedRectangle(cornerRadius: 6, style: .continuous).fill(state.color))
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(title).font(.system(size: 13, weight: .medium))
                    Text(state.text).font(.system(size: 10, weight: .medium)).foregroundStyle(state.color)
                        .padding(.horizontal, 6).padding(.vertical, 1)
                        .background(Capsule().fill(state.color.opacity(0.12)))
                }
                Text(detail).font(.system(size: 11)).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 8)
            HStack(spacing: 6) { actions }.controlSize(.small)
        }
        .padding(.vertical, 2)
    }
}
