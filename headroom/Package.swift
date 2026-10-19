// swift-tools-version:5.9
import PackageDescription

// The Headroom menu bar app. HeadroomKit (config, clients, stores, view
// models, assistant, confirmation) is a separate package that builds and
// tests on any machine; the app target (views and lifecycle only) needs
// macOS. scripts/build-app.sh turns the product into Headroom.app with
// sysqd, sysq and sysact inside.
var targets: [Target] = []
#if os(macOS)
targets.append(.executableTarget(
    name: "Headroom",
    dependencies: [.product(name: "HeadroomKit", package: "HeadroomKit")],
    path: "Headroom"
))
#endif

let package = Package(
    name: "Headroom",
    platforms: [.macOS(.v14)],
    dependencies: [.package(path: "HeadroomKit")],
    targets: targets
)
