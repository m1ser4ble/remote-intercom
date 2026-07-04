# Self-hosting Remote Intercom

Remote Intercom is designed to run behind your own HTTPS reverse proxy. The current MVP stores channels in memory, so active sessions are lost when the relay restarts.

## Run locally

```bash
cd extension
npm ci
npm run build:bundle

cd ../relay
RELAY_TOKEN_SECRET="$(openssl rand -hex 32)" \
RELAY_EXTENSION_BUNDLE="$(pwd)/../extension/dist/remote-intercom-extension.mjs" \
go run ./cmd/relay
```

The relay listens on `:8080` by default. Override with `RELAY_ADDR`:

```bash
RELAY_ADDR=":9090" \
RELAY_TOKEN_SECRET="$(openssl rand -hex 32)" \
RELAY_EXTENSION_BUNDLE="$(pwd)/../extension/dist/remote-intercom-extension.mjs" \
go run ./cmd/relay
```

## Docker run

Until a release image is published, run from a checkout with the Go toolchain image:

```bash
docker run --rm \
  -p 8080:8080 \
  -e RELAY_ADDR=":8080" \
  -e RELAY_TOKEN_SECRET="$(openssl rand -hex 32)" \
  -e RELAY_EXTENSION_BUNDLE="/bundle/remote-intercom-extension.mjs" \
  -v "$PWD/relay:/app" \
  -v "$PWD/extension/dist/remote-intercom-extension.mjs:/bundle/remote-intercom-extension.mjs:ro" \
  -w /app \
  golang:1.26 \
  go run ./cmd/relay
```

Or add a stable `RELAY_TOKEN_SECRET` value to `docker-compose.yml` before using the included compose file:

```bash
docker compose up relay
```

## Required environment

- `RELAY_TOKEN_SECRET`: HMAC secret for member and pending JWTs. Use a long random value and keep it stable across relay restarts if clients may reconnect with existing tokens.
- `RELAY_ADDR`: listen address, default `:8080`.
- `RELAY_EXTENSION_BUNDLE`: path to the bundled pi extension served at `/extension.mjs`. Set this for one-line installer support.

If `RELAY_TOKEN_SECRET` is omitted, the relay generates a development-only secret on startup. Do not rely on that in shared deployments.

## HTTPS and reverse proxy

Terminate TLS in front of the relay and forward traffic to `http://127.0.0.1:8080`. The installer and connect responses infer public URLs from request headers:

- `Host: relay.example.com`
- `X-Forwarded-Proto: https`

Example nginx location:

```nginx
location / {
  proxy_pass http://127.0.0.1:8080;
  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-Proto $scheme;
}

location /ws {
  proxy_pass http://127.0.0.1:8080;
  proxy_http_version 1.1;
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection "upgrade";
  proxy_set_header Host $host;
  proxy_set_header X-Forwarded-Proto $scheme;
}
```

If you embed the HTTP API package in another server, set `PublicHTTPURL` and `PublicWSURL` on `httpapi.Server` when proxy headers are not sufficient.

## Install command

After the relay is reachable over HTTPS:

```bash
curl -fsSL https://relay.example.com/install.sh | sh
```

Safer alternative: download and inspect before running:

```bash
curl -fsSLo remote-intercom-install.sh https://relay.example.com/install.sh
less remote-intercom-install.sh
sh remote-intercom-install.sh
```

The installer writes:

- `~/.pi/remote-intercom/config.json` with relay HTTP and WebSocket URLs;
- `~/.pi/agent/extensions/remote-intercom/index.js` with the bundled pi extension downloaded from `/extension.mjs`.

Restart pi or run `/reload` after installation so pi discovers the extension.

## Operations notes

- Channels are ephemeral and in memory.
- Run one relay instance for a channel; there is no shared registry or sticky-session support yet.
- WebSocket clients should reconnect by calling `/channels/connect` again if their token expires or the relay restarts.
