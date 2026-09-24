# syntax=docker/dockerfile:1.7

FROM node:22-alpine AS web-builder
WORKDIR /src/internal/api/web

COPY internal/api/web/package.json internal/api/web/package-lock.json ./
RUN npm ci

COPY internal/api/web/ ./
RUN npm run build

FROM golang:1.26-alpine AS go-builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
COPY --from=web-builder /src/internal/api/web/dist ./internal/api/web/dist

# TAGS must stay identical to the Makefile's TAGS_FULL. with_mihomo is
# deliberately absent (docs/ENGINE_DECISIONS.md D-1): compiling it in would make
# GET /api/v1/system/capabilities report mihomo as built while every mihomo node
# still fails with ENGINE_NOT_BUILT.
ARG TAGS="with_quic with_grpc with_utls with_wireguard with_gvisor with_openvpn with_openconnect http2legacy"
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 go build -trimpath \
  -tags "${TAGS}" \
  -ldflags="-s -w \
  -X prism/internal/buildinfo.Version=${VERSION} \
  -X prism/internal/buildinfo.GitCommit=${GIT_COMMIT} \
  -X prism/internal/buildinfo.BuildTime=${BUILD_TIME} \
  -X prism/internal/buildinfo.Tags=$(printf '%s' "${TAGS}" | tr ' ' ',')" \
  -o /out/prism ./cmd/prism

FROM alpine:3.21
# NOTE: Keep this runtime stage in sync with .github/Dockerfile.release.
# GHCR release images are built from .github/Dockerfile.release, not this file.
RUN apk add --no-cache ca-certificates tzdata su-exec \
  && addgroup -S prism \
  && adduser -S -G prism -h /var/lib/prism prism \
  && mkdir -p /var/cache/prism /var/lib/prism /var/log/prism \
  && chown -R prism:prism /var/cache/prism /var/lib/prism /var/log/prism

COPY --from=go-builder /out/prism /usr/local/bin/prism
COPY docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 2260
VOLUME ["/var/cache/prism", "/var/lib/prism", "/var/log/prism"]

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/usr/local/bin/prism"]
