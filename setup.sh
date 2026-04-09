#!/bin/sh
set -e

REPO="nchapman/hiro"
DIR="${1:-hiro}"

# Pre-flight checks.
if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is not installed. See https://docs.docker.com/get-docker/"
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Error: 'docker compose' plugin not found. See https://docs.docker.com/compose/install/"
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "Error: curl is not installed."
  exit 1
fi

# Detect checksum tool.
if command -v sha256sum >/dev/null 2>&1; then
  SHASUM="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  SHASUM="shasum -a 256"
else
  echo "Error: sha256sum or shasum is required but not found."
  exit 1
fi

# Find the latest release tag.
TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)
if [ -z "$TAG" ]; then
  echo "Error: could not determine latest release."
  exit 1
fi
case "$TAG" in
  v[0-9]*) ;;
  *) echo "Error: unexpected tag format: $TAG"; exit 1 ;;
esac

echo "Hiro $TAG"
echo ""

# Download release assets to a temp directory.
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT
BASE_URL="https://github.com/$REPO/releases/download/$TAG"
curl -fsSL "$BASE_URL/hiro-$TAG.tar.gz" -o "$WORK_DIR/hiro.tar.gz"
curl -fsSL "$BASE_URL/SHA256SUMS" -o "$WORK_DIR/SHA256SUMS"

# Verify tarball checksum before extraction.
EXPECTED=$(grep 'hiro-.*\.tar\.gz' "$WORK_DIR/SHA256SUMS" | head -1)
if [ -z "$EXPECTED" ]; then
  echo "Error: tarball not listed in SHA256SUMS."
  exit 1
fi
ACTUAL=$($SHASUM "$WORK_DIR/hiro.tar.gz" | cut -d' ' -f1)
EXPECTED_HASH=$(echo "$EXPECTED" | cut -d' ' -f1)
if [ "$ACTUAL" != "$EXPECTED_HASH" ]; then
  echo "Error: tarball checksum mismatch."
  echo "  Expected: $EXPECTED_HASH"
  echo "  Got:      $ACTUAL"
  exit 1
fi

# Extract verified tarball.
tar xzf "$WORK_DIR/hiro.tar.gz" -C "$WORK_DIR"

# Install or update each file.
mkdir -p "$DIR"
FRESH=true
if [ -f "$DIR/docker-compose.yml" ] || [ -f "$DIR/seccomp.json" ]; then
  FRESH=false
fi

install_file() {
  file="$1"
  src="$WORK_DIR/$file"
  dst="$DIR/$file"

  if [ ! -f "$dst" ]; then
    cp "$src" "$dst"
    echo "  Created $file"
    return
  fi

  if cmp -s "$src" "$dst"; then
    echo "  $file is up to date"
    return
  fi

  if [ "$file" = "seccomp.json" ]; then
    printf "  %s has changed. This file controls container security. Update? [y/N] " "$file"
  else
    printf "  %s has changed. Update? [y/N] " "$file"
  fi
  read -r answer </dev/tty
  case "$answer" in
    [yY]|[yY][eE][sS])
      cp "$dst" "$dst.bak"
      cp "$src" "$dst"
      echo "  Updated $file (backup: $file.bak)"
      ;;
    *)
      echo "  Skipped $file"
      ;;
  esac
}

echo ""
install_file "docker-compose.yml"
install_file "seccomp.json"

echo ""
if [ "$FRESH" = true ]; then
  echo "Done! To start Hiro:"
  echo ""
  echo "  cd $DIR"
  echo "  docker compose up -d"
  echo ""
  echo "Then open http://localhost:8080"
else
  echo "Done! To apply updates:"
  echo ""
  echo "  cd $DIR"
  echo "  docker compose pull"
  echo "  docker compose up -d"
fi
