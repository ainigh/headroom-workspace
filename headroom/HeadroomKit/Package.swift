// swift-tools-version:5.9
import PackageDescription

// HeadroomKit: config, clients, stores, view models, assistant and
// confirmation. Foundation only (plus Yams for YAML), so it builds and
// tests on any machine: swift test --package-path headroom/HeadroomKit
let package = Package(
    name: "HeadroomKit",
    platforms: [.macOS(.v14)],
    products: [.library(name: "HeadroomKit", targets: ["HeadroomKit"])],
    dependencies: [.package(url: "https://github.com/jpsim/Yams.git", exact: "5.4.0")],
    targets: [
        .target(name: "HeadroomKit", dependencies: ["Yams"]),
        .testTarget(name: "HeadroomKitTests", dependencies: ["HeadroomKit"], resources: [.copy("Fixtures")]),
    ]
)
