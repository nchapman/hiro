# Build web UI
FROM node:24-alpine AS web
WORKDIR /app/web/ui
COPY web/ui/package*.json ./
RUN npm ci
COPY web/ui/ ./
RUN npm run build

# Build Go binary
FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/ui/dist ./web/ui/dist
RUN CGO_ENABLED=0 go build -o /hive ./cmd/hive

# Runtime
FROM ubuntu:24.04

# System tools
RUN DEBIAN_FRONTEND=noninteractive apt-get update && apt-get install -y --no-install-recommends \
    # essentials
    ca-certificates curl wget git openssh-client \
    # build tools (agents may compile native extensions at runtime)
    build-essential pkg-config cmake \
    # utilities
    jq ripgrep tree unzip zip tar gzip file less \
    # networking
    dnsutils iputils-ping net-tools \
    # misc
    locales \
    && locale-gen en_US.UTF-8 \
    && rm -rf /var/lib/apt/lists/*

ENV LANG=en_US.UTF-8
ENV LC_ALL=en_US.UTF-8

# Create agent user pool for per-agent Unix user isolation.
# Each agent process runs as a dedicated user from this pool.
RUN groupadd -g 10000 hive-agents \
    && for i in $(seq 0 63); do \
        uid=$((10000 + i)); \
        useradd -r -u $uid -g hive-agents -M -d /nonexistent -s /bin/bash "hive-agent-$i"; \
    done

# Workspace uses setgid (2775) so files created by any agent inherit the
# hive-agents group and are group-writable for collaborative access.
RUN mkdir -p /workspace && chown root:hive-agents /workspace && chmod 2775 /workspace

# Install mise (tool version manager). All mise state lives under /opt/mise —
# binary, tool installs, config, cache, and shims — so every user (root and
# agent UIDs) shares one installation. Shims on PATH resolve tools automatically.
ENV MISE_DATA_DIR=/opt/mise
ENV MISE_CONFIG_DIR=/opt/mise/config
ENV MISE_CACHE_DIR=/opt/mise/cache
ENV MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml
ENV MISE_INSTALL_PATH=/usr/local/bin/mise
ENV PATH="/opt/mise/shims:${PATH}"
RUN curl -fsSL https://mise.run | sh

# Install runtimes and tools via mise, plus common global packages.
RUN mise use -g node@24 python@3.12 uv@latest \
    && npm install -g \
        typescript \
        ts-node \
        prettier \
        eslint \
    && uv pip install --system --no-cache \
        requests \
        pyyaml \
        beautifulsoup4 \
        pytest \
        ruff \
        httpx \
    && node --version && python3 --version && which eslint && which ruff

# Make tool installations group-writable so agent users (hive-agents) can
# install additional tools at runtime via mise. Setgid ensures new files
# inherit the hive-agents group.
RUN chgrp -R hive-agents /opt/mise \
    && chmod -R g+rwX /opt/mise \
    && find /opt/mise -type d -exec chmod g+s {} +

WORKDIR /workspace

COPY --from=build /hive /usr/local/bin/hive

# Control plane runs as root (required for per-agent UID switching).
ENTRYPOINT ["hive"]
