# Build the web UI (Svelte) into internal/webui/dist, which the Go binary embeds.
FROM node:22-bookworm-slim AS web

RUN npm install -g pnpm@10

WORKDIR /app/web

COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY web/ ./
RUN mkdir -p /app/internal/webui/dist && pnpm build

FROM golang:1.26-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web /app/internal/webui/dist/ ./internal/webui/dist/

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# Nothing in the server needs cgo, so build a static binary. The build cache
# lives in a cache mount so it does not bloat the builder layer.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /out/CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /CLIProxyAPI

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
# Reference template only; mount your own config.yaml at /CLIProxyAPI/config.yaml.
COPY config.example.yaml /CLIProxyAPI/config.example.yaml

# Timezone defaults to UTC; pass -e TZ=Area/City to change it.
EXPOSE 8317

CMD ["./CLIProxyAPI"]
