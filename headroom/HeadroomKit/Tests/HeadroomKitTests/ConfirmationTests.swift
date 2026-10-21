import XCTest
@testable import HeadroomKit

/// The mutation path's guarantees (implementation.md §5.4).
final class ConfirmationTests: XCTestCase {
    let repoHeadroom = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    /// Only ConfirmationFlow.swift constructs a ConfirmationToken, and its
    /// initializer is fileprivate. (scripts/check-confirmation-token.sh also
    /// proves another module can't compile a construction.)
    func testTokenIsConstructedOnlyInConfirmationFlow() throws {
        var offenders: [String] = []
        let fm = FileManager.default
        for dir in ["HeadroomKit/Sources", "HeadroomKit/Tests", "Headroom"] {
            guard let e = fm.enumerator(atPath: repoHeadroom.appendingPathComponent(dir).path) else { continue }
            for case let rel as String in e where rel.hasSuffix(".swift") {
                let path = repoHeadroom.appendingPathComponent(dir).appendingPathComponent(rel)
                let text = try String(contentsOf: path, encoding: .utf8)
                if path.lastPathComponent == "ConfirmationFlow.swift" { continue }
                if path.lastPathComponent == "ConfirmationTests.swift" { continue }
                if text.range(of: "ConfirmationToken\\s*\\(", options: .regularExpression) != nil { offenders.append(rel) }
                if text.contains("extension ConfirmationToken") { offenders.append(rel + " (extension)") }
            }
        }
        XCTAssertEqual(offenders, [])
        let flow = try String(contentsOf: repoHeadroom.appendingPathComponent("HeadroomKit/Sources/HeadroomKit/Confirmation/ConfirmationFlow.swift"), encoding: .utf8)
        let inits = flow.components(separatedBy: "\n").filter { $0.contains(" init(") && flow.contains("struct ConfirmationToken") }
        XCTAssertTrue(flow.contains("    fileprivate init(plan: ActionPlan, via: String, at: Date)"))
        XCTAssertFalse(inits.contains { $0.contains("public init(plan") || $0.contains("internal init(plan") })
        // Exactly one construction site: confirm().
        XCTAssertEqual(flow.components(separatedBy: "ConfirmationToken(plan:").count - 1, 1)
    }

    func plan() -> ActionPlan {
        ActionPlan(plan_id: "p1", plan_hash: "abc", name: "ai.ollama_unload", doc: "", tier: "caution", params: ["model": "gemma-27b"],
                   effect: "Unloads gemma-27b", undo_hint: nil, requires_admin: false, command: "", expires_at: "")
    }

    @MainActor
    func testConfirmIsIgnoredUntilTheDelayPasses() async {
        let clock = TestClock()
        let flow = ConfirmationFlow(delay: 2, now: { clock.now })
        let pending = Task { await flow.request(self.plan(), requestedBy: .assistant, explanation: "idle 40 min") }
        let shown = await waitUntil { flow.current != nil }
        XCTAssertTrue(shown)
        XCTAssertEqual(flow.current?.explanation, "idle 40 min")
        XCTAssertFalse(flow.confirmEnabled)
        clock.now = clock.now.addingTimeInterval(1.5)
        flow.confirm() // too early: nothing happens
        XCTAssertNotNil(flow.current)
        // A second request while one is showing is refused, not queued.
        let second = await flow.request(plan(), requestedBy: .user)
        XCTAssertNil(second)
        clock.now = clock.now.addingTimeInterval(1)
        flow.confirm()
        let token = await pending.value
        XCTAssertEqual(token?.planID, "p1")
        XCTAssertEqual(token?.planHash, "abc")
        XCTAssertEqual(token?.via, "headroom-assistant")
        XCTAssertEqual(token?.nonce.count, 32)
        XCTAssertNil(flow.current)
    }

    @MainActor
    func testDeclineReturnsNoToken() async {
        let flow = ConfirmationFlow(delay: 1)
        let pending = Task { await flow.request(self.plan(), requestedBy: .user) }
        _ = await waitUntil { flow.current != nil }
        flow.decline()
        let token = await pending.value
        XCTAssertNil(token)
    }

