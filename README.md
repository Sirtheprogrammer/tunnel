<p align="center">
  <img src="internal/web/static/icons8-tunnel.svg" width="72" height="72" alt="TunnelX Logo">
</p>

<h1 align="center">TunnelX</h1>

<p align="center">
  <strong>Self-hosted HTTP tunnel server, CLI client, and developer web portal.</strong><br>
  Expose local development servers to the public internet via secure, instant HTTPS subdomains.
</p>

<p align="center">
  <a href="#features">Features</a> &bull;
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="#one-line-installers">Installers</a> &bull;
  <a href="#web-portal">Web Portal</a> &bull;
  <a href="#deployment">Deployment</a> &bull;
  <a href="#cli-reference">CLI Reference</a> &bull;
  <a href="docs/SETUP.md">Full Setup Guide</a>
</p>

---

```bash
$ tunnelx http 3000
Forwarding  https://brave-otter-7f3a.tl.codesky.tech → http://localhost:3000
```

## Features

- **Instant Public HTTPS Tunnels** — Expose `localhost` servers for testing webhooks, mobile APIs, OAuth callbacks, and sharing work.
- **Built-in Developer Web Portal** — Self-service web dashboard running at your apex domain with GitHub OAuth or email authentication.
- **Automatic Wildcard TLS** — Native Let's Encrypt certificates via ACME DNS-01 (Cloudflare), or custom static certs.
- **Full WebSocket & SSE Support** — HTTP/1.1 connection upgrades and streaming protocols are spliced transparently.
- **Multi-Tenant & Self-Service** — User accounts, authtoken generation/revocation, concurrent tunnel limits, and subdomain reservations.
- **Reserved Subdomains** — Lock in permanent subdomains (e.g., `https://myapp.yourdomain.com`) tied to your account.
- **Disconnect Leases** — Network hiccups don't lose your assigned subdomain (60-second grace window).
- **Single Pure-Go Binary** — No CGO, no external shared libraries (`modernc.org/sqlite`).
- **Production-Ready Docker & Nginx** — Non-root container with TLS SNI passthrough configs included.

---

## One-Line Installers

Install the `tunnelx` client CLI on developer machines in seconds:

#### Linux & macOS
```bash
curl -fsSL https://tl.codesky.tech/install.sh | bash
```

#### Windows (PowerShell)
```powershell
irm https://tl.codesky.tech/install.ps1 | iex
```

*(Replace `tl.codesky.tech` with your own self-hosted domain if hosting your own instance).*

---

## Quick Start

### 1. Web Portal & Authtokens

1. Navigate to `https://<YOUR_DOMAIN>` (e.g. `https://tl.codesky.tech`) in your browser.
2. Sign in using **GitHub OAuth** or register an **Email/Password** account.
3. Click **"New Authtoken"** in the dashboard to generate your secret token.
4. (Optional) Click **"Reserve Subdomain"** to claim permanent subdomains.

### 2. Connect Your Local Server

```bash
# Save your token and server
tunnelx login <YOUR_TOKEN> --server <YOUR_DOMAIN>:7835

# Forward local port 3000
tunnelx http 3000

# Forward Vite/Next.js dev servers with Host header rewriting
tunnelx http 5173 --host-header rewrite

# Forward to a reserved subdomain
tunnelx http 3000 --subdomain myapp
```

---

## Architecture

```
┌─────────────────────────┐                   ┌───────────────────────────────────────────────┐
│    Developer Machine    │                   │               VPS / Cloud Host                │
│                         │                   │                                               │
│  ┌───────────────────┐  │   TLS Connection  │  ┌───────────────┐         ┌───────────────┐  │
│  │   tunnelx (CLI)   │──┼───── Port 7835 ───┼─▶│  tunnelxd     │◀───────▶│  SQLite DB    │  │
│  └─────────┬─────────┘  │      (Yamux)      │  │  (Docker)     │         └───────────────┘  │
│            │            │                   │  └───────▲───────┘                            │
│  ┌─────────▼─────────┐  │                   │          │ :8443 (Raw TLS)                    │
│  │ Local App Server  │  │                   │  ┌───────┴─────────────────────────────────┐  │
│  │ (e.g. :3000)      │  │                   │  │ Nginx (Stream TLS Passthrough / SNI)    │  │
│  └───────────────────┘  │                   │  │ :80 (HTTP Redirect)  :443 (HTTPS)       │  │
└─────────────────────────┘                   │  └───────────────────▲─────────────────────┘  │
                                              └──────────────────────┼────────────────────────┘
                                                                     │ Public Internet Requests
                                                         ┌───────────┴──────────┐
                                                         │ https://*.domain.com │
                                                         │ https://domain.com   │
                                                         └──────────────────────┘
```

