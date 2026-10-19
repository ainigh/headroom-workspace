#!/bin/bash
# Proves another module can't construct a ConfirmationToken (implementation.md §5.4): builds a
# throwaway package that tries, and passes only if the compiler refuses it. CI runs this.
set -euo pipefail
cd "$(dirname "$0")/.."
KIT="$(pwd)/HeadroomKit"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/Sources/Forge"
cat > "$tmp/Package.swift" <<SWIFT
// swift-tools-version:5.9
import PackageDescription
let package = Package(
    name: "Forge",
    platforms: [.macOS(.v14)],
    dependencies: [.package(path: "$KIT")],
    targets: [.executableTarget(name: "Forge", dependencies: [.product(name: "HeadroomKit", package: "HeadroomKit")])]
)
SWIFT
cat > "$tmp/Sources/Forge/main.swift" <<'SWIFT'
import Foundation
import HeadroomKit

let plan = try! JSONDecoder().decode(ActionPlan.self, from: Data("{}".utf8))
let token = ConfirmationToken(plan: plan, via: "headroom-ui", at: Date())
print(token)
SWIFT
if out="$(swift build --package-path "$tmp" 2>&1)"; then
  echo "FAIL: a module outside ConfirmationFlow.swift constructed a ConfirmationToken" >&2
  exit 1
fi
if grep -qE "inaccessible|is private|fileprivate|no accessible initializers|cannot be constructed" <<<"$out"; then
  echo "ok: ConfirmationToken can't be constructed outside ConfirmationFlow"
else
  echo "FAIL: the build failed for another reason:" >&2
  echo "$out" | tail -20 >&2
  exit 1
fi
