FROM golang:1.27.3-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY testdata ./testdata
RUN go test ./... && go vet ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/spartan-stock-bot ./cmd/spartan-stock-bot

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates chromium fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 65532 bot \
    && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /tmp bot \
    && mkdir -p /data \
    && chown 65532:65532 /data
ENV FETCH_MODE=browser \
    BROWSER_EXECUTABLE=/usr/bin/chromium \
    BROWSER_NO_SANDBOX=true \
    HOME=/tmp \
    XDG_CONFIG_HOME=/tmp/chromium-config \
    XDG_CACHE_HOME=/tmp/chromium-cache
WORKDIR /
COPY --from=builder /out/spartan-stock-bot /spartan-stock-bot
COPY --chown=65532:65532 data/.gitkeep /data/.gitkeep
USER 65532:65532
ENTRYPOINT ["/spartan-stock-bot"]
