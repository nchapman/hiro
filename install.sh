#!/bin/sh
set -e

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is not installed. See https://docs.docker.com/get-docker/"
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "Error: 'docker compose' plugin not found. See https://docs.docker.com/compose/install/"
  exit 1
fi

REPO="https://raw.githubusercontent.com/nchapman/hiro/main"
DIR="${1:-hiro}"

echo "Setting up Hiro in ./$DIR"
mkdir -p "$DIR"
curl -fsSL "$REPO/docker-compose.yml" -o "$DIR/docker-compose.yml"
curl -fsSL "$REPO/seccomp.json" -o "$DIR/seccomp.json"

echo ""
echo "Done! To start Hiro:"
echo ""
echo "  cd $DIR"
echo "  docker compose up -d"
echo ""
echo "Then open http://localhost:8080"
