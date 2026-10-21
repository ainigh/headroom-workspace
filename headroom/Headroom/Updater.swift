import AppKit
import Foundation
import HeadroomKit
import SwiftUI

/// Keeps the app on the newest commit of the branch it was built from (`main`, unless install.sh
/// was given another): checks at launch and every 6 hours, and installs on request.
///
/// Installing takes GitHub's prebuilt copy when there's a release made from that exact commit.
/// Otherwise it downloads the source and builds it here with Apple's command line tools and Go
/// (headroom/scripts/build-app.sh), so an update never has to wait for GitHub Actions. It talks
/// to GitHub through `gh` (signed in, so a private repository works too), else directly.
@MainActor
final class Updater: ObservableObject {
    static let shared = Updater()

    nonisolated static let repo = "ainigh/headroom-workspace"
    nonisolated static let assetName = "Headroom.zip"

    /// The branch this copy follows (build-app.sh writes it into Info.plist).
    nonisolated static var branch: String {
        let b = Bundle.main.object(forInfoDictionaryKey: "HeadroomBranch") as? String ?? ""
        return b.isEmpty ? "main" : b
    }

    /// The commit this copy was built from.
    nonisolated static var currentCommit: String? {
        Bundle.main.object(forInfoDictionaryKey: "HeadroomCommit") as? String
    }

