# syntax=docker/dockerfile:1

FROM node:24-alpine AS extension-builder
WORKDIR /src/extension
COPY extension/package.json extension/package-lock.json ./
RUN npm ci
COPY extension/ ./
RUN npm run build:bundle

FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS relay-builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/relay
COPY relay/go.mod relay/go.sum ./
RUN go mod download
COPY relay/ ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/remote-intercom-relay ./cmd/relay

FROM alpine:3.22
RUN addgroup -S remote-intercom && adduser -S -G remote-intercom remote-intercom
COPY --from=relay-builder /out/remote-intercom-relay /usr/local/bin/remote-intercom-relay
COPY --from=extension-builder /src/extension/dist/remote-intercom-extension.mjs /bundle/remote-intercom-extension.mjs
USER remote-intercom
ENV RELAY_ADDR=:8080
ENV RELAY_EXTENSION_BUNDLE=/bundle/remote-intercom-extension.mjs
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
CMD ["remote-intercom-relay"]
