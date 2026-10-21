import XCTest
import Yams
@testable import HeadroomKit

final class GroupStoreTests: XCTestCase {
    func sysq() -> FakeSysq {
        let s = FakeSysq()
        s.set("mem.headline", table([("available", "bytes"), ("pressure", "str"), ("swap_used", "bytes")], [[18.2e9, "normal", 2.1e9]]))
        s.set("attention.memory", table([("status", "str"), ("message", "str")], [["amber", "Memory pressure 72%"]]))
        s.set("proc.top", table([("pid", "pid"), ("command", "str"), ("mem", "bytes"), ("cpu", "float")], [
            [2210, "ollama", 22e9, 0.5], [301, "Google Chrome", 5.1e9, 12], [77, "Xcode", 3.8e9, 3], [12, "Finder", 0.2e9, 0],
        ], unit: ["cpu": "%"]))
        s.set("cpu.headline", table([("load_pct", "float"), ("load1", "float")], [[34, 4.2]], unit: ["load_pct": "%"]))
        return s
    }

    @MainActor
    func testFeedsAreSharedAndFollowRates() async {
        let c = loadShipped()
        let s = sysq()
        let store = GroupStore(config: c)
        store.attach(s)
        _ = await waitUntil { store.state("memory")?.headline != nil && store.state("memory")?.attention != nil }
        // mem.headline backs the headline and two metrics: one feed.
        XCTAssertEqual(s.calls.filter { $0.hasPrefix("subscribe mem.headline ") }.count, 1)
        // Idle: headline 30s; attention gets the thresholds as params.
        XCTAssertTrue(s.calls.contains("subscribe mem.headline every 30"))
        XCTAssertTrue(s.calls.contains("subscribe attention.memory {\"amber\":60,\"direction\":\"up\",\"red\":85} every 60"), s.calls.joined(separator: "\n"))
        XCTAssertTrue(s.calls.contains("subscribe attention.storage {\"amber\":50000000000,\"direction\":\"down\",\"red\":20000000000} every 60"))
        // Detail only while a view is open.
        XCTAssertFalse(s.calls.contains { $0.hasPrefix("subscribe mem.detail") })
        store.setViewOpen(true)
        _ = await waitUntil { s.calls.contains("subscribe mem.headline every 2") }
        XCTAssertTrue(s.calls.contains("subscribe mem.headline every 2"))
        XCTAssertTrue(s.calls.contains { $0.hasPrefix("subscribe mem.detail every 5") })

        let st = store.state("memory")!
        XCTAssertEqual(st.status, .amber)
        let vm = ViewModels.group(c.group("memory")!, state: st, config: c, mode: "view", isKey: true, consumerLimit: 3)
        XCTAssertEqual(vm.headline, Units.bytes(18.2e9))
        XCTAssertEqual(vm.headlineLabel, "available")
        XCTAssertEqual(vm.metrics.map(\.label), ["pressure", "swap"])
        XCTAssertEqual(vm.metrics.map(\.text), ["normal", Units.bytes(2.1e9)])
        XCTAssertEqual(vm.consumers.map(\.name), ["ollama", "Google Chrome", "Xcode"])
        XCTAssertEqual(vm.consumers.first?.fraction, 1)
        XCTAssertEqual(vm.consumers.first?.itemID, "2210")
        XCTAssertEqual(vm.consumers.first?.kind, "process")
        XCTAssertEqual(vm.statusMessage, "Memory pressure 72%")
        XCTAssertTrue(vm.accessibilityLabel.hasPrefix("Memory, \(Units.bytes(18.2e9)) available, status amber"))
        // Sparkline: history first.
        _ = await waitUntil { (store.state("memory")?.sparkline.count ?? 0) >= 2 }
        XCTAssertEqual(Array(store.state("memory")!.sparkline.prefix(2)), [1, 2])
    }

    @MainActor
    func testFallsBackToPollingWhenSubscribeFails() async {
        let c = loadShipped()
        let s = sysq()
        s.subscribeFails = true
        let store = GroupStore(config: c)
        store.attach(s)
        _ = await waitUntil { store.state("cpu")?.headline != nil }
        XCTAssertTrue(s.calls.contains("run cpu.headline"))
        let vm = ViewModels.group(c.group("cpu")!, state: store.state("cpu"), config: c, mode: "view", isKey: true, consumerLimit: 3)
        XCTAssertEqual(vm.headline, Units.percent(34))
    }