    nonisolated static var version: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0.0.0-dev"
    }

    /// "0.1" from "0.1.12" or "0.1-abc1234".
    nonisolated static var versionBase: String {
        let parts = version.split(separator: "-")[0].split(separator: ".")
        return parts.prefix(2).joined(separator: ".")
    }

    struct Update: Equatable {
        var commit: Commit
        /// A release made from this commit, if GitHub built one.
        var release: Release?
    }

    enum State: Equatable {
        case idle, checking, upToDate
        case available(Update)
        case installing(String)
        case failed(String)
    }

    @Published private(set) var state: State = .idle
    private var started = false

    private var busy: Bool {
        switch state {
        case .checking, .installing: return true
        default: return false
        }
    }

    func start() {
        if started { return }
        started = true
        check(userInitiated: false)
        Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 6 * 3600 * 1_000_000_000)
                self?.check(userInitiated: false)
            }
        }
    }

    func check(userInitiated: Bool) {
        if busy { return }
        let before = state
        state = .checking
        Task.detached {
            let outcome: State
            do {
                outcome = try Self.findUpdate().map { .available($0) } ?? .upToDate
            } catch {
                outcome = .failed(error.localizedDescription)
            }
            await MainActor.run {
                // A quiet background check that fails shouldn't leave an error showing.
                if case .failed = outcome, !userInitiated {
                    if case .available = before { self.state = before } else { self.state = .idle }
                } else {
                    self.state = outcome
                }
            }
        }
    }

    func install() {
        guard case .available(let update) = state else { return }
        state = .installing("Updating…")
        Task.detached {
            do {
                try Self.install(update) { step in
                    Task { @MainActor in self.state = .installing(step) }
                }
            } catch {
                Self.log("update failed: \(error.localizedDescription)")
                await MainActor.run { self.state = .failed("Update failed: \(error.localizedDescription)") }
            }
        }
    }

    // MARK: What's new

    nonisolated static func findUpdate() throws -> Update? {
        let head = try Commit.decode(api("repos/\(repo)/commits/\(branch)"))
        if let mine = currentCommit, !mine.isEmpty, mine != "unknown", head.sha.hasPrefix(mine) || mine.hasPrefix(head.sha) { return nil }
        let release = try? Release.decode(api("repos/\(repo)/releases/latest"))
        let prebuilt = release.flatMap { $0.target == head.sha && $0.asset(named: assetName) != nil ? $0 : nil }
        return Update(commit: head, release: prebuilt)
    }

    // MARK: Installing

    nonisolated static func install(_ update: Update, step: @escaping @Sendable (String) -> Void) throws {
        let fm = FileManager.default
        let app = Bundle.main.bundleURL
        guard app.pathExtension == "app" else { throw Problem("this copy isn't an app bundle (a dev build?)") }
        // On the app's own volume, so the swap at the end is a rename.
        let work = try fm.url(for: .itemReplacementDirectory, in: .userDomainMask, appropriateFor: app, create: true)
        defer { try? fm.removeItem(at: work) }

        let newApp: URL
        if let release = update.release {
            step("Downloading \(release.tag)…")
            newApp = try downloadPrebuilt(release, into: work)
        } else {
            newApp = try buildFromSource(update.commit, in: work, step: step)
        }
        let newID = Bundle(url: newApp)?.bundleIdentifier
        guard newID != nil, newID == Bundle.main.bundleIdentifier else {
            throw Problem("the new copy isn't \(app.lastPathComponent)")
        }
        _ = try? run("/usr/bin/xattr", ["-dr", "com.apple.quarantine", newApp.path])

        step("Restarting…")
        _ = try fm.replaceItemAt(app, withItemAt: newApp)
        log("updated to \(update.commit.short) (\(update.release == nil ? "built here" : "prebuilt"))")
        // Start the new copy once this one has quit.
        let relaunch = Process()
        relaunch.executableURL = URL(fileURLWithPath: "/bin/sh")
        relaunch.arguments = ["-c", "while kill -0 \(getpid()) 2>/dev/null; do sleep 0.2; done; /usr/bin/open \"$0\"", app.path]
        try relaunch.run()
        DispatchQueue.main.async { NSApp.terminate(nil) }
    }

    nonisolated static func downloadPrebuilt(_ release: Release, into work: URL) throws -> URL {
        let zip = work.appendingPathComponent(assetName)
        if let gh = ghPath(), (try? run(gh, ["release", "download", release.tag, "--repo", repo, "--pattern", assetName,
                                            "--dir", work.path, "--clobber"])) != nil {
        } else if let asset = release.asset(named: assetName) {
            try download(asset.browserDownloadURL, to: zip)
        } else {
            throw Problem("the release has no \(assetName)")
        }
        let unpacked = work.appendingPathComponent("unpacked")
        try FileManager.default.createDirectory(at: unpacked, withIntermediateDirectories: true)
        try run("/usr/bin/ditto", ["-x", "-k", zip.path, unpacked.path])
        return unpacked.appendingPathComponent(Bundle.main.bundleURL.lastPathComponent)
    }

    /// Downloads the commit's source and runs headroom/scripts/build-app.sh on it (this Mac only).
    nonisolated static func buildFromSource(_ commit: Commit, in work: URL, step: (String) -> Void) throws -> URL {
        guard (try? run("/usr/bin/xcode-select", ["-p"])) != nil else {
            throw Problem("building needs Apple's command line tools: run xcode-select --install in Terminal")
        }
        guard goPath() != nil else {
            throw Problem("building needs Go: run brew install go in Terminal (or install it from go.dev)")
        }
        step("Downloading \(commit.short)…")
        let tarball = work.appendingPathComponent("source.tar.gz")
        if let gh = ghPath(), (try? run(gh, ["api", "repos/\(repo)/tarball/\(commit.sha)"], to: tarball)) != nil {
        } else {
            try download(URL(string: "https://codeload.github.com/\(repo)/tar.gz/\(commit.sha)")!, to: tarball)
        }
        let src = work.appendingPathComponent("source")
        try FileManager.default.createDirectory(at: src, withIntermediateDirectories: true)
        try run("/usr/bin/tar", ["-xzf", tarball.path, "-C", src.path, "--strip-components", "1"])

        step("Building \(commit.short) (a few minutes)…")
        log("building \(commit.short) in \(src.path)")
        try run("/bin/bash", [src.appendingPathComponent("headroom/scripts/build-app.sh").path],
                env: ["VERSION": "\(versionBase)-\(commit.short)", "COMMIT": commit.sha, "BRANCH": branch, "UNIVERSAL": "0"],
                in: src, logOutput: true)
        return src.appendingPathComponent("headroom/build").appendingPathComponent(Bundle.main.bundleURL.lastPathComponent)
    }

    // MARK: GitHub

    struct Problem: LocalizedError {
        let errorDescription: String?
        init(_ message: String) { errorDescription = message }
    }

    /// GET https://api.github.com/<path>
    nonisolated static func api(_ path: String) throws -> Data {
        var ghError: Error?
        if let gh = ghPath() {
            do { return try run(gh, ["api", path]) } catch { ghError = error }
        }
        var req = URLRequest(url: URL(string: "https://api.github.com/\(path)")!)
        req.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        let (data, response) = try fetch(req)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        if status == 404, let ghError { throw ghError }
        if status == 404 { throw Problem("GitHub can't see \(repo): if it's private, sign in with gh auth login") }
        if status == 403 { throw Problem("GitHub's hourly limit: try again later, or sign in with gh auth login") }
        guard status == 200 else { throw Problem("GitHub answered \(status)") }
        return data
    }

    nonisolated static func download(_ url: URL, to file: URL) throws {
        let (data, response) = try fetch(URLRequest(url: url))
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else { throw Problem("download failed (GitHub answered \(status)): \(url)") }
        try data.write(to: file)
    }

    /// gh from Homebrew (apps started from Finder don't get the Terminal's PATH).
    nonisolated static func ghPath() -> String? {
        ["/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/usr/bin/gh"].first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    nonisolated static func goPath() -> String? {
        ["/opt/homebrew/bin/go", "/usr/local/go/bin/go", "/usr/local/bin/go"].first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    // MARK: Helpers

    /// ~/Library/Logs/Headroom/update.log: what updates did, and a build's output.
    nonisolated static var logURL: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/Headroom/update.log")
    }

    nonisolated static func log(_ line: String) {
        let url = logURL
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        let text = "\(ISO8601DateFormatter().string(from: Date())) \(line)\n"
        if let h = try? FileHandle(forWritingTo: url) {
            defer { try? h.close() }
            _ = try? h.seekToEnd()
            try? h.write(contentsOf: Data(text.utf8))
        } else {
            try? Data(text.utf8).write(to: url)
        }
    }

    /// Runs a program and returns its output (or writes it to `to`). Throws with the last line of
    /// its errors if it fails. `logOutput` also copies everything it prints into update.log.
    @discardableResult
    nonisolated static func run(_ tool: String, _ args: [String], env extra: [String: String] = [:],
                                in dir: URL? = nil, to file: URL? = nil, logOutput: Bool = false) throws -> Data {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: tool)
        p.arguments = args
        if let dir { p.currentDirectoryURL = dir }
        var env = ProcessInfo.processInfo.environment
        env["PATH"] = "/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        env["GH_PROMPT_DISABLED"] = "1"
        env.merge(extra) { $1 }
        p.environment = env
        let out = Pipe(), err = Pipe()
        var outFile: FileHandle?
        if let file {
            FileManager.default.createFile(atPath: file.path, contents: nil)
            outFile = try FileHandle(forWritingTo: file)
            p.standardOutput = outFile
        } else {
            p.standardOutput = out
        }
        defer { try? outFile?.close() }
        p.standardError = err
        try p.run()
        // Read both while it runs, so a full pipe can't stall it.
        final class Box: @unchecked Sendable { var data = Data() }
        let errBox = Box()
        let errRead = DispatchGroup()
        errRead.enter()
        DispatchQueue.global().async {
            errBox.data = err.fileHandleForReading.readDataToEndOfFile()
            errRead.leave()
        }
        let data = file == nil ? out.fileHandleForReading.readDataToEndOfFile() : Data()
        p.waitUntilExit()
        errRead.wait()
        let errData = errBox.data
        let name = (tool as NSString).lastPathComponent
        if logOutput {
            log("\(name) \(args.joined(separator: " "))\n\(String(decoding: data, as: UTF8.self))\(String(decoding: errData, as: UTF8.self))")
        }
        if p.terminationStatus != 0 {
            let lines = String(decoding: errData.isEmpty ? data : errData, as: UTF8.self)
                .trimmingCharacters(in: .whitespacesAndNewlines).components(separatedBy: "\n")
            let msg = logOutput ? (lines.last(where: { $0.contains("error") }) ?? lines.last ?? "") : (lines.last ?? "")
            if name == "gh", msg.contains("auth login") || msg.contains("401") {
                throw Problem("gh isn't signed in: run gh auth login in Terminal")
            }
            let tail = msg.isEmpty ? "\(name) failed (\(p.terminationStatus))" : msg
            throw Problem(logOutput ? "\(tail) (details in \(logURL.path))" : tail)
        }
        return data
    }

    nonisolated static func fetch(_ req: URLRequest) throws -> (Data, URLResponse) {
        let sem = DispatchSemaphore(value: 0)
        final class Box: @unchecked Sendable { var result: Result<(Data, URLResponse), Error> = .failure(Problem("no answer")) }
        let box = Box()
        URLSession.shared.dataTask(with: req) { data, response, error in
            if let data, let response { box.result = .success((data, response)) } else { box.result = .failure(error ?? Problem("no answer")) }
            sem.signal()
        }.resume()
        sem.wait()
        return try box.result.get()
    }
}

