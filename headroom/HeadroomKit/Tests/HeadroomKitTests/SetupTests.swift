import XCTest
@testable import HeadroomKit

final class FakeAdmin: SysqAdminProtocol, @unchecked Sendable {
    var statusValue: JSONValue = [:]
    var rechecks = 0
    func status() async throws -> JSONValue { statusValue }
    func status(recheck: Bool) async throws -> JSONValue {
        if recheck { rechecks += 1 }
        return statusValue
    }
    func clients() async throws -> [APIClientInfo] { [] }
    func pending() async throws -> [PendingPairing] { [] }
    func approve(id: String, code: String, privacy: String, allowScripts: Bool, origins: [String]) async throws {}
    func upsert(_ client: HostClient) async throws -> String { "token" }
    func revoke(_ id: String) async throws {}
    func setAPI(_ api: APISettings) async throws {}
    func setState(viewOpen: Bool, onBattery: Bool?) async throws {}
    func putSchedules(_ yaml: String) async throws {}
    func reload() async throws {}
}

@MainActor
final class SetupTests: XCTestCase {
    func testSudoRulesAndSudoersLine() async {
        let admin = FakeAdmin()
        admin.statusValue = [
            "sudo": ["ane.activity": true, "thermal.pressure": false],
            "sudo_rules": [
                "ane.activity": "/usr/bin/powermetrics --samplers cpu_power,gpu_power,ane_power -n 1 -i 1000",
                "thermal.pressure": "/usr/bin/powermetrics --samplers thermal -n 1 -i 1000",
            ],
            "network_primitives": ["ai.ollama_ps"],
        ]
        let s = SetupStore(admin: admin)
        await s.refresh(recheck: true)
        XCTAssertEqual(admin.rechecks, 1)
        XCTAssertEqual(s.sudo.map(\.primitive), ["ane.activity", "thermal.pressure"])
        XCTAssertFalse(s.sudoReady)
        XCTAssertEqual(s.networkPrimitives, ["ai.ollama_ps"])
        // Only the missing rule, by default.
        XCTAssertEqual(s.sudoersLine(user: "me"), "me ALL=(root) NOPASSWD: /usr/bin/powermetrics --samplers thermal -n 1 -i 1000")
        // Commas inside a command are escaped for sudoers.
        XCTAssertEqual(s.sudoersLine(user: "me", all: true),
                       "me ALL=(root) NOPASSWD: /usr/bin/powermetrics --samplers cpu_power\\,gpu_power\\,ane_power -n 1 -i 1000, /usr/bin/powermetrics --samplers thermal -n 1 -i 1000")
    }

    func testAllRulesInPlace() async {
        let admin = FakeAdmin()
        admin.statusValue = ["sudo": ["thermal.pressure": true], "sudo_rules": ["thermal.pressure": "/usr/bin/powermetrics -n 1"]]
        let s = SetupStore(admin: admin)
        await s.refresh()
        XCTAssertTrue(s.sudoReady)
        XCTAssertNil(s.sudoersLine(user: "me"))
        XCTAssertTrue(s.loaded)
        XCTAssertNil(s.error)
    }
}