    @MainActor
    func testPausedStopsFeeds() async {
        let store = GroupStore(config: loadShipped())
        let s = sysq()
        store.setPaused(true)
        store.attach(s)
        try? await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertTrue(s.calls.isEmpty)
    }
}

final class HistoryStoreTests: XCTestCase {
    /// Switching the range while a slow load is in flight: the slow, older
    /// load must not overwrite the newer range's series when it lands.
    @MainActor
    func testOlderLoadDoesNotOverwriteNewerRange() async {
        let c = loadShipped()
        let s = FakeSysq()
        s.historyDelay = ["24h": 300_000_000]
        s.historyValue = ["24h": 24, "1h": 1]
        let store = HistoryStore()
        store.attach(s)
        let g = c.group("memory")!
        let slow = Task { await store.load([g], range: "24h") }
        try? await Task.sleep(nanoseconds: 50_000_000)
        await store.load([g], range: "1h")
        XCTAssertEqual(store.series["memory"]?.points.map(\.1), [1])
        XCTAssertFalse(store.loading)
        await slow.value
        XCTAssertEqual(store.series["memory"]?.points.map(\.1), [1])
        XCTAssertFalse(store.loading)
    }
}

final class ViewModelTests: XCTestCase {
    func vm(_ id: String, _ status: GroupStatus, reclaim: Double? = nil) -> GroupViewModel {
        GroupViewModel(id: id, name: id, symbol: "", headline: "", headlineLabel: nil, status: status, statusMessage: nil, sparkline: [],
                       metrics: [], consumers: [], reclaimable: nil, reclaimableValue: reclaim, unavailable: nil, updated: nil, isKey: false, emphasis: nil)
    }

    func testOptimizeRanksByReclaimable() {
        let mode = ModeViewModel(id: "optimize", label: "Optimize", question: "", tint: nil, rankBy: "reclaimable", historyRange: nil, consumerCount: nil)
        let out = ViewModels.ordered([vm("a", .green, reclaim: 1), vm("b", .green), vm("c", .green, reclaim: 24e9)], mode: mode)
        XCTAssertEqual(out.map(\.id), ["c", "a", "b"])
        XCTAssertEqual(ViewModels.ordered([vm("a", .green, reclaim: 1), vm("c", .green, reclaim: 24e9)], mode: nil).map(\.id), ["a", "c"])
    }

    func testPinnedKeyGroups() {
        var sel = KeyGroupSelector()
        let c = loadShipped()
        XCTAssertEqual(sel.select(c, statuses: ["thermal": .red]), ["memory", "cpu", "gpu", "storage"])
    }

    func testAutoKeyGroupsHoldTheirSlot() {
        var c = loadShipped()
        c.layout.key_groups?.mode = "auto"
        c.layout.key_groups?.auto_hold_minutes = 5
        var sel = KeyGroupSelector()
        let t0 = Date(timeIntervalSince1970: 0)
        let calm = Dictionary(uniqueKeysWithValues: c.groups.groups.map { ($0.id, GroupStatus.green) })
        XCTAssertEqual(sel.select(c, statuses: calm, now: t0), ["memory", "cpu", "gpu", "storage"])
        var hot = calm
        hot["thermal"] = .red
        let s1 = sel.select(c, statuses: hot, now: t0.addingTimeInterval(10))
        XCTAssertEqual(s1, ["memory", "cpu", "gpu", "thermal"], "a red group takes the last pinned-order slot")
        // Calms down: keeps its slot until the hold time passes.
        XCTAssertEqual(sel.select(c, statuses: calm, now: t0.addingTimeInterval(200)), ["memory", "cpu", "gpu", "thermal"])
        XCTAssertEqual(sel.select(c, statuses: calm, now: t0.addingTimeInterval(400)), ["memory", "cpu", "gpu", "storage"])
    }

    func testReadout() {
        let c = loadShipped()
        var m = vm("memory", .amber)
        m.headline = "18 GB"
        XCTAssertEqual(ViewModels.readout(c, vms: [m, vm("cpu", .red)])?.text, "18 GB")
    }

