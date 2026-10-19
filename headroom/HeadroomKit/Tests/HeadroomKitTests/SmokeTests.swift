import XCTest
@testable import HeadroomKit

final class SmokeTests: XCTestCase {
    func testVersion() { XCTAssertFalse(HeadroomKit.version.isEmpty) }
}
