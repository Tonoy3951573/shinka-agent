#!/bin/bash
# ── Shinka Dynamics deb package builder ───────────────────────────────────────
# Automates cross-compilation of the agent and builds architecture-specific
# Debian packages (.deb) for both AMD64 and ARM64 platforms.
# ──────────────────────────────────────────────────────────────────────────────

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

BUILD_DIR="build"
DIST_DIR="dist-installers"

mkdir -p "$BUILD_DIR"
mkdir -p "$DIST_DIR"

build_arch() {
  local arch="$1"
  local goarch="$2"
  local stage_dir="$BUILD_DIR/pkg_stage_$arch"

  echo "=== Building shinka-agent for $arch (GOARCH=$goarch) ==="

  # 1. Compile Go binary
  echo "-> Compiling Go binary..."
  GOOS=linux GOARCH="$goarch" go build -o "$BUILD_DIR/shinka-agent-$arch" .

  # 2. Setup Staging Directory
  echo "-> Setting up staging directory..."
  rm -rf "$stage_dir"
  mkdir -p "$stage_dir"
  cp -r package/deb/* "$stage_dir/"

  # 3. Copy target binary
  mkdir -p "$stage_dir/usr/local/bin"
  cp "$BUILD_DIR/shinka-agent-$arch" "$stage_dir/usr/local/bin/shinka-agent"
  chmod 755 "$stage_dir/usr/local/bin/shinka-agent"

  # 4. Patch DEBIAN/control architecture
  echo "-> Patching control file..."
  sed -i "s/^Architecture: .*/Architecture: $arch/" "$stage_dir/DEBIAN/control"

  # 5. Fix permissions (dpkg requires DEBIAN directory and postinst to be >=0755 and <=0775)
  chmod 755 "$stage_dir/DEBIAN"
  chmod g-s "$stage_dir/DEBIAN"
  chmod 755 "$stage_dir/DEBIAN/postinst"

  # 6. Build the debian package
  echo "-> Packaging deb..."
  dpkg-deb --build "$stage_dir" "$DIST_DIR/shinka-agent_${arch}.deb"

  echo "-> Successfully generated: $DIST_DIR/shinka-agent_${arch}.deb"
  echo
}

# Build for both AMD64 (standard server/desktop) and ARM64 (Raspberry Pi 4/5 / ARM64 servers)
build_arch "amd64" "amd64"
build_arch "arm64" "arm64"

# Cleanup staging build folders
echo "-> Cleaning up build directory..."
rm -rf "$BUILD_DIR"

echo "=== All builds completed successfully! ==="
