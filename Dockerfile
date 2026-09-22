# syntax=docker/dockerfile:1
# ---------------------------------------------------------------------------
# Up - single image build
#
#   stage 1 "web"   : builds the Vue 3 + Vite frontend (web/dist)
#   stage 2 "build" : compiles the Go backend and embeds web/dist inside it
#   stage 3 final   : minimal Alpine runtime, non-root user, single binary
#
# The final image is self contained: no external build steps are required.
# Build from the repository root:  docker build -t ghcr.io/ivancarlosti/up:latest .
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Stage 1 - Frontend
# ---------------------------------------------------------------------------
FROM node:24-alpine AS web
WORKDIR /web

# Dependency layer (cached while package.json / package-lock.json do not change)
COPY web/package.json web/package-lock.json ./
RUN npm ci

# Application sources (vite.config.ts outputs to /web/dist)
COPY web/ ./
RUN npm run build

# ---------------------------------------------------------------------------
# Stage 2 - Backend (embeds the frontend build through web/embed.go)
# ---------------------------------------------------------------------------
FROM golang:1.27-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src

# Module layer (cached while go.mod / go.sum do not change)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# web/embed.go uses //go:embed all:dist, so the assets produced by stage 1
# must be in place before the Go compiler runs.
RUN rm -rf web/dist && mkdir -p web/dist
COPY --from=web /web/dist ./web/dist

ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w \
        -X github.com/ivancarlosti/up/internal/version.Version=${VERSION} \
        -X github.com/ivancarlosti/up/internal/version.Commit=${COMMIT}" \
      -o /out/up ./cmd/server

# ---------------------------------------------------------------------------
# Stage 3 - Runtime
# ---------------------------------------------------------------------------
FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata wget \
    && adduser -D -u 1000 -h /app up

COPY --from=build /out/up /usr/local/bin/up

USER up
WORKDIR /app

# The application port inside the container; docker-compose maps 3000:3000.
ENV APP_PORT=3000
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${APP_PORT}/api/health" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/up"]

LABEL org.opencontainers.image.title="Up" \
      org.opencontainers.image.description="Minimalist, cluster-ready uptime monitoring (HTTP, Keyword, TCP, DNS)" \
      org.opencontainers.image.source="https://github.com/ivancarlosti/up" \
      org.opencontainers.image.url="https://github.com/ivancarlosti/up" \
      org.opencontainers.image.licenses="MIT"
