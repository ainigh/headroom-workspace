#!/bin/bash
# Builds build/Headroom.app and build/Headroom.zip (what a release carries), with sysqd, sysq and
# sysact inside. Needs a Mac with Xcode or its Command Line Tools, and Go. GitHub Actions runs it,
# so nobody has to build anything by hand.
#
#   VERSION=0.1.7 headroom/scripts/build-app.sh
#
# The app's "Update" runs it too, on your Mac, when GitHub hasn't built that commit (UNIVERSAL=0:
# just this Mac's chip, which works with only the Command Line Tools).
set -euo pipefail
cd "$(dirname "$0")/.."          # headroom/
HEADROOM="$(pwd)"
ROOT="$(cd .. && pwd)"           # repo root: sysq/, sysact/, headroom/

VERSION="${VERSION:-0.0.0-dev}"
COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)}"
# The branch the app's updater follows.
BRANCH="${BRANCH:-main}"
NAME=Headroom
APP="build/$NAME.app"

# Swift 5.9 or newer. When the Swift on the PATH is older (old Command Line Tools) and a newer
# Xcode is installed, build with that Xcode instead.
swift_version() { "$@" --version 2>/dev/null | sed -nE 's/.*Swift version ([0-9]+)\.([0-9]+).*/\1 \2/p' | head -1; }
swift_ok() { # major minor
  [[ -n "${1:-}" ]] && { [[ "$1" -gt 5 ]] || { [[ "$1" -eq 5 ]] && [[ "${2:-0}" -ge 9 ]]; }; }
}
read -r major minor <<<"$(swift_version swift || true)" || true
if ! swift_ok "${major:-}" "${minor:-}"; then
  for dev in /Applications/Xcode*.app/Contents/Developer "$HOME"/Applications/Xcode*.app/Contents/Developer; do
    [[ -d "$dev" ]] || continue
    read -r m n <<<"$(DEVELOPER_DIR="$dev" swift_version xcrun swift || true)" || true
    if swift_ok "${m:-}" "${n:-}"; then
      export DEVELOPER_DIR="$dev"
      major="$m"; minor="$n"
      echo "using Swift $m.$n from $dev"
      break
    fi
  done
fi
if ! swift_ok "${major:-}" "${minor:-}"; then
  have="$(swift --version 2>/dev/null | head -1 || true)"
  echo "error: building needs Swift 5.9 or newer, and this Mac has: ${have:-no Swift}. Update Apple's command line tools (xcode-select --install) or install Xcode 15 or newer, then try again." >&2
  exit 3
fi

# Go, for sysq, sysqd and sysact.
GO=""
for g in "$(command -v go 2>/dev/null || true)" /opt/homebrew/bin/go /usr/local/go/bin/go /usr/local/bin/go; do
  if [[ -n "$g" && -x "$g" ]]; then GO="$g"; break; fi
done
if [[ -z "$GO" ]]; then
  echo "error: building needs Go (for sysq, sysqd and sysact). Install it with: brew install go   (or from https://go.dev/dl), then try again." >&2
  exit 3
fi
echo "using $("$GO" version)"

if [[ "${UNIVERSAL:-1}" == 1 ]]; then
  GOARCHES=(arm64 amd64); ARCHS=(--arch arm64 --arch x86_64)
else
  case "$(uname -m)" in arm64) GOARCHES=(arm64) ;; *) GOARCHES=(amd64) ;; esac
  ARCHS=()
fi

rm -rf build && mkdir -p build/go "$APP/Contents/MacOS" "$APP/Contents/Resources/bin"

# Go binaries: each module from its own directory with its own go.mod (GOWORK=off; sysact's
# go.mod points at ../sysq with a replace directive).
build_go() { # module-dir package output
  local mod="$1" pkg="$2" out="$3" parts=()
  for arch in "${GOARCHES[@]}"; do
    (cd "$ROOT/$mod" && GOWORK=off GOTOOLCHAIN=auto CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" \
      "$GO" build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$HEADROOM/build/go/$out-$arch" "$pkg")
    parts+=("build/go/$out-$arch")
  done
  if [[ "${#parts[@]}" -gt 1 ]]; then
    lipo -create "${parts[@]}" -output "$APP/Contents/Resources/bin/$out"
  else
    cp "${parts[0]}" "$APP/Contents/Resources/bin/$out"
  fi
  chmod 755 "$APP/Contents/Resources/bin/$out"
}
build_go sysq ./cmd/sysq sysq
build_go sysq ./cmd/sysqd sysqd
build_go sysact ./cmd/sysact sysact

# The app.
swift build -c release --product "$NAME" ${ARCHS[@]+"${ARCHS[@]}"}
BINDIR="$(swift build -c release --product "$NAME" ${ARCHS[@]+"${ARCHS[@]}"} --show-bin-path)"
cp "$BINDIR/$NAME" "$APP/Contents/MacOS/$NAME"
# SwiftPM resource bundles, where their code looks for them.
for bundle in "$BINDIR"/*.bundle; do
  if [[ -e "$bundle" ]]; then cp -R "$bundle" "$APP/Contents/Resources/"; fi
done
# Shipped defaults: YAML config, JSON Schemas, prompts (copied to Application Support on first run).
cp -R "$HEADROOM/config" "$APP/Contents/Resources/config"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>com.ainigh.headroom</string>
  <key>CFBundleName</key><string>$NAME</string>
  <key>CFBundleDisplayName</key><string>Headroom</string>
  <key>CFBundleExecutable</key><string>$NAME</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>HeadroomCommit</key><string>$COMMIT</string>
  <key>HeadroomBranch</key><string>$BRANCH</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSAppleEventsUsageDescription</key><string>Headroom asks apps to quit and moves items to the Trash only after you confirm each action.</string>
</dict>
</plist>
PLIST

# Ad-hoc signatures: Apple silicon only runs signed code, and this needs no developer account.
# Nested binaries first, then the bundle.
for bin in "$APP"/Contents/Resources/bin/*; do
  codesign --force --sign - --timestamp=none "$bin"
done
codesign --force --sign - --timestamp=none "$APP"
ditto -c -k --keepParent "$APP" "build/$NAME.zip"
rm -rf build/go
echo "built $HEADROOM/$APP ($VERSION, $BRANCH, $COMMIT)"
