# Remote Intercom

Remote Intercom is a self-hostable channel relay for pi sessions that cannot reach each other through local IPC or SSH.

The concept is: open the same human-friendly channel from two machines, such as `dwkim 1234`, and let the relay connect those pi sessions with an intercom-like tool surface. Users should not need to copy long tokens, manage SSH access, or use a separate admin panel. The pi agent conversation is the primary interface.

It includes:

- a Go relay that serves HTTP bootstrap endpoints and WebSocket session traffic;
- a TypeScript pi extension/client;
- a relay-rendered installer script that installs a bundled pi extension and local relay configuration.

Channels are ephemeral live rooms keyed by channel name + PIN. The first device becomes owner; later devices require owner approval. Owner is a live role, not a permanent account: if the owner disconnects, ownership fails over to the next online member, and returns when the higher-priority member reconnects while the channel still exists.

## Quick start

Build the bundled pi extension, then run the relay with the bundle path:

```bash
cd extension
npm ci
npm run build:bundle

cd ../relay
RELAY_TOKEN_SECRET="$(openssl rand -hex 32)" \
RELAY_EXTENSION_BUNDLE="$(pwd)/../extension/dist/remote-intercom-extension.mjs" \
go run ./cmd/relay
```

Install from the relay:

```bash
curl -fsSL http://127.0.0.1:8080/install.sh | sh
```

The installer writes:

- relay config: `${PI_REMOTE_INTERCOM_CONFIG_DIR:-$HOME/.pi/remote-intercom}/config.json`
- pi extension: `${PI_REMOTE_INTERCOM_EXTENSION_DIR:-$HOME/.pi/agent/extensions/remote-intercom}/index.js`

Restart pi or run `/reload`, then use the `remote_intercom` tool.

## Architecture

```text
pi extension <--HTTP /channels/connect-- relay
pi extension <--WebSocket /ws----------> relay <--WebSocket /ws--> pi extension
```

HTTP is used for health, version, installer rendering, and token bootstrap. WebSocket is used for live presence, owner changes, join approval, member lists, status, and intercom messages.

The relay is intentionally narrow: it routes messages, tracks ephemeral channel state, and enforces owner approval. It does not expose a server-side admin/control API for manipulating rooms. Channel decisions are made through the owner pi agent flow.

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

- [Concept and design direction](docs/concept.md)
- [Wire protocol](protocol/wire.md)
- [Self-hosting](docs/self-hosting.md)
- [Security model](docs/security.md)

## Status

This is an MVP. There is no durable channel storage, published extension package, admin API, or end-to-end encryption yet.
