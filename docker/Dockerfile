# Multi-stage build for the multi-agent-rag services. Single-arch **amd64** (the
# deploy VM is Debian/amd64) — no cross-compile. Two runtime targets:
#
#   minimal — distroless static (~20 MB), for the 6 code-only services
#             (orchestrator, registry, git-publisher, mcp-gateway,
#              linear-gateway, ingestion-worker). No toolchain, runs nonroot.
#   worker  — golang + git (~400 MB), ONLY for the worker: its developer stage
#             runs `go build`/`go test` and git clone/push in a local sandbox,
#             so it needs the toolchain at runtime.
#
# Build context is the repo root:
#   docker build -f deploy/Dockerfile --target minimal -t marag:local .
#   docker build -f deploy/Dockerfile --target worker  -t marag-worker:local .

# ---- build stage ----
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# No CGO (project convention) → fully static binaries. amd64 only. `-s -w`
# strips debug info to shrink the binaries.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o /out/ ./cmd/...

# ---- minimal runtime: the 6 code-only services ----
# distroless/static ships CA certs + tzdata and runs as nonroot; it has no shell
# or toolchain. Each stack service selects its binary via `command:
# ["/app/<name>"]` — there is intentionally no default.
FROM gcr.io/distroless/static-debian12:nonroot AS minimal
COPY --from=build /out/ /app/
# The empty, non-secret config scaffold; real config comes from Consul at
# runtime (CONSUL=true). Secrets are never baked (see .dockerignore).
COPY config.json /app/config.json
ENV CONFIG_JSON=/app/config.json
ENTRYPOINT []

# ---- worker runtime: needs the Go toolchain + git for the developer sandbox ----
FROM golang:1.25-bookworm AS worker
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# All binaries (not just the worker): this image also has git + a shell, so it's
# the one used for admin one-offs like clone-then-ingest (ingestion-worker).
COPY --from=build /out/ /app/
COPY config.json /app/config.json
ENV CONFIG_JSON=/app/config.json
# A sane git identity for sandbox commits inside the container.
RUN git config --system user.email "agent@multi-agent-rag.local" \
    && git config --system user.name "multi-agent-rag"
CMD ["/app/worker"]
