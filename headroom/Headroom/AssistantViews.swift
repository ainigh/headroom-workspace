import HeadroomKit
import SwiftUI

/// The assistant panel (ui-design §9).
struct AssistantView: View {
    let model: HeadroomModel
    let ui: UIState
    let windows: WindowManager
    @State private var question = ""

    var body: some View {
        let a = model.assistant
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Badge(text: ui.assistantScope.label(model.config))
                Badge(text: model.modeViewModel?.label ?? model.mode)
                Picker("Model", selection: Binding(get: { a.model }, set: { a.setModel($0) })) {
                    if a.models.isEmpty, !a.model.isEmpty { Text(a.model).tag(a.model) }
                    ForEach(a.models) { m in Text(m.name).tag(m.name) }
                }
                .labelsHidden()
                .frame(maxWidth: 200)
                Spacer()
                if let s = a.status {
                    Text(s.rawValue).font(.system(size: 10, weight: .bold)).foregroundStyle(.white)
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Capsule().fill(s.color))
                }
                Button { windows.showSummaries() } label: { Image(systemName: "clock.arrow.circlepath") }
                    .buttonStyle(.borderless).help("Summary History")
                Button { a.reset() } label: { Image(systemName: "square.and.pencil") }
                    .buttonStyle(.borderless).help("New conversation").disabled(a.running)
            }
            .padding(10)
            ProgressView(value: a.fuel).progressViewStyle(.linear).tint(a.fuel > 0.8 ? .orange : .accentColor)
                .frame(height: 2).opacity(a.fuel > 0 ? 1 : 0)
                .help("Query budget used")
            Divider()
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        if let w = a.warning { Label(w, systemImage: "exclamationmark.triangle").font(.system(size: 12)).foregroundStyle(.orange) }
                        if model.config.assistant.enabled == false {
                            Text("The assistant is turned off in assistant.yaml.").foregroundStyle(.secondary)
                        }
                        if a.turns.isEmpty {
                            Text(model.modeViewModel?.question ?? "").foregroundStyle(.secondary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        ForEach(a.turns) { t in
                            TurnView(model: model, turn: t).id(t.id)
                        }
                    }
                    .padding(14)
                }
                .onChange(of: a.turns.count) { _, _ in
                    if let last = a.turns.last { withAnimation { proxy.scrollTo(last.id, anchor: .top) } }
                }
            }
            Divider()
            HStack(spacing: 8) {
                TextField(a.turns.isEmpty ? "Ask, or press Return for the mode's question" : "Follow up", text: $question, axis: .vertical)
                    .textFieldStyle(.plain)
                    .lineLimit(1...4)
                    .onSubmit(send)
                if a.running {
                    Button("Stop") { a.stop() }.keyboardShortcut(.cancelAction)
                } else {
                    Button("Ask", action: send).keyboardShortcut(.defaultAction).disabled(!a.available)
                }
            }
            .padding(10)
        }
        .task {
            await a.loadModels()
            if a.turns.isEmpty, a.available { send() }
        }
    }

    func send() {
        let q = question
        question = ""
        model.assistant.ask(q, mode: model.mode, scope: ui.assistantScope, keyIDs: model.keyIDs)
    }
}

struct TurnView: View {
    let model: HeadroomModel
    let turn: AssistantTurn
    @State private var traceOpen = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(turn.question).font(.system(size: 12, weight: .medium)).foregroundStyle(.secondary)
            if !turn.trace.isEmpty || turn.running {
                DisclosureGroup(isExpanded: $traceOpen) {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(turn.trace) { q in
                            VStack(alignment: .leading, spacing: 3) {
                                HStack {
                                    Text(q.call).font(.system(size: 11, design: .monospaced)).lineLimit(2).textSelection(.enabled)
                                    Spacer()
                                    Text(q.error == nil ? "\(q.rows) rows" : "error").font(.system(size: 10)).foregroundStyle(.secondary)
                                }
                                if let e = q.error { Text(e).font(.system(size: 11)).foregroundStyle(.red) }
                                ForEach(Array(q.items.prefix(2).enumerated()), id: \.offset) { _, it in
                                    SysqTableView(table: it.table, maxHeight: 120)
                                }
                            }
                        }
                    }
                } label: {
                    HStack(spacing: 6) {
                        if turn.running { ProgressView().controlSize(.small) }
                        Text(turn.progress ?? "Investigation: \(turn.trace.count) queries").font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                }
            }
            ForEach(turn.notes, id: \.self) { n in Label(n, systemImage: "info.circle").font(.system(size: 11)).foregroundStyle(.secondary) }
            if let r = turn.response {
                if let s = r.summary { Text(s).font(.system(size: 13, weight: .semibold)).textSelection(.enabled) }
                if let st = r.status {
                    HStack(spacing: 6) {
                        Circle().fill(st.color).frame(width: 8, height: 8)
                        Text("\(st.rawValue)\(r.statusLine.map { " — \($0)" } ?? "")").font(.system(size: 12))
                    }
                }
                if let m = r.message { MessageText(model: model, text: m) }
                if r.incomplete { Label("The answer was incomplete; any action request in it was ignored.", systemImage: "exclamationmark.triangle").font(.system(size: 11)).foregroundStyle(.orange) }
                if let a = r.actionRequest {
                    ActionRequestCard(model: model, turn: turn, action: a, explanation: r.actionExplanation)
                } else if let problem = r.actionProblem {
                    Label("Action request \(problem)", systemImage: "nosign").font(.system(size: 11)).foregroundStyle(.secondary)
                }
            }
            if let e = turn.error { Label(e, systemImage: "xmark.octagon").font(.system(size: 12)).foregroundStyle(.red) }
        }
    }
}

