// swift-tools-version:5.9
import PackageDescription

// HeadroomKit: config, clients, stores, view models, assistant, confirmation.
// Foundation-only so it builds and tests on any machine (swift test).
let package = Package(
    name: "HeadroomKit",
    platforms: [.macOS(.v14)],
    products: [.library(name: "HeadroomKit", targets: ["HeadroomKit"])],
    targets: [
        .target(name: "HeadroomKit"),
        .testTarget(name: "HeadroomKitTests", dependencies: ["HeadroomKit"]),
    ]
)