    @MainActor
    func testDelayHasAFloorOfOneSecond() {
        XCTAssertEqual(ConfirmationFlow(delay: 0).delay, 1)
    }

    /// Same vector as sysact's Go confirm.Sign (key bytes 0...31).
    func testSignatureMatchesSysact() throws {
        let canonical = HostKeySigner.canonical(planID: "p1", planHash: "abc", confirmedAt: Date(timeIntervalSince1970: 1_792_411_200),
                                                nonce: "n1", via: "headroom-ui")
        XCTAssertEqual(canonical, "sysact-confirm-v1\np1\nabc\n2026-10-19T12:00:00Z\nn1\nheadroom-ui")
        let key = (0..<32).map { UInt8($0) }
        XCTAssertEqual(SHA256.hex(SHA256.hmac(key: key, message: Array(canonical.utf8))),
                       "fe6744d03a01447630901ae91a829f594dc98b32bb871ca991de5cc9ad2dddd5")
    }

    /// The whole UI path: prepare → sheet → confirm → execute with the token.
    @MainActor
    func testActionStoreRunsOnlyAfterConfirmation() async {
        let sysact = FakeSysact()
        sysact.catalogList = [action("ai.ollama_unload", params: ["model": spec("str")])]
        let clock = TestClock()
        let flow = ConfirmationFlow(delay: 2, now: { clock.now })
        let store = ActionStore(client: sysact, flow: flow)
        await store.loadCatalog()
        let declined = Task { await store.run("ai.ollama_unload", params: ["model": "gemma-27b"], requestedBy: .user) }
        _ = await waitUntil { flow.current != nil }
        flow.decline()
        let d = await declined.value
        XCTAssertFalse(d.approved)
        XCTAssertTrue(sysact.executed.isEmpty)

        let ran = Task { await store.run("ai.ollama_unload", params: ["model": "gemma-27b"], requestedBy: .user) }
        _ = await waitUntil { flow.current != nil }
        clock.now = clock.now.addingTimeInterval(3)
        flow.confirm()
        let r = await ran.value
        XCTAssertTrue(r.approved && r.ok)
        XCTAssertEqual(sysact.executed.count, 1)
        XCTAssertEqual(sysact.executed[0].1.planID, "plan-1")
        XCTAssertEqual(sysact.executed[0].1.via, "headroom-ui")
    }

    /// A second request while a sheet is up is "not shown", never a decline.
    @MainActor
    func testRequestWhileASheetIsOpenIsNotADecline() async {
        let sysact = FakeSysact()
        sysact.catalogList = [action("ai.ollama_unload", params: ["model": spec("str")])]
        let flow = ConfirmationFlow(delay: 2)
        let store = ActionStore(client: sysact, flow: flow)
        await store.loadCatalog()
        let first = Task { await store.run("ai.ollama_unload", params: ["model": "a"], requestedBy: .user) }
        _ = await waitUntil { flow.current != nil }
        let second = await store.run("ai.ollama_unload", params: ["model": "b"], requestedBy: .assistant)
        XCTAssertTrue(second.notShown)
        XCTAssertFalse(second.approved)
        XCTAssertEqual(flow.current?.plan.params["model"], "a")
        flow.decline()
        let d = await first.value
        XCTAssertFalse(d.notShown)
    }

    @MainActor
    func testActionsForAnItemArePrefilled() async {
        let sysact = FakeSysact()
        sysact.catalogList = [action("ai.ollama_unload", params: ["model": spec("str")]),
                              action("app.force_quit", tier: "risky", params: ["pid": spec("pid")]),
                              action("fs.trash", tier: "risky", params: ["path": spec("path")])]
        let store = ActionStore(client: sysact, flow: ConfirmationFlow())
        await store.loadCatalog()
        let a = store.actions(for: ItemRef(kind: "process", id: "2210", name: "ollama"))
        XCTAssertEqual(a.map(\.0.name), ["app.force_quit"])
        XCTAssertEqual(a.first?.1, ["pid": "2210"])
        let m = store.actions(for: ItemRef(kind: "model", id: "gemma-27b", name: "gemma-27b"))
        XCTAssertEqual(m.map(\.0.name), ["ai.ollama_unload"])
    }
}
