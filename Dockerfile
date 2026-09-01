# syntax=docker/dockerfile:1.24.0

FROM node:26.8.1-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.27-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN apk add --no-cache ca-certificates git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=frontend /src/frontend/out ./frontend/out
RUN test -f frontend/out/index.html
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -mod=readonly -trimpath \
    -ldflags "-s -w -X github.com/Suhaibinator/kms/internal/cli.Version=${VERSION}" \
    -o /out/parameter-store ./cmd/parameter-store

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S kms \
    && adduser -S -G kms -h /data kms \
    && mkdir -p /data /key \
    && chown kms:kms /data /key
COPY --from=builder /out/parameter-store /usr/local/bin/parameter-store

LABEL org.opencontainers.image.source="https://github.com/Suhaibinator/kms" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="Self-hosted parameter store and secret management service"

USER kms:kms
WORKDIR /data
# Honored by serve and by every offline command (init, migrate, check, backup,
# restore, create-admin, rotate-admin, rotate-kek, import), which resolve
# settings flag > env > config file > default — so `docker run ... init --admin
# ops` needs no path flags.
ENV KMS_SQLITE_PATH=/data/kms.db \
    KMS_KEK_FILE=/key/master.key
VOLUME ["/data", "/key"]
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/parameter-store"]
CMD ["serve"]
