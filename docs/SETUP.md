# TunnelX — Setup & Deployment Guide

> **TunnelX** is a self-hosted HTTP tunneling service that exposes local development
> servers to the public internet via HTTPS subdomains (e.g. `https://my-app.tunnel.example.com`).
> Think of it as a self-hosted alternative to ngrok or Cloudflare Tunnel.

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Prerequisites](#prerequisites)
3. [Building from Source](#building-from-source)
4. [DNS Configuration](#dns-configuration)
5. [TLS Certificate Modes](#tls-certificate-modes)
6. [Deployment Option A — Docker (Recommended)](#deployment-option-a--docker-recommended)
7. [Deployment Option B — Bare Metal (systemd)](#deployment-option-b--bare-metal-systemd)
8. [Server Administration](#server-administration)
9. [Client Setup](#client-setup)
10. [Usage Examples](#usage-examples)
11. [Configuration Reference](#configuration-reference)
12. [Healthcheck & Monitoring](#healthcheck--monitoring)
13. [Troubleshooting](#troubleshooting)

---

## Architecture Overview

```
┌─────────────────────┐         ┌─────────────────────────────────────┐
│  Developer Machine  │         │        Public Server / VPS          │
│                     │         │                                     │
│  ┌──────────┐       │   TLS   │  ┌──────────┐     ┌─────────────┐  │
│  │ tunnelx  │───────┼────:7835┼──│ tunnelxd │────▶│  SQLite DB  │  │
│  │ (client) │       │  yamux  │  │ (server) │     └─────────────┘  │
│  └────┬─────┘       │         │  └────┬─────┘                      │
│       │             │         │       │                             │
│  ┌────▼─────┐       │         │  ┌────▼──────────────────────┐     │
│  │ localhost│       │         │  │ :443  HTTPS reverse proxy │     │
│  │ :3000    │       │         │  │ *.tunnel.example.com      │     │
│  └──────────┘       │         │  └───────────────────────────┘     │
└─────────────────────┘         └─────────────────────────────────────┘
```

**How it works:**
1. `tunnelx` (client) opens a persistent TLS connection to `tunnelxd` (server) on port **7835**
2. The connection is multiplexed with [Yamux](https://github.com/hashicorp/yamux) — one control stream + N data streams
3. The client authenticates with a token and requests a subdomain
4. When a public HTTP request hits `https://<subdomain>.<domain>`, `tunnelxd` routes it through the Yamux session to the client
5. The client forwards the request to the local service and streams the response back

---

## Prerequisites

### Server Requirements
| Requirement | Minimum |
|-------------|---------|
| OS | Linux (amd64 or arm64), or Docker on any OS |
| RAM | 128 MB (256 MB recommended) |
| Disk | 100 MB (for binary + DB + cert cache) |
| Ports | **80**, **443**, **7835** open to the internet |
| Domain | A domain you control (e.g. `tunnel.example.com`) |

### Build Requirements (if building from source)
| Requirement | Version |
|-------------|---------|
| Go | 1.23 or later |
| Git | Any recent version |

### Client Requirements
- **Go 1.23+** (to build from source), or a pre-built binary
- Network access to the server on port **7835**

---

## Building from Source

```bash
# Clone the repository
git clone https://github.com/Sirtheprogrammer/tunnel.git
cd tunnel

# Build both binaries
go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags --always)" -o tunnelxd ./cmd/tunnelxd
go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags --always)" -o tunnelx  ./cmd/tunnelx

# Verify
./tunnelxd version
./tunnelx version
```

> **Note:** The project uses `modernc.org/sqlite` (pure Go), so no C compiler or
> CGO is required. `CGO_ENABLED=0` builds work correctly.

### Cross-compilation

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o tunnelxd-linux-amd64 ./cmd/tunnelxd
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o tunnelx-linux-amd64  ./cmd/tunnelx

# Linux arm64 (Raspberry Pi, ARM VPS)
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o tunnelxd-linux-arm64 ./cmd/tunnelxd

# macOS (client only, typically)
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o tunnelx-darwin-arm64 ./cmd/tunnelx

# Windows (client only, typically)
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o tunnelx.exe ./cmd/tunnelx
```

---

## DNS Configuration

Before deploying the server, set up DNS records pointing to your server's IP address.

### Required DNS Records

| Type | Name | Value | Purpose |
|------|------|-------|---------|
| `A` | `tunnel.example.com` | `<server-ip>` | Apex domain for the tunnel server |
| `A` | `*.tunnel.example.com` | `<server-ip>` | Wildcard — routes all subdomains to the server |

> **Replace** `tunnel.example.com` with your actual domain and `<server-ip>` with
> your server's public IPv4 address.

### Optional: IPv6

| Type | Name | Value |
|------|------|-------|
| `AAAA` | `tunnel.example.com` | `<server-ipv6>` |
| `AAAA` | `*.tunnel.example.com` | `<server-ipv6>` |

### Cloudflare Users

If using Cloudflare DNS (required for `--tls-mode acme`):
1. Set both records to **DNS only** (grey cloud / no proxy) — `tunnelxd` handles TLS itself
2. Create an **API Token** with **Zone → DNS → Edit** permission scoped to your zone
3. Save the token — you'll need it as `CF_API_TOKEN`

### Verification

```bash
# Should return your server's IP
dig +short tunnel.example.com
dig +short anything.tunnel.example.com
```

---

## TLS Certificate Modes

`tunnelxd` supports three TLS modes:

### Mode 1: `file` (Default) — Static Certificates

Use a pre-existing wildcard certificate (e.g. from Cloudflare Origin CA, your own CA, or a purchased cert).

```bash
tunnelxd serve \
  --domain tunnel.example.com \
  --tls-mode file \
  --tls-cert /path/to/wildcard.pem \
  --tls-key  /path/to/wildcard.key
```

**When to use:** Behind Cloudflare proxy (orange cloud), or with certificates from your own PKI.

### Mode 2: `acme` — Automatic Let's Encrypt

Automatically obtains and renews a free wildcard certificate from Let's Encrypt using DNS-01 challenge via Cloudflare.

```bash
CF_API_TOKEN=your-cloudflare-api-token \
tunnelxd serve \
  --domain tunnel.example.com \
  --tls-mode acme \
  --acme-email admin@example.com \
  --acme-cache-dir /var/lib/tunnelx/acme-cache
```

**Requirements:**
- Domain DNS managed by Cloudflare
- Cloudflare API token with `Zone:DNS:Edit` scope
- Valid email for Let's Encrypt expiry notices

> **Tip:** Use `--acme-staging` during initial setup to avoid Let's Encrypt rate
> limits while testing the DNS-01 wiring.

### Mode 3: `--dev` — No TLS (Development Only)

Serves plain HTTP. No certificates needed.

```bash
tunnelxd serve --dev --domain lvh.me
```

**When to use:** Local development and testing only. Never in production.

---

## Deployment Option A — Docker (Recommended)

### Quick Start

```bash
cd tunnel

# 1. Create an environment file
cat > deploy/.env << 'EOF'
TUNNEL_DOMAIN=tunnel.example.com
ACME_EMAIL=admin@example.com
CF_API_TOKEN=your-cloudflare-api-token-here
TLS_MODE=acme
EOF

# 2. Build and start
docker compose -f deploy/docker-compose.yml up -d

# 3. Check logs
docker compose -f deploy/docker-compose.yml logs -f
```

### Build the Image Manually

```bash
docker build \
  -f deploy/Dockerfile \
  -t tunnelxd:latest \
  --build-arg VERSION=$(git describe --tags --always) \
  .
```

### Run Directly with `docker run`

```bash
docker run -d \
  --name tunnelxd \
  --restart unless-stopped \
  -p 80:8080 -p 443:8443 -p 7835:7835 \
  -e CF_API_TOKEN=your-cloudflare-api-token \
  -v tunnelx-data:/var/lib/tunnelx \
  tunnelxd:latest serve \
    --domain tunnel.example.com \
    --public-url https://tunnel.example.com \
    --tls-mode acme \
    --acme-email admin@example.com \
    --db /var/lib/tunnelx/tunnelx.db \
    --acme-cache-dir /var/lib/tunnelx/acme-cache \
    --http-addr :8080 \
    --https-addr :8443
```

### Port Mapping Explained

| Host Port | Container Port | Purpose |
|-----------|---------------|---------|
| 80 | 8080 | HTTP → HTTPS redirect |
| 443 | 8443 | Public HTTPS tunnel traffic |
| 7835 | 7835 | Agent control connections |

> **Important:** The container runs as `nonroot` (UID 65532) and cannot bind to
> ports below 1024. The `--http-addr :8080` and `--https-addr :8443` flags bind
> to unprivileged ports inside the container, and Docker maps them to 80/443 on the host.

> **Important:** Always set `--public-url https://tunnel.example.com` when using
> Docker port mapping. Without it, generated tunnel URLs would incorrectly include
> the container port (`:8443`) instead of the standard HTTPS port.

### Using File-Based TLS with Docker

```bash
docker run -d \
  --name tunnelxd \
  -p 80:8080 -p 443:8443 -p 7835:7835 \
  -v tunnelx-data:/var/lib/tunnelx \
  -v /path/to/certs:/certs:ro \
  tunnelxd:latest serve \
    --domain tunnel.example.com \
    --public-url https://tunnel.example.com \
    --tls-mode file \
    --tls-cert /certs/wildcard.pem \
    --tls-key /certs/wildcard.key \
    --db /var/lib/tunnelx/tunnelx.db \
    --http-addr :8080 \
    --https-addr :8443
```

### Docker Compose Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `TUNNEL_DOMAIN` | Yes | Your tunnel domain (e.g. `tunnel.example.com`) |
| `ACME_EMAIL` | Yes (ACME) | Email for Let's Encrypt notices |
| `CF_API_TOKEN` | Yes (ACME) | Cloudflare API token with DNS edit permission |
| `TLS_MODE` | No | `acme` (default) or `file` |

### Updating

```bash
cd tunnel
git pull
docker compose -f deploy/docker-compose.yml build
docker compose -f deploy/docker-compose.yml up -d
```

---

## Deployment Option B — Bare Metal (systemd)

### 1. Create the System User

```bash
sudo useradd --system --home-dir /var/lib/tunnelx --shell /usr/sbin/nologin tunnelxd
sudo install -d -o tunnelxd -g tunnelxd -m 0750 /var/lib/tunnelx
sudo install -d -o root     -g tunnelxd -m 0750 /etc/tunnelx
```

### 2. Install the Binary

```bash
sudo install -m 0755 tunnelxd-linux-amd64 /usr/local/bin/tunnelxd
```

### 3. Create the Environment File

```bash
sudo tee /etc/tunnelx/tunnelxd.env > /dev/null << 'EOF'
CF_API_TOKEN=your-cloudflare-api-token-here
EOF
sudo chown root:tunnelxd /etc/tunnelx/tunnelxd.env
sudo chmod 0640 /etc/tunnelx/tunnelxd.env
```

### 4. Configure the Service

Edit `deploy/tunnelxd.service` — update these two lines:

```ini
ExecStart=/usr/local/bin/tunnelxd serve \
    --domain tunnel.example.com \              # ← Your domain
    --db /var/lib/tunnelx/tunnelx.db \
    --tls-mode acme \
    --acme-email admin@example.com \           # ← Your email
    --acme-cache-dir /var/lib/tunnelx/acme-cache
```

### 5. Install and Start

```bash
sudo cp deploy/tunnelxd.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tunnelxd
```

### 6. Verify

```bash
# Check the service is running
sudo systemctl status tunnelxd

# Follow the logs
sudo journalctl -u tunnelxd -f
```

### Security Hardening (Included in Service File)

The systemd unit includes production hardening:
- `NoNewPrivileges=yes` — prevents privilege escalation
- `ProtectSystem=strict` — read-only filesystem except `/var/lib/tunnelx`
- `AmbientCapabilities=CAP_NET_BIND_SERVICE` — binds ports 80/443 without root
- `MemoryDenyWriteExecute=yes` — blocks JIT/shellcode
- `SystemCallFilter=@system-service` — whitelist-only syscalls

---

## Server Administration

All admin commands require direct access to the server (SSH or `docker exec`).

### Create a User Account

```bash
# On the server (or docker exec)
tunnelxd users create admin@example.com --db /var/lib/tunnelx/tunnelx.db
```

This prints a token like `tx_a1b2c3d4e5f6...`. **Copy it immediately** — the
plaintext token is never stored (only its SHA-256 hash).

```
# Docker variant:
docker exec tunnelxd /usr/local/bin/tunnelxd users create admin@example.com \
  --db /var/lib/tunnelx/tunnelx.db
```

### List Users

```bash
tunnelxd users list --db /var/lib/tunnelx/tunnelx.db
```

### Disable / Enable a User

```bash
tunnelxd users disable baduser@example.com --db /var/lib/tunnelx/tunnelx.db
tunnelxd users enable  baduser@example.com --db /var/lib/tunnelx/tunnelx.db
```

### Issue Additional Tokens

```bash
tunnelxd tokens issue admin@example.com --name "laptop" --db /var/lib/tunnelx/tunnelx.db
```

### List and Revoke Tokens

```bash
tunnelxd tokens list admin@example.com --db /var/lib/tunnelx/tunnelx.db
tunnelxd tokens revoke <token-id> --db /var/lib/tunnelx/tunnelx.db
```

### Reserve a Subdomain

```bash
# Reserve "myapp" so only admin@example.com can use it
tunnelxd reserve myapp admin@example.com --db /var/lib/tunnelx/tunnelx.db

# List reservations for a user
tunnelxd reserve list admin@example.com --db /var/lib/tunnelx/tunnelx.db

# Release a reservation
tunnelxd reserve release myapp --db /var/lib/tunnelx/tunnelx.db
```

---

## Client Setup

### 1. Install the Client

Build from source (see [Building from Source](#building-from-source)) or copy a
pre-built binary to your `PATH`:

```bash
# Linux / macOS
sudo install -m 0755 tunnelx /usr/local/bin/tunnelx

# Windows — copy tunnelx.exe to a directory in your PATH
```

### 2. Authenticate

```bash
tunnelx login tx_your-token-here --server tunnel.example.com:7835
```

This saves the token and server address to `~/.tunnelx/config.yaml`.

### 3. Verify Configuration

```bash
tunnelx config check
```

Output:
```
Config file:  /home/user/.tunnelx/config.yaml
Server:       tunnel.example.com:7835
Authtoken:    tx_a1b2****cdef
```

### 4. Logout (Clear Token)

```bash
tunnelx logout
```

---

## Usage Examples

### Basic: Expose a Local Web Server

```bash
# Expose localhost:3000 with a random subdomain
tunnelx http 3000
```

Output:
```
tunnelx v0.1.0 ready
Forwarding   https://brave-otter-7f3a.tunnel.example.com → http://localhost:3000
```

### Custom Subdomain

```bash
tunnelx http 3000 --subdomain myapp
```

Creates `https://myapp.tunnel.example.com`

### Vite / Webpack Dev Server

Dev servers often check the `Host` header. Use `--host-header rewrite`:

```bash
tunnelx http 5173 --host-header rewrite
```

### Custom Host Header

```bash
tunnelx http 8080 --host-header "api.local.dev"
```

### Self-Hosted Server with Private CA

```bash
tunnelx http 3000 --ca-cert /path/to/my-ca.pem
```

### Development Mode (No TLS)

```bash
# Server started with --dev
tunnelx http 3000 --server localhost:7835 --no-tls
```

### Verbose Logging

```bash
tunnelx http 3000 -v
```

---

## Configuration Reference

### Server Flags (`tunnelxd serve`)

| Flag | Default | Description |
|------|---------|-------------|
| `--domain` | `tl.codesky.tech` | Base domain for tunnel subdomains |
| `--control-addr` | `:7835` | Agent connection listen address |
| `--https-addr` | `:443` | Public HTTPS listen address |
| `--http-addr` | `:80` | HTTP→HTTPS redirect listen address |
| `--public-url` | *(derived)* | Override the base URL for generated tunnel URLs |
| `--tls-mode` | `file` | Certificate source: `file` or `acme` |
| `--tls-cert` | | Wildcard cert PEM path (file mode) |
| `--tls-key` | | Private key path (file mode) |
| `--acme-email` | | Let's Encrypt contact email (acme mode) |
| `--acme-cf-token-env` | `CF_API_TOKEN` | Env var name for Cloudflare token |
| `--acme-cache-dir` | `acme-cache` | ACME certificate cache directory |
| `--acme-staging` | `false` | Use Let's Encrypt staging CA |
| `--dev` | `false` | Plain HTTP mode (no TLS) |
| `--db` | `tunnelx.db` | SQLite database path |
| `--max-tunnels-per-account` | `4` | Max concurrent tunnels per account |
| `--subdomain-lease` | `60s` | Subdomain hold time after disconnect |
| `--allow-anonymous` | `false` | Accept unauthenticated agents |
| `-v, --verbose` | `false` | Debug logging |

### Client Flags (`tunnelx http`)

| Flag | Default | Description |
|------|---------|-------------|
| `--subdomain` | *(random)* | Request a specific subdomain |
| `--host-header` | `preserve` | `preserve`, `rewrite`, or a literal value |
| `--server` | *(from config)* | Server address (`host:port`) |
| `--token` | *(from config)* | Override saved authtoken |
| `--ca-cert` | | Custom CA certificate PEM file |
| `--insecure` | `false` | Skip TLS verification |
| `--no-tls` | `false` | Connect without TLS |
| `-v, --verbose` | `false` | Debug logging |

### Client Config File (`~/.tunnelx/config.yaml`)

```yaml
server_addr: tunnel.example.com:7835
token: tx_your-token-here
ca_cert: ""        # optional: path to custom CA PEM
insecure: false    # optional: skip TLS verification
```

---

## Healthcheck & Monitoring

### Health Endpoint

`tunnelxd` exposes a `/healthz` endpoint on the public HTTPS listener:

```bash
curl -s https://tunnel.example.com/healthz | jq
```

```json
{
  "status": "ok",
  "tunnels": 3
}
```

### Docker Healthcheck

The `docker-compose.yml` includes a healthcheck. Verify with:

```bash
docker inspect --format='{{.State.Health.Status}}' tunnelxd
```

### Monitoring Checklist

| Check | Method |
|-------|--------|
| Service running | `systemctl is-active tunnelxd` or `docker ps` |
| Health endpoint | `curl https://tunnel.example.com/healthz` |
| Certificate valid | `openssl s_client -connect tunnel.example.com:443 -servername tunnel.example.com` |
| Agent connectivity | `tunnelx http 8080` from a client machine |
| Logs | `journalctl -u tunnelxd -f` or `docker logs -f tunnelxd` |

---

## Troubleshooting

### Server Issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| `--acme-email is required with --tls-mode acme` | Missing email flag | Add `--acme-email you@example.com` |
| `CF_API_TOKEN not set` | Missing Cloudflare token | Set `CF_API_TOKEN` environment variable |
| `permission denied` on DB path | Wrong directory ownership | `chown tunnelxd:tunnelxd /var/lib/tunnelx` |
| `bind: permission denied` on port 80/443 | Missing capability | Add `CAP_NET_BIND_SERVICE` or use Docker port mapping |
| URLs contain `:8443` | Missing `--public-url` in Docker | Add `--public-url https://tunnel.example.com` |
| ACME rate limit errors | Too many cert requests | Use `--acme-staging` for testing |

### Client Issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| `connection refused` | Server not running or port blocked | Check server status; open port 7835 in firewall |
| `certificate signed by unknown authority` | Self-signed / private CA cert | Use `--ca-cert /path/to/ca.pem` or `--insecure` |
| `unauthorized` | Invalid or revoked token | Get a new token: `tunnelxd tokens issue <email>` |
| `subdomain_taken` | Another session holds the subdomain | Wait for lease to expire (default 60s) or use a different subdomain |
| Tunnel works but local service returns errors | Host header mismatch | Try `--host-header rewrite` |

### Checking Connectivity

```bash
# Test agent port from client machine
nc -zv tunnel.example.com 7835

# Test HTTPS from anywhere
curl -I https://tunnel.example.com

# Test DNS
dig +short '*.tunnel.example.com'
```

### Resetting the Database

```bash
# Stop the server first
rm /var/lib/tunnelx/tunnelx.db
# Restart — a fresh database will be created automatically
```

---

## Quick Reference — Full Deployment Checklist

```
□ 1. Buy/choose a domain (e.g. tunnel.example.com)
□ 2. Point DNS:  A   tunnel.example.com     → <server-ip>
                  A   *.tunnel.example.com   → <server-ip>
□ 3. Get Cloudflare API token (Zone:DNS:Edit) — if using ACME mode
□ 4. Deploy tunnelxd (Docker or systemd)
□ 5. Create first user:   tunnelxd users create admin@example.com
□ 6. Give token to client: tunnelx login tx_...
□ 7. Test:                  tunnelx http 3000
□ 8. Verify URL opens in browser ✓
```