1. **Client Control Stream (`:7835`)**: The `tunnelx` CLI establishes an encrypted TLS multiplexed session (`yamux`) to `tunnelxd`.
2. **Nginx Reverse Proxy & TLS Passthrough (`:80` & `:443`)**:
   - Port `80` proxies HTTP requests to `tunnelxd` (`127.0.0.1:8080`), issuing automatic `301` HTTPS redirects.
   - Port `443` uses Nginx **SNI Stream Passthrough** (`ssl_preread on;`) to stream raw TLS traffic directly to `tunnelxd` (`127.0.0.1:8443`). `tunnelxd` terminates TLS using its internal Let's Encrypt certificates without requiring certbot on the host.
3. **Subdomain Routing & Web Portal**:
   - Requests to the apex domain (`https://tl.codesky.tech`) serve the interactive Web Portal & Dashboard.
   - Requests to subdomains (`https://myapp.tl.codesky.tech`) are matched against active sessions in `tunnelxd`'s registry and routed to the developer's client.

---

## Deployment

Deploying `tunnelxd` in production takes under 5 minutes using Docker Compose and Nginx:

### 1. DNS Records (Cloudflare)
Configure two DNS records pointing to your server's public IP:
- `A` `tl.codesky.tech` &rarr; `<SERVER_IP>` (DNS-only, Grey cloud)
- `A` `*.tl.codesky.tech` &rarr; `<SERVER_IP>` (DNS-only, Grey cloud)

### 2. Configure Environment (`deploy/.env`)
```bash
cp deploy/.env.example deploy/.env
```
Edit `deploy/.env`:
```env
TUNNEL_DOMAIN=tl.codesky.tech
ACME_EMAIL=admin@codesky.tech
CF_API_TOKEN=your-cloudflare-dns-edit-token
TLS_MODE=acme

# Optional: GitHub OAuth for Portal Login
GITHUB_CLIENT_ID=your_oauth_client_id
GITHUB_CLIENT_SECRET=your_oauth_client_secret
```

### 3. Nginx Stream & Proxy Setup
Ensure the Nginx stream module is loaded in `/etc/nginx/nginx.conf`:
```nginx
stream {
    include /etc/nginx/stream.d/*.conf;
}
```
Deploy the configurations:
```bash
sudo mkdir -p /etc/nginx/stream.d
sudo cp deploy/nginx-tunnelx-stream.conf /etc/nginx/stream.d/tunnelx.conf
sudo cp deploy/nginx-tunnelx.conf /etc/nginx/sites-available/tunnelx
sudo ln -s /etc/nginx/sites-available/tunnelx /etc/nginx/sites-enabled/tunnelx

# Test and reload
sudo nginx -t && sudo systemctl reload nginx
```

### 4. Start TunnelX
```bash
docker compose -f deploy/docker-compose.yml up -d --build
```

For complete instructions, step-by-step guides, and bare-metal deployment, see **[docs/SETUP.md](docs/SETUP.md)**.

---

## CLI Reference

### Client (`tunnelx`)

| Command | Description | Example |
|---------|-------------|---------|
| `tunnelx http <target>` | Expose local port or host:port | `tunnelx http 3000` |
| `--subdomain <name>` | Request a specific or reserved subdomain | `tunnelx http 3000 --subdomain myapp` |
| `--host-header <mode>` | `preserve`, `rewrite`, or custom host header | `tunnelx http 5173 --host-header rewrite` |
| `tunnelx login <token>` | Save authentication token and server | `tunnelx login tx_... --server domain:7835` |
| `tunnelx logout` | Clear saved credentials | `tunnelx logout` |
| `tunnelx config check` | Inspect current client configuration | `tunnelx config check` |
| `tunnelx version` | Print client version | `tunnelx version` |

### Server (`tunnelxd`)

| Command | Description |
|---------|-------------|
| `tunnelxd serve` | Start the tunneling engine and web portal |
| `tunnelxd users create <email>` | Create user account and generate authtoken |
| `tunnelxd users list` | List all registered user accounts |
| `tunnelxd users disable <email>` | Deactivate a user account |
| `tunnelxd tokens issue <email>` | Issue a new authtoken for an account |
| `tunnelxd tokens list <email>` | List tokens associated with an account |
| `tunnelxd tokens revoke <id>` | Revoke an existing authtoken |
| `tunnelxd reserve <subdomain> <email>` | Bind a subdomain permanently to an account |
| `tunnelxd reserve list <email>` | List reserved subdomains |
| `tunnelxd reserve release <subdomain>` | Release a subdomain reservation |

---

## Building from Source

```bash
# Clone the repository
git clone https://github.com/Sirtheprogrammer/tunnel.git
cd tunnel

# Build binaries (pure Go, CGO_ENABLED=0)
go build -trimpath -ldflags "-s -w" -o tunnelxd ./cmd/tunnelxd
go build -trimpath -ldflags "-s -w" -o tunnelx  ./cmd/tunnelx

# Run test suite
go test ./... -count=1
```

---

## License

See [LICENSE](LICENSE) for details.