/// The version, and Update when there's one.
struct UpdateButton: View {
    @ObservedObject var updater = Updater.shared

    var body: some View {
        HStack(spacing: 6) {
            switch updater.state {
            case .available(let u):
                Button {
                    updater.install()
                } label: {
                    Label(u.release.map { "Update to \($0.tag)" } ?? "Update (build here)", systemImage: "arrow.down.circle.fill")
                }
                .buttonStyle(PillButton(tint: Palette.blue, filled: true))
                .help(u.commit.title)
            case .installing(let step):
                ProgressView().controlSize(.small)
                Text(step).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
            case .checking:
                ProgressView().controlSize(.small)
                Text("v\(Updater.version)").font(.system(size: 11)).foregroundStyle(.secondary)
            case .failed(let why):
                Button { updater.check(userInitiated: true) } label: {
                    Label("v\(Updater.version)", systemImage: "exclamationmark.triangle.fill")
                }
                .buttonStyle(.borderless)
                .font(.system(size: 11))
                .foregroundStyle(.orange)
                .help("\(why)\nClick to check again.")
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(why, forType: .string)
                } label: { Image(systemName: "doc.on.doc") }
                    .buttonStyle(.borderless).help("Copy the error")
            case .idle, .upToDate:
                Button { updater.check(userInitiated: true) } label: {
                    HStack(spacing: 4) {
                        Text("v\(Updater.version)")
                        Image(systemName: "arrow.clockwise")
                    }
                }
                .buttonStyle(.borderless)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .help(updater.state == .upToDate ? "Up to date. Click to check again." : "Check for updates")
            }
        }
    }
}