    func testLayoutEditorKeepsComments() {
        let text = "version: 1\nkey_groups:\n  mode: pinned          # pinned | auto\n  pinned: [memory, cpu, gpu, storage]\n  auto_hold_minutes: 5\ngroup_order: [memory, cpu]\n"
        let e = LayoutEditor(text: text)
        XCTAssertEqual(e.pinnedText(["cpu", "memory", "network", "gpu", "extra"]),
                       "version: 1\nkey_groups:\n  mode: pinned          # pinned | auto\n  pinned: [cpu, memory, network, gpu]\n  auto_hold_minutes: 5\ngroup_order: [memory, cpu]\n")
        XCTAssertEqual(e.orderText(["cpu", "memory"])?.contains("group_order: [cpu, memory]\n"), true)
        let flow = LayoutEditor(text: "version: 1\npanel: { always_on_top: false, remember_position: true }\n")
        XCTAssertEqual(flow.flagText("always_on_top", true, under: "panel"), "version: 1\npanel: { always_on_top: true, remember_position: true }\n")
        XCTAssertEqual(LayoutEditor(text: "version: 1\n").flagText("always_on_top", true, under: "panel"), "version: 1\n\npanel:\n  always_on_top: true")
    }

    func testCLIText() {
        XCTAssertEqual(RecipeStore.cli("disk.hogs", params: ["root": "~/Library", "min_size": "1GB"]), "sysq disk hogs --min-size 1GB --root ~/Library")
        XCTAssertEqual(RecipeStore.cli("disk.recent_big", params: ["root": "/Users/me/My Files", "all": true]), "sysq disk recent-big --all --root '/Users/me/My Files'")
    }
}

final class ScheduleTests: XCTestCase {
    func testSchedulesFromGroups() throws {
        let c = loadShipped()
        let text = ScheduleWriter.yaml(c)
        let doc = try XCTUnwrap(try Yams.load(yaml: text) as? [String: Any])
        XCTAssertEqual(doc["version"] as? Int, 1)
        let sch = try XCTUnwrap(doc["schedules"] as? [[String: Any]])
        let mem = try XCTUnwrap(sch.first { $0["recipe"] as? String == "mem.headline" && $0["name"] == nil })
        XCTAssertEqual(mem["every"] as? String, "2s")
        XCTAssertEqual(mem["idle_every"] as? String, "30s")
        XCTAssertEqual(mem["battery_every"] as? String, "15s")
        let cons = try XCTUnwrap(sch.first { $0["name"] as? String == "consumers.memory" })
        XCTAssertEqual(cons["recipe"] as? String, "proc.top")
        XCTAssertEqual(cons["keep_table"] as? Bool, true)
        XCTAssertEqual(cons["key"] as? String, "pid")
        XCTAssertEqual((cons["params"] as? [String: Any])?["by"] as? String, "mem")
        XCTAssertEqual(sch.filter { $0["recipe"] as? String == "mem.headline" }.count, 1)
        XCTAssertEqual((doc["narrative"] as? [Any])?.count, 8)
        XCTAssertEqual((doc["events"] as? [Any])?.count, 4)
        // Every name/recipe matches sysqd's schema patterns.
        for s in sch {
            XCTAssertNotNil((s["recipe"] as? String)?.range(of: "^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$", options: .regularExpression))
            if let n = s["name"] as? String { XCTAssertNotNil(n.range(of: "^[a-z][a-z0-9_.]*$", options: .regularExpression), n) }
        }
    }

    func testHiddenGroupsSampleSlowly() throws {
        var c = loadShipped()
        c.layout.hidden_groups = ["thermal"]
        let doc = ScheduleWriter.document(c)
        let t = doc["schedules"]?.array?.first { $0["recipe"]?.string == "thermal.headline" }
        XCTAssertEqual(t?["every"]?.string, "300s")
        XCTAssertEqual(t?["idle_every"]?.string, "300s")
    }
}

final class Recorder: Notifier, @unchecked Sendable {
    var posted: [String] = []
    func post(id: String, title: String, body: String) { posted.append(title) }
}

