<p align="center">
  <img src="internal/web/static/icons8-tunnel.svg" width="72" height="72" alt="TunnelX Logo">
</p>

<h1 align="center">TunnelX</h1>

<p align="center">
  <strong>Self-hosted HTTP tunnel server, client, and developer web portal.</strong><br>
  Expose local development servers to the public internet via secure HTTPS subdomains.
</p>

```
tunnelx http 3000
→ https://brave-otter-7f3a.tunnel.example.com
```

## Features

- **Public HTTPS URLs** for local services — webhooks, share work-in-progress, test on real devices
- **Wildcard TLS** — automatic Let's Encrypt certs via DNS-01, or bring your own
- **WebSocket support** — protocol upgrades are spliced transparently
- **Multi-tenant** — accounts, tokens, per-account tunnel limits, subdomain reservations
- **Single binary** — no CGO, no external dependencies, pure-Go SQLite
- **Production-ready Docker** — distroless image, nonroot, compose file included
- **Reconnect leases** — disconnect and reconnect without losing your subdomain

## Quick Start

### Server (Docker)

```bash
# 1. Configure
cp deploy/.env.example deploy/.env
# Edit deploy/.env with your domain, email, and Cloudflare API token

# 2. Deploy
docker compose -f deploy/docker-compose.yml up -d

# 3. Create a user
docker exec tunnelxd tunnelxd users create you@example.com \
  --db /var/lib/tunnelx/tunnelx.db
# → tx_a1b2c3d4...  (save this token!)
```

### Client

```bash
# 1. Login
tunnelx login tx_a1b2c3d4... --server tunnel.example.com:7835

# 2. Start a tunnel
tunnelx http 3000
```

## Documentation

- **[Setup & Deployment Guide](docs/SETUP.md)** — full deployment instructions, DNS setup, TLS modes, troubleshooting

## Project Structure

```
tunnel/
├── cmd/
│   ├── tunnelx/          # Client CLI
│   └── tunnelxd/         # Server daemon + admin CLI
├── internal/
│   ├── agent/            # Client tunnel agent
│   ├── config/           # Client configuration (~/.tunnelx/config.yaml)
│   ├── names/            # Subdomain generator & validator
│   ├── proto/            # Wire protocol (JSON-over-Yamux)
│   ├── server/           # Server core, reverse proxy, registry
│   └── store/            # SQLite store (accounts, tokens, reservations)
├── deploy/
│   ├── Dockerfile        # Multi-stage distroless build
│   ├── docker-compose.yml
│   └── tunnelxd.service  # systemd unit (bare metal)
└── docs/
    └── SETUP.md          # Full deployment guide
```

## CLI Reference

### Client (`tunnelx`)

| Command | Description |
|---------|-------------|
| `tunnelx http <port>` | Expose a local HTTP service |
| `tunnelx login <token>` | Save auth token |
| `tunnelx logout` | Clear saved token |
| `tunnelx config check` | Show effective configuration |
| `tunnelx config path` | Print config file location |
| `tunnelx version` | Print version |

### Server (`tunnelxd`)

| Command | Description |
|---------|-------------|
| `tunnelxd serve` | Start the tunnel server |
| `tunnelxd users create <email>` | Create account + token |
| `tunnelxd users list` | List all accounts |
| `tunnelxd users disable <email>` | Disable an account |
| `tunnelxd tokens issue <email>` | Issue additional token |
| `tunnelxd tokens revoke <id>` | Revoke a token |
| `tunnelxd reserve <sub> <email>` | Reserve a subdomain |
| `tunnelxd reserve list <email>` | List reservations |
| `tunnelxd reserve release <sub>` | Release a reservation |

## Building

```bash
go build -trimpath -ldflags "-s -w" -o tunnelxd ./cmd/tunnelxd
go build -trimpath -ldflags "-s -w" -o tunnelx  ./cmd/tunnelx
```

## Testing

```bash
go test ./... -count=1 -v
```

## License

See [LICENSE](LICENSE) for details.

