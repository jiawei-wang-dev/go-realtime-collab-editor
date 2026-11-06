ARG GO_IMAGE=golang:1.26.10-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS build

WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
COPY go.mod go.sum ./
RUN GOPROXY=${GOPROXY} go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/collab-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

FROM ${RUNTIME_IMAGE}

RUN groupadd --system --gid 10001 collab \
    && useradd --system --uid 10001 --gid collab --home-dir /app --shell /usr/sbin/nologin collab

WORKDIR /app
COPY --from=build /out/collab-server /usr/local/bin/collab-server
COPY --from=build /out/healthcheck /usr/local/bin/healthcheck
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=collab:collab /src/migrations /app/migrations

USER 10001:10001
EXPOSE 8080/tcp 8443/udp
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
    CMD ["/usr/local/bin/healthcheck"]

ENTRYPOINT ["/usr/local/bin/collab-server"]
