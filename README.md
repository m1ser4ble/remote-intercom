# Remote Intercom

Remote Intercom is a self-hostable relay for pi intercom sessions. It includes a Go relay service and a TypeScript extension client scaffold.

## Development

```bash
cd relay
go test ./...
go run ./cmd/relay
```

The relay exposes `GET /healthz` on `:8080` by default.