/// MESSAGE with recipe and action names as clickable chips.
struct MessageText: View {
    let model: HeadroomModel
    let text: String

    var body: some View {
        let names = Set(model.recipes.catalog.map(\.name)).union(model.actions.catalog.map(\.name))
        VStack(alignment: .leading, spacing: 4) {
            ForEach(Array(text.components(separatedBy: "\n").enumerated()), id: \.offset) { _, line in
                Text(attributed(line, names: names)).font(.system(size: 12)).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .environment(\.openURL, OpenURLAction { url in
            if url.scheme == "headroom", let name = url.host {
                if model.recipes.entry(name) != nil { Task { await model.recipes.run(name) } }
                return .handled
            }
            return .systemAction
        })
    }

    func attributed(_ line: String, names: Set<String>) -> AttributedString {
        var out = AttributedString()
        var word = ""
        func flush() {
            guard !word.isEmpty else { return }
            let trimmed = word.trimmingCharacters(in: CharacterSet(charactersIn: "`.,;:()\"'"))
            var piece = AttributedString(word)
            if names.contains(trimmed), let url = URL(string: "headroom://\(trimmed)") {
                piece.link = url
            }
            out += piece
            word = ""
        }
        for ch in line {
            if ch == " " { flush(); out += AttributedString(" ") } else { word.append(ch) }
        }
        flush()
        return out
    }
}

struct ActionRequestCard: View {
    let model: HeadroomModel
    let turn: AssistantTurn
    let action: ParsedAction
    let explanation: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
                Text(action.text).font(.system(size: 12, design: .monospaced)).textSelection(.enabled)
                Spacer()
                if let tier = turn.actionInfo?.tier { Badge(text: tier.uppercased()) }
            }
            if let e = turn.actionInfo?.effect { Text(e).font(.system(size: 11)).foregroundStyle(.secondary) }
            if let x = explanation { Text(x).font(.system(size: 12)).fixedSize(horizontal: false, vertical: true) }
            if let r = turn.actionResult {
                Label(r.approved ? (r.ok ? "Ran: \(r.output)" : "Failed: \(r.output)") : "Declined", systemImage: r.approved && r.ok ? "checkmark.circle" : "xmark.circle")
                    .font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(4)
            } else {
                HStack {
                    Button("Confirm…") { model.assistant.confirmAction(turn.id) }
                        .buttonStyle(.bordered).tint(.orange)
                        .disabled(model.assistant.running || model.actions.busy != nil)
                    Button("Dismiss") { model.assistant.dismissAction(turn.id) }.buttonStyle(.borderless)
                }
                .controlSize(.small)
            }
        }
        .padding(10)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.orange.opacity(0.08)))
    }
}

/// Summary History (headroom-spec §8.10): browse, search, filter, delete.
struct SummaryHistoryView: View {
    let model: HeadroomModel
    @State private var search = ""
    @State private var status = "all"
    @State private var selection: Set<String> = []
    @State private var before = Date().addingTimeInterval(-30 * 86400)
    @State private var confirmAll = false

    var body: some View {
        let all = model.assistant.summaryList.sorted { $0.at > $1.at }
        let shown = all.filter { s in
            (status == "all" || s.status == status) &&
            (search.isEmpty || s.summary.localizedCaseInsensitiveContains(search) || s.scope.localizedCaseInsensitiveContains(search))
        }
        VStack(spacing: 0) {
            HStack {
                TextField("Search", text: $search).textFieldStyle(.roundedBorder).frame(width: 200)
                Picker("Status", selection: $status) {
                    Text("All").tag("all")
                    ForEach(AssistantStatus.allCases, id: \.self) { s in Text(s.rawValue).tag(s.rawValue) }
                }
                .frame(width: 160)
                Spacer()
                Button("Delete selected") { model.assistant.deleteSummaries(selection); selection = [] }.disabled(selection.isEmpty)
                DatePicker("Before", selection: $before, displayedComponents: .date).labelsHidden().frame(width: 120)
                Button("Delete before") { model.assistant.deleteSummaries(before: before) }
                Button("Delete all", role: .destructive) { confirmAll = true }
            }
            .padding(10)
            List(shown, selection: $selection) { s in
                HStack(alignment: .top, spacing: 8) {
                    Circle().fill(AssistantStatus(rawValue: s.status ?? "")?.color ?? .gray).frame(width: 8, height: 8).padding(.top, 4)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(s.summary).font(.system(size: 12))
                        HStack(spacing: 8) {
                            Text(s.at.formatted(date: .abbreviated, time: .shortened))
                            Text(s.mode)
                            Text(s.scope)
                            if let a = s.action { Text(a).font(.system(size: 10, design: .monospaced)) }
                        }
                        .font(.system(size: 10)).foregroundStyle(.secondary)
                    }
                }
                .tag(s.id)
                .contextMenu { Button("Delete") { model.assistant.deleteSummaries([s.id]) } }
            }
        }
        .confirmationDialog("Delete every summary? The assistant forgets all earlier answers.", isPresented: $confirmAll) {
            Button("Delete all", role: .destructive) { model.assistant.deleteAllSummaries() }
        }
        .onAppear { model.assistant.refreshSummaries() }
    }
}