final class NotificationTests: XCTestCase {
    func vm(_ id: String, _ s: GroupStatus) -> GroupViewModel {
        GroupViewModel(id: id, name: id.capitalized, symbol: "", headline: "1", headlineLabel: nil, status: s, statusMessage: "msg", sparkline: [],
                       metrics: [], consumers: [], reclaimable: nil, reclaimableValue: nil, unavailable: nil, updated: nil, isKey: true, emphasis: nil)
    }

    @MainActor
    func testLevelsCooldownAndQuietHours() async {
        let c = loadShipped().notifications
        let r = Recorder()
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        let n = NotificationStore(notifier: r, calendar: cal)
        let noon = Date(timeIntervalSince1970: 1_792_411_200)
        n.observe([vm("memory", .green), vm("battery", .green), vm("network", .green)], config: c, now: noon)
        n.observe([vm("memory", .amber), vm("battery", .amber), vm("network", .red)], config: c, now: noon)
        XCTAssertEqual(r.posted, ["Battery: amber"], "memory notifies on red only; network is off")
        n.observe([vm("memory", .red), vm("battery", .red), vm("network", .red)], config: c, now: noon.addingTimeInterval(60))
        XCTAssertEqual(r.posted, ["Battery: amber", "Memory: red"], "battery is in its cooldown")
        n.observe([vm("memory", .green)], config: c, now: noon.addingTimeInterval(120))
        n.observe([vm("memory", .red)], config: c, now: noon.addingTimeInterval(11 * 3600)) // 23:00: quiet hours
        XCTAssertEqual(r.posted.count, 2)
        n.assistant(status: "WARNING", line: "Idle model", config: c, now: noon)
        n.assistant(status: "OK", line: "fine", config: c, now: noon)
        XCTAssertEqual(r.posted.last, "Headroom assistant: Warning")
        XCTAssertEqual(r.posted.count, 3)
    }
}

final class ExplorerTests: XCTestCase {
    func testTreemapFillsTheBoxProportionally() {
        let rects = Treemap.layout([6, 6, 4, 3, 2, 2, 1, 0], width: 600, height: 400)
        XCTAssertEqual(rects.count, 7, "zero-size items are left out")
        let area = rects.reduce(0) { $0 + $1.w * $1.h }
        XCTAssertEqual(area, 240_000, accuracy: 0.5)
        let first = rects.first { $0.index == 0 }!
        XCTAssertEqual(first.w * first.h, 240_000 * 6 / 24, accuracy: 0.5)
        for r in rects {
            XCTAssertGreaterThanOrEqual(r.x, -0.001); XCTAssertGreaterThanOrEqual(r.y, -0.001)
            XCTAssertLessThanOrEqual(r.x + r.w, 600.001); XCTAssertLessThanOrEqual(r.y + r.h, 400.001)
        }
        XCTAssertTrue(Treemap.layout([], width: 10, height: 10).isEmpty)
    }

    func testBindingParamsAndItems() {
        let c = loadShipped()
        let files = c.groups.explorer!.files!
        XCTAssertEqual(files.params(root: "~/Downloads", minBytes: 1e9, ageSeconds: 86400),
                       ["root": "~/Downloads", "min": 1e9, "since": "86400s", "limit": 100])
        let t = table([("path", "path"), ("size", "bytes"), ("modified", "ts")], [["/Users/me/Downloads/big.dmg", 4e9, "2026-10-01T00:00:00Z"]])
        let items = ExplorerStore.items(t)
        XCTAssertEqual(items.first?.name, "big.dmg")
        XCTAssertEqual(items.first?.size, 4e9)
    }

    /// Switching to a view groups.yaml doesn't configure while a scan runs:
    /// the cancelled scan never clears `scanning`, so the spinner stuck.
    @MainActor
    func testUnconfiguredViewStopsTheSpinnerAndClearsTheOldListing() async {
        var ec = loadShipped().groups.explorer!
        ec.files = nil
        let s = FakeSysq()
        s.set("disk.hogs", table([("path", "path"), ("size", "bytes")], [["/Users/me/Library", 9e9]]))
        let ex = ExplorerStore()
        ex.attach(s, config: ec)
        ex.refresh()
        XCTAssertTrue(ex.scanning)
        ex.view = .files
        ex.refresh()
        XCTAssertNotNil(ex.error)
        XCTAssertFalse(ex.scanning)
        try? await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertFalse(ex.scanning)
        XCTAssertTrue(ex.items.isEmpty)
    }
}
