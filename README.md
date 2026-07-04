# Remote Intercom

Remote Intercom is a self-hostable relay for pi intercom sessions. It includes:

- a Go relay that serves HTTP bootstrap endpoints and WebSocket session traffic;
- a TypeScript pi extension/client;
- a relay-rendered installer script for local configuration.

Channels are ephemeral in-memory rooms keyed by channel name + PIN. The first device becomes owner; later devices require owner approval.

## Quick start

Run the relay:

```bash
cd relay
RELAY_TOKEN_SECRET="$(openssl rand -hex 32)" go run ./cmd/relay
```

Install local client configuration from the relay:

```bash
curl -fsSL http://127.0.0.1:8080/install.sh | sh
```

Build the extension from this checkout:

```bash
cd extension
npm ci
npm test
npm run build
```

## Architecture

```text
pi extension <--HTTP /channels/connect-- relay
pi extension <--WebSocket /ws----------> relay <--WebSocket /ws--> pi extension
```

HTTP is used for health, version, installer rendering, and token bootstrap. WebSocket is used for presence, join approval, member lists, status, and intercom messages.

## Development commands

```bash
cd relay
go test ./...
go run ./cmd/relay
```

```bash
cd extension
npm ci
npm test
npm run build
```

## Documentation

- [Wire protocol](protocol/wire.md)
- [Self-hosting](docs/self-hosting.md)
- [Security model](docs/security.md)

## Status

This is an MVP. There is no durable channel storage, published extension package, admin API, or end-to-end encryption yet.
