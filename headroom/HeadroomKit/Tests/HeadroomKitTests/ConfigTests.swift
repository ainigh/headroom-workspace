import XCTest
@testable import HeadroomKit

/// The repo's shipped config directory.
let shippedConfig = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    .appendingPathComponent("config")

func tempDir() -> URL {
    let u = FileManager.default.temporaryDirectory.appendingPathComponent("hk-\(UUID().uuidString)")
    try? FileManager.default.createDirectory(at: u, withIntermediateDirectories: true)
    return u
}

final class ConfigTests: XCTestCase {
    func testShippedConfigLoadsClean() throws {
        let loader = ConfigLoader(userDir: tempDir(), defaultsDir: shippedConfig)
        try loader.bootstrap()
        let (c, problems) = loader.load(previous: nil)
        XCTAssertEqual(problems, [], problems.map(\.text).joined(separator: "\n"))
        XCTAssertEqual(c.groups.groups.count, 8)
        XCTAssertEqual(c.modeOrder, ["view", "optimize", "deep_dive", "repair"])
        XCTAssertEqual(c.thresholds["storage"]?.amber.value, 50e9)
        XCTAssertEqual(c.thresholds["network"]?.red.value, 100e6)
        XCTAssertFalse(c.thresholds["storage"]!.higherIsWorse)
        XCTAssertTrue(c.systemPrompt.contains("SUMMARY"))
        XCTAssertTrue(c.requestTemplate.contains("{{question}}"))
        XCTAssertEqual(c.assistant.maxSensitivity, "personal")
        XCTAssertEqual(c.notifications.levels(for: "network"), [])
        XCTAssertEqual(c.notifications.levels(for: "battery"), ["amber", "red"])
        XCTAssertEqual(c.group("battery")?.metrics?.first?.label, "on")
    }

    func testInvalidFileKeepsLastGoodAndReportsLine() throws {
        let dir = tempDir()
        let loader = ConfigLoader(userDir: dir, defaultsDir: shippedConfig)
        try loader.bootstrap()
        let (good, _) = loader.load(previous: nil)
        let bad = "version: 1\nrates:\n  view_open: { headline: fast }\n  idle: {}\n"
        try bad.write(to: dir.appendingPathComponent("sampling.yaml"), atomically: true, encoding: .utf8)
        let (c, problems) = loader.load(previous: good)
        XCTAssertEqual(problems.count, 1)
        XCTAssertEqual(problems.first?.file, "sampling.yaml")
        XCTAssertEqual(problems.first?.line, 3)
        XCTAssertEqual(c.sampling.rates.view_open.headline, good.sampling.rates.view_open.headline)
        // Broken YAML syntax is reported, not thrown.
        try "groups: [\n".write(to: dir.appendingPathComponent("groups.yaml"), atomically: true, encoding: .utf8)
        let (c2, p2) = loader.load(previous: c)
        XCTAssertEqual(c2.groups.groups.count, 8)
        XCTAssertTrue(p2.contains { $0.file == "groups.yaml" })
    }

    func testSaveValidatesFirst() throws {
        let dir = tempDir()
        let loader = ConfigLoader(userDir: dir, defaultsDir: shippedConfig)
        try loader.bootstrap()
        XCTAssertNotNil(loader.save("layout", text: "version: 1\nkey_groups: { mode: sideways }\n"))
        XCTAssertNil(loader.save("layout", text: "version: 1\nkey_groups: { mode: auto, auto_hold_minutes: 3 }\n"))
        XCTAssertEqual(loader.load(previous: nil).0.layout.keyMode, "auto")
    }

    func testAddingAGroupNeedsNoCode() throws {
        let dir = tempDir()
        let loader = ConfigLoader(userDir: dir, defaultsDir: shippedConfig)
        try loader.bootstrap()
        var text = loader.text(of: "groups")
        text += """
          - id: disk_io
            name: Disk I/O
            symbol: arrow.up.arrow.down
            headline:  { recipe: disk.activity, column: mb_per_s }
            detail:    { recipe: disk.io }
            consumers: { recipe: proc.top, value: cpu }
            sparkline: { recipe: disk.activity, column: mb_per_s, range: 5m }
            attention: attention.disk_io

        """
        XCTAssertNil(loader.save("groups", text: text))
        let (c, _) = loader.load(previous: nil)
        XCTAssertEqual(c.groups.groups.last?.name, "Disk I/O")
    }

    func testUnits() {
        XCTAssertEqual(Units.parse("50GB"), 50e9)
        XCTAssertEqual(Units.parse("1.5 GiB"), 1.5 * 1_073_741_824)
        XCTAssertEqual(Units.parse("50MB/s"), 50e6)
        XCTAssertEqual(Units.parse("5m"), 300)
        XCTAssertEqual(Units.parse("90s"), 90)
        XCTAssertEqual(Units.bytes(18_200_000_000), "18.2 GB")
        XCTAssertEqual(Units.bytes(995_000_000_000), "995 GB")
        XCTAssertEqual(Units.duration(7800), "2 h 10 min")
    }
}
