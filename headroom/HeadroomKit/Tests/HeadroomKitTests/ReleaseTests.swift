import XCTest
@testable import HeadroomKit

final class ReleaseTests: XCTestCase {
    func testDecodesRelease() throws {
        let json = """
        {"tag_name": "v0.1.42", "target_commitish": "0123456789abcdef0123456789abcdef01234567",
         "body": "M6: views", "html_url": "https://github.com/ainigh/headroom-workspace/releases/tag/v0.1.42",
         "assets": [{"name": "Headroom.zip", "browser_download_url": "https://github.com/ainigh/headroom-workspace/releases/download/v0.1.42/Headroom.zip", "size": 1}],
         "draft": false}
        """
        let r = try Release.decode(Data(json.utf8))
        XCTAssertEqual(r.tag, "v0.1.42")
        XCTAssertEqual(r.target, "0123456789abcdef0123456789abcdef01234567")
        XCTAssertEqual(r.notes, "M6: views")
        XCTAssertEqual(r.asset(named: "Headroom.zip")?.browserDownloadURL.lastPathComponent, "Headroom.zip")
        XCTAssertNil(r.asset(named: "Other.zip"))
    }

    func testDecodesReleaseWithoutOptionalFields() throws {
        let r = try Release.decode(Data(#"{"tag_name": "v1", "assets": []}"#.utf8))
        XCTAssertNil(r.target)
        XCTAssertNil(r.page)
        XCTAssertTrue(r.assets.isEmpty)
    }

    func testDecodesCommit() throws {
        let json = """
        {"sha": "abcdef0123456789abcdef0123456789abcdef01",
         "commit": {"message": "M5: sysact\\n\\nDetails here", "author": {"name": "x"}}}
        """
        let c = try Commit.decode(Data(json.utf8))
        XCTAssertEqual(c.short, "abcdef0")
        XCTAssertEqual(c.title, "M5: sysact")
        XCTAssertEqual(Commit(sha: "1234567890", message: "one line").title, "one line")
    }

    func testRejectsMalformed() {
        XCTAssertThrowsError(try Commit.decode(Data(#"{"sha": "x"}"#.utf8)))
        XCTAssertThrowsError(try Release.decode(Data("[]".utf8)))
    }
}
