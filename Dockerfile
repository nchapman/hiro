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

# Create hive user (for build-time tool installation) and workspace directory.
# The hive user's primary group is hive-agents so that tools it installs are
# immediately group-accessible to all agent users at runtime.
# Workspace uses setgid (2775) so files created by any agent inherit the
# hive-agents group and are group-writable for collaborative access.
RUN useradd -r -g hive-agents -m -d /home/hive -s /bin/bash hive \
    && mkdir -p /workspace && chown root:hive-agents /workspace && chmod 2775 /workspace
USER hive
ENV HOME=/home/hive

# Install mise and uv. MISE_DATA_DIR is set explicitly so agent processes
# (which run with HOME=/tmp) can locate the shared tool installations.
ENV MISE_DATA_DIR=/home/hive/.local/share/mise
ENV PATH="${MISE_DATA_DIR}/shims:/home/hive/.local/bin:${PATH}"
RUN curl https://mise.run | sh \
    && mise settings set activate_aggressive true \
    && curl -LsSf https://astral.sh/uv/install.sh | sh

# Install node and python via mise, plus common global packages.
RUN mise use --global node@24 python@3.12 \
    && mise reshim \
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
    && mise reshim \
    && node --version && python3 --version && which eslint && which ruff

# Make tool installations group-writable so agent users (hive-agents) can
# install additional tools at runtime via mise. Setgid ensures new files
# inherit the hive-agents group.
USER root
RUN chgrp -R hive-agents /home/hive/.local \
    && chmod -R g+rwX /home/hive/.local \
    && find /home/hive/.local -type d -exec chmod g+s {} +

# Workspace
WORKDIR /workspace

COPY --from=build /hive /usr/local/bin/hive

# Control plane runs as root for UID switching
ENTRYPOINT ["hive"]
