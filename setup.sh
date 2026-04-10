#!/bin/sh
set -e

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

mkdir -p "$DIR"

COMPOSE_URL="https://raw.githubusercontent.com/nchapman/hiro/main/docker-compose.yml"

if [ -f "$DIR/docker-compose.yml" ]; then
  echo "docker-compose.yml already exists in $DIR/"
  echo ""
  echo "To update Hiro:"
  echo ""
  echo "  cd $DIR"
  echo "  docker compose pull"
  echo "  docker compose up -d"
  exit 0
fi

curl -fsSL "$COMPOSE_URL" -o "$DIR/docker-compose.yml"

echo "Done! To start Hiro:"
echo ""
echo "  cd $DIR"
echo "  docker compose up -d"
echo ""
echo "Then open http://localhost:8120"
