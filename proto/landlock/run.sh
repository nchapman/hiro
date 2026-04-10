#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../.."

echo "Building landlock prototype..."
docker build -t hiro-landlock-proto -f proto/landlock/Dockerfile .

echo ""
echo "--- Run 1: Default Docker seccomp (no special flags) ---"
echo "  Expected: Landlock and seccomp pass; netns fails (Docker blocks clone with CLONE_NEWUSER)"
echo ""
docker run --rm hiro-landlock-proto || true

echo ""
echo "--- Run 2: Custom seccomp allowing clone with namespace flags ---"
echo "  Expected: All tests pass"
echo ""
docker run --rm \
  --security-opt seccomp="$(pwd)/proto/landlock/seccomp.json" \
  hiro-landlock-proto
