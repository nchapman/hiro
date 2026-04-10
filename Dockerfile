# Build web UI
FROM node:24-alpine AS web
WORKDIR /app/web/ui
COPY web/ui/package*.json ./
RUN npm ci
COPY web/ui/ ./
RUN npm run build

# Go source + dependencies (shared between build and test stages)
FROM golang:1.26 AS go-base
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Build Go binary
FROM go-base AS build
ARG VERSION=dev
COPY --from=web /app/web/ui/dist ./web/ui/dist
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=${VERSION}" -o /hiro ./cmd/hiro

# Test runner — docker compose run --rm --build test
FROM go-base AS test

# Stub web UI so go:embed is satisfied without building the frontend.
RUN mkdir -p web/ui/dist && echo '<!doctype html>' > web/ui/dist/index.html

# Install mise — same env vars as runtime stage.
ENV MISE_DATA_DIR=/opt/mise
ENV MISE_CONFIG_DIR=/opt/mise/config
ENV MISE_CACHE_DIR=/opt/mise/cache
ENV MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml
ENV MISE_INSTALL_PATH=/usr/local/bin/mise
ENV PATH="/opt/mise/shims:${PATH}"
RUN curl -fsSL https://mise.run | sh \
    && mise use -g node@24 python@3.12 \
    && chmod -R a+rX /opt/mise

# Pre-build the binary so tests that spawn agent processes have it available.
RUN go build -o /usr/local/bin/hiro ./cmd/hiro

CMD ["go", "test", "-race", "./...", "-v", "-count=1"]

# Runtime
FROM ubuntu:24.04

# System tools
RUN DEBIAN_FRONTEND=noninteractive apt-get update && apt-get install -y --no-install-recommends \
    # essentials
    ca-certificates curl wget git openssh-client rsync \
    # build tools (agents may compile native extensions at runtime)
    build-essential pkg-config cmake \
    # editors
    nano vim-tiny \
    # utilities
    jq ripgrep tree unzip zip tar gzip file less bc sqlite3 gettext-base zstd \
    # process tools
    htop strace lsof \
    # terminal multiplexer (persistent sessions across disconnects)
    tmux \
    # networking
    dnsutils iputils-ping net-tools netcat-openbsd socat \
    # system
    sudo locales \
    && locale-gen en_US.UTF-8 \
    && rm -rf /var/lib/apt/lists/*

ENV LANG=en_US.UTF-8
ENV LC_ALL=en_US.UTF-8

# Create a single non-root user for the control plane and agent workers.
# Landlock + seccomp provide isolation — no UID pool needed.
RUN groupadd -g 10000 hiro \
    && useradd -u 10000 -g hiro -m -s /bin/bash hiro

# Platform root — owned by hiro user.
RUN mkdir -p /hiro && chown hiro:hiro /hiro

# Install mise (tool version manager). All mise state lives under /opt/mise —
# binary, tool installs, config, cache, and shims — so the hiro user and
# agent workers share one installation. Shims on PATH resolve tools automatically.
ENV MISE_DATA_DIR=/opt/mise
ENV MISE_CONFIG_DIR=/opt/mise/config
ENV MISE_CACHE_DIR=/opt/mise/cache
ENV MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml
ENV MISE_INSTALL_PATH=/usr/local/bin/mise
ENV PATH="/opt/mise/shims:${PATH}"
RUN curl -fsSL https://mise.run | sh

# Install runtimes and tools via mise, plus common global packages.
RUN mise use -g node@24 python@3.12 uv@latest ruff@latest \
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
        httpx \
    && node --version && python3 --version && which eslint && ruff --version

# Modern CLI tools — installed via mise for easy versioning.
RUN mise use -g eza@latest bat@latest fd@latest fzf@latest zoxide@latest delta@latest starship@latest \
    && eza --version && bat --version && fd --version && fzf --version \
    && zoxide --version && delta --version && starship --version

# Make mise installations readable by the hiro user.
RUN chown -R hiro:hiro /opt/mise

# Shell configuration — polished terminal experience for all users.
COPY docker/shell/bashrc /etc/hiro.bashrc
COPY docker/shell/starship.toml /etc/starship.toml
ENV STARSHIP_CONFIG=/etc/starship.toml
RUN echo 'source /etc/hiro.bashrc' >> /etc/bash.bashrc

# Git defaults for a pleasant experience.
RUN git config --system init.defaultBranch main \
    && git config --system core.pager delta \
    && git config --system interactive.diffFilter 'delta --color-only' \
    && git config --system delta.navigate true \
    && git config --system delta.syntax-theme Dracula \
    && git config --system delta.line-numbers true \
    && git config --system merge.conflictstyle zdiff3

WORKDIR /hiro

COPY --from=build /hiro /usr/local/bin/hiro

# Run as non-root hiro user. Landlock + seccomp-BPF provide agent isolation.
USER hiro
ENTRYPOINT ["hiro"]
