# TunnelX — Complete Setup & Production Deployment Guide

> **TunnelX** is a self-hosted developer HTTP tunneling platform, client CLI, and developer web portal.
> It allows developers to expose local development servers to the public internet via secure, instant HTTPS subdomains (e.g. `https://myapp.tl.codesky.tech`), complete with automatic wildcard TLS certificates, WebSockets support, user accounts, and subdomain reservations.

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Prerequisites](#2-prerequisites)
3. [DNS Configuration (Cloudflare)](#3-dns-configuration-cloudflare)
4. [GitHub OAuth Configuration](#4-github-oauth-configuration)
5. [Production Deployment: Docker + Nginx (Recommended)](#5-production-deployment-docker--nginx-recommended)
   - [Why Nginx Stream TLS Passthrough?](#why-nginx-stream-tls-passthrough)
   - [Step 1: Clone Repository & Configure Environment](#step-1-clone-repository--configure-environment)
   - [Step 2: Configure Nginx](#step-2-configure-nginx)
   - [Step 3: Launch TunnelX via Docker Compose](#step-3-launch-tunnelx-via-docker-compose)
   - [Step 4: Verify Deployment & Logs](#step-4-verify-deployment--logs)
6. [Alternative Deployment: Standalone Docker (No Nginx)](#6-alternative-deployment-standalone-docker-no-nginx)
7. [Alternative Deployment: Bare Metal (systemd)](#7-alternative-deployment-bare-metal-systemd)
8. [Web Portal & User Management](#8-web-portal--user-management)
9. [Client Installation & Usage](#9-client-installation--usage)
   - [One-Line Installers](#one-line-installers)
   - [CLI Commands & Examples](#cli-commands--examples)
10. [Server Administration CLI](#10-server-administration-cli)
11. [Configuration Reference](#11-configuration-reference)
12. [Troubleshooting & Common Pitfalls](#12-troubleshooting--common-pitfalls)

---

## 1. Architecture Overview

```
┌──────────────────────────────────────────────┐
│             Developer Machine                │
│                                              │
│  ┌────────────────────────┐                  │
│  │   tunnelx (Client CLI) │                  │
│  └───────────┬────────────┘                  │
│              │                               │
│              ▼ (Local forwarding)            │
│  ┌────────────────────────┐                  │
│  │ Local Dev Server       │                  │
│  │ (e.g. localhost:3000)  │                  │
│  └────────────────────────┘                  │
└──────────────────────┬───────────────────────┘
                       │
                       │ TLS yamux multiplexed control stream
                       │ Port 7835 (Direct to VPS)
                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           VPS / Cloud Server                                │
│                                                                             │
│   ┌─────────────────────────────────────────────────────────────────────┐   │
│   │                         Nginx Host Proxy                            │   │
│   │                                                                     │   │
│   │   :80 (HTTP)           ───────────────▶ Proxy to 127.0.0.1:8080     │   │
│   │   (Redirect to HTTPS)                   (tunnelxd handles 301)      │   │
│   │                                                                     │   │
│   │   :443 (HTTPS)          ───────────────▶ SNI Stream TLS Passthrough │   │
│   │   (*.tl.codesky.tech)                   Raw TCP to 127.0.0.1:8443   │   │
│   │                                         (tunnelxd terminates TLS)   │   │
│   └────────────────────────────────────────────────┬────────────────────┘   │
│                                                    │                        │
│                                                    ▼                        │
│   ┌─────────────────────────────────────────────────────────────────────┐   │
│   │                     tunnelxd (Docker Container)                     │   │
│   │                                                                     │   │
│   │   - Internal Port 8080 : HTTP → HTTPS redirect listener             │   │
│   │   - Internal Port 8443 : Public HTTPS Wildcard & Web Portal engine  │   │
│   │   - Public Port 7835   : Agent Control listener (yamux multiplexer) │   │
│   │   - ACME DNS-01 Engine : Automatically manages Let's Encrypt certs  │   │
│   │   - SQLite Database    : /var/lib/tunnelx/tunnelx.db                │   │
│   │                          (Users, Hashed Tokens, Reserved Names)     │   │
│   └─────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

### How It Works:
1. **Developer Connection**: `tunnelx` connects to `tunnelxd` on port **7835** using TLS. Inside this connection, Yamux provides bi-directional multiplexing (1 control stream + N concurrent HTTP/WebSocket streams).
2. **Authentication**: `tunnelx` authenticates using an authtoken and requests a subdomain (either random or reserved).
3. **Public Traffic**:
   - HTTP requests on port **80** are redirected to **HTTPS**.
   - HTTPS requests on port **443** hit Nginx, which uses SNI preread to pass raw TLS to `tunnelxd` (`127.0.0.1:8443`).
   - If the request is for the apex domain (`https://tl.codesky.tech`), `tunnelxd` renders the built-in **Developer Web Portal**.
   - If the request is for an active tunnel subdomain (`https://myapp.tl.codesky.tech`), `tunnelxd` routes the request over the Yamux stream to the developer's laptop, which forwards it to `localhost:3000` and streams the response back.

---

## 2. Prerequisites

| Component | Requirement | Note |
|-----------|-------------|------|
| **OS** | Linux (Ubuntu 22.04+, Debian 12+, CentOS Stream) | amd64 or arm64 |
| **RAM** | 512 MB minimum (1 GB recommended) | Lightweight Go binary |
| **Docker** | Docker Engine 24+ & Docker Compose v2 | Recommended deployment method |
| **Nginx** | 1.18+ with `stream` module enabled | Standard on modern Linux distros |
| **Domain** | A domain managed in **Cloudflare DNS** | Required for automated Let's Encrypt DNS-01 |
| **Open Ports** | `80/tcp`, `443/tcp`, `7835/tcp` | Must be open in VPS firewall & cloud security groups |

---

## 3. DNS Configuration (Cloudflare)

`tunnelxd` uses **ACME DNS-01 challenges** through Cloudflare to obtain wildcard certificates (`*.yourdomain.com` and `yourdomain.com`). This allows issuing valid certificates for any dynamic subdomain on the fly.

### 1. Create DNS Records in Cloudflare
Go to your domain's **DNS Management** in Cloudflare and create two records:

| Type | Name | IPv4 Address | Proxy status | TTL |
|------|------|--------------|--------------|-----|
| `A` | `tl` *(or `@` for apex)* | `<YOUR_VPS_IP>` | **DNS only (Grey Cloud)** | Auto |
| `A` | `*.tl` *(or `*` for apex)* | `<YOUR_VPS_IP>` | **DNS only (Grey Cloud)** | Auto |

> [!IMPORTANT]
> **Proxy status MUST be "DNS only" (Grey Cloud).**
> Do not enable Cloudflare Orange Cloud proxy for TunnelX:
> 1. Cloudflare proxy blocks non-HTTP ports like `7835` (used by the agent control stream).
> 2. Cloudflare proxy interferes with direct ACME DNS-01 TLS termination.

### 2. Create Cloudflare API Token
1. Go to **Cloudflare Dashboard &rarr; My Profile &rarr; API Tokens**.
2. Click **Create Token** &rarr; select **Create Custom Token**.
3. Configure the token:
   - **Token name**: `TunnelX DNS-01`
   - **Permissions**:
     - `Zone` &rarr; `DNS` &rarr; `Edit`
   - **Zone Resources**:
     - `Include` &rarr; `Specific zone` &rarr; `yourdomain.com`
4. Click **Continue to summary** &rarr; **Create Token**.
5. Copy the generated token string. You will need it as `CF_API_TOKEN`.

---

## 4. GitHub OAuth Configuration

TunnelX includes a built-in Developer Web Portal with GitHub OAuth authentication so developers can log in with one click to manage their tokens and subdomains.

1. Go to **GitHub &rarr; Settings &rarr; Developer Settings &rarr; OAuth Apps**.
2. Click **New OAuth App**.
3. Fill in the application details:
   - **Application name**: `TunnelX`
   - **Homepage URL**: `https://tl.codesky.tech` *(replace with your domain)*
   - **Application description**: `Developer tunneling portal`
   - **Authorization callback URL**: `https://tl.codesky.tech/auth/github/callback` *(replace with your domain)*
4. Click **Register application**.
5. Copy the **Client ID**.
6. Click **Generate a new client secret** and copy the secret.

You will set these in `deploy/.env` as `GITHUB_CLIENT_ID` and `GITHUB_CLIENT_SECRET`.

---

## 5. Production Deployment: Docker + Nginx (Recommended)

This is the battle-tested configuration used in production on `tl.codesky.tech`.

### Why Nginx Stream TLS Passthrough?

On most production VPS servers, Nginx is already bound to ports 80 and 443 (or you may have other websites running alongside TunnelX).

Using **SNI Stream TLS Passthrough**:
1. Port 80 traffic is passed to `tunnelxd` (`:8080`), allowing `tunnelxd` to handle clean HTTP &rarr; HTTPS redirection.
2. Port 443 traffic uses Nginx's `stream_ssl_preread` module to inspect the TLS SNI domain header **without decrypting it**, passing raw TLS packets straight to `tunnelxd` (`:8443`).
3. `tunnelxd` handles all wildcard certificate acquisition, rotation, and Let's Encrypt renewals internally. You do **not** need to manage Certbot or copy certificates on the host!

```
Public Request → VPS Port 443 → Nginx Stream (SNI Inspection) → tunnelxd Container :8443 (TLS Terminated)
```

---

### Step 1: Clone Repository & Configure Environment

SSH into your server:

```bash
git clone https://github.com/Sirtheprogrammer/tunnel.git ~/tunnel
cd ~/tunnel

# Copy the example environment file
cp deploy/.env.example deploy/.env
```

Edit `deploy/.env`:

```env
# Your base tunnel domain
TUNNEL_DOMAIN=tl.codesky.tech

# Email for Let's Encrypt expiry notifications
ACME_EMAIL=admin@yourdomain.com

# Cloudflare API Token with Zone:DNS:Edit permissions
CF_API_TOKEN=your_cloudflare_api_token_here

# TLS mode (acme for automatic wildcard Let's Encrypt)
TLS_MODE=acme

# GitHub OAuth credentials (for web portal login)
GITHUB_CLIENT_ID=your_github_oauth_client_id
GITHUB_CLIENT_SECRET=your_github_oauth_client_secret
```

---

### Step 2: Configure Nginx

#### 1. Enable Nginx Stream Module
Verify your Nginx installation supports the stream module:
```bash
nginx -V 2>&1 | grep stream_ssl_preread
```
*(Ubuntu and Debian default `nginx` package includes stream support. If missing, install with `sudo apt install libnginx-mod-stream`).*

Open `/etc/nginx/nginx.conf`:
```bash
sudo nano /etc/nginx/nginx.conf
```
Add the `stream` configuration block at the **top level** (outside of the `http { ... }` block):

```nginx
stream {
    include /etc/nginx/stream.d/*.conf;
}
```

#### 2. Create the Stream Passthrough Configuration
Create `/etc/nginx/stream.d/tunnelx.conf`:
```bash
sudo mkdir -p /etc/nginx/stream.d
sudo cp deploy/nginx-tunnelx-stream.conf /etc/nginx/stream.d/tunnelx.conf
```

Verify `/etc/nginx/stream.d/tunnelx.conf` contents:
```nginx
map $ssl_preread_server_name $tunnelx_backend {
    # Match your domain and all subdomains
    ~^(.+\.)?tl\.codesky\.tech$    tunnelxd_tls;
    default                         tunnelxd_tls;
}

upstream tunnelxd_tls {
    server 127.0.0.1:8443;
}

server {
    listen 443;
    listen [::]:443;
    ssl_preread on;
    proxy_pass $tunnelx_backend;
}
```
*(Replace `tl\.codesky\.tech` with your actual domain escaping dots with `\.`).*

#### 3. Create the HTTP Redirect Configuration
Copy the HTTP redirect block:
```bash
sudo cp deploy/nginx-tunnelx.conf /etc/nginx/sites-available/tunnelx
sudo ln -sf /etc/nginx/sites-available/tunnelx /etc/nginx/sites-enabled/tunnelx
```

Verify `/etc/nginx/sites-available/tunnelx` contents:
```nginx
server {
    listen 80;
    listen [::]:80;

    server_name tl.codesky.tech *.tl.codesky.tech;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```
*(Replace `tl.codesky.tech` with your actual domain).*

#### 4. Test & Reload Nginx
Remove default site if it conflicts on port 80:
```bash
sudo rm -f /etc/nginx/sites-enabled/default

# Test Nginx syntax
sudo nginx -t

# Reload Nginx
sudo systemctl reload nginx
```

---

### Step 3: Launch TunnelX via Docker Compose

In `deploy/docker-compose.yml`, `tunnelxd` is configured with:
- Ports `127.0.0.1:8080:8080` (HTTP redirect from Nginx)
- Ports `127.0.0.1:8443:8443` (Raw TLS stream from Nginx)
- Port `7835:7835` (Agent control listener, bound to 0.0.0.0)
- Volume `tunnelx-data:/var/lib/tunnelx` (Persists SQLite DB and ACME certificate cache)

Start the container:
```bash
cd ~/tunnel
docker compose -f deploy/docker-compose.yml up -d --build
```

---

### Step 4: Verify Deployment & Logs

Follow the container logs to observe certificate acquisition:
```bash
docker compose -f deploy/docker-compose.yml logs -f
```

You should see log output similar to:
```
tunnelxd  | time=... level=INFO msg="requesting certificate via ACME DNS-01; this blocks until issued" domain=tl.codesky.tech staging=false
tunnelxd  | time=... level=INFO msg="certificate ready"
tunnelxd  | time=... level=INFO msg="tunnelxd ready" domain=tl.codesky.tech control=:7835 data=:8443
tunnelxd  | time=... level=INFO msg="control listener started" addr=[::]:7835
tunnelxd  | time=... level=INFO msg="listener started" name=redirect addr=:8080 tls=false
tunnelxd  | time=... level=INFO msg="listener started" name=data addr=:8443 tls=true
```

> [!NOTE]
> On the first start, Let's Encrypt DNS-01 verification takes 30–60 seconds for Cloudflare TXT records to propagate. Subsequent starts use the cached certificate from `/var/lib/tunnelx/acme-cache` and start instantly.

Test the health endpoint:
```bash
curl -s https://tl.codesky.tech/healthz
# Response: {"status":"ok","tunnels":0}
```

---

## 6. Alternative Deployment: Standalone Docker (No Nginx)

If your VPS is dedicated purely to TunnelX and does **not** run Nginx, you can bind Docker directly to ports 80 and 443.

Edit `deploy/docker-compose.yml`:
```yaml
    ports:
      - "80:8080"
      - "443:8443"
      - "7835:7835"
```

Then run:
```bash
docker compose -f deploy/docker-compose.yml up -d
```

---

## 7. Alternative Deployment: Bare Metal (systemd)

For deploying directly on the host without Docker:

### 1. Build and Install Binary
```bash
go build -trimpath -ldflags "-s -w" -o /usr/local/bin/tunnelxd ./cmd/tunnelxd
```

### 2. Create Service User and Directories
```bash
sudo useradd --system --home-dir /var/lib/tunnelx --shell /usr/sbin/nologin tunnelxd
sudo install -d -o tunnelxd -g tunnelxd -m 0750 /var/lib/tunnelx
```

### 3. Configure Environment & systemd Unit
Create `/etc/tunnelx/tunnelxd.env`:
```bash
sudo mkdir -p /etc/tunnelx
sudo tee /etc/tunnelx/tunnelxd.env << 'EOF'
CF_API_TOKEN=your_cloudflare_api_token
GITHUB_CLIENT_ID=your_client_id
GITHUB_CLIENT_SECRET=your_client_secret
EOF
sudo chmod 0600 /etc/tunnelx/tunnelxd.env
sudo chown tunnelxd:tunnelxd /etc/tunnelx/tunnelxd.env
```

Install `deploy/tunnelxd.service`:
```bash
sudo cp deploy/tunnelxd.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tunnelxd
sudo journalctl -u tunnelxd -f
```

---

## 8. Web Portal & User Management

### Web Portal Access
Open your browser and navigate to:
```
https://tl.codesky.tech
```

You will see the TunnelX landing page:
- **Sign In with GitHub** or **Register via Email/Password**.
- Once logged in, the dashboard allows you to:
  1. **Generate Authtokens**: Label your devices (e.g. `Work MacBook`, `Home PC`).
  2. **Reserve Subdomains**: Lock in names like `https://myapp.tl.codesky.tech`.
  3. **Monitor Sessions**: View live and historical tunnel connections in real time.
  4. **Developer Quickstart**: View one-line setup instructions.

### CLI User Administration (Alternative)
You can also manage accounts directly from the server CLI using `docker exec`:

```bash
# Create user and generate first authtoken
docker exec -it tunnelxd /usr/local/bin/tunnelxd users create developer@example.com --db /var/lib/tunnelx/tunnelx.db

# List users
docker exec -it tunnelxd /usr/local/bin/tunnelxd users list --db /var/lib/tunnelx/tunnelx.db

# Issue an additional token for an existing user
docker exec -it tunnelxd /usr/local/bin/tunnelxd tokens issue developer@example.com --name "Laptop" --db /var/lib/tunnelx/tunnelx.db

# Reserve a subdomain for a user
docker exec -it tunnelxd /usr/local/bin/tunnelxd reserve myapp developer@example.com --db /var/lib/tunnelx/tunnelx.db
```

---

## 9. Client Installation & Usage

### One-Line Installers

Run the automated installer on the client machine:

#### Linux & macOS
```bash
curl -fsSL https://tl.codesky.tech/install.sh | bash
```

#### Windows (PowerShell)
```powershell
irm https://tl.codesky.tech/install.ps1 | iex
```

---

### CLI Commands & Examples

#### 1. Authenticate with the Server
```bash
tunnelx login <YOUR_AUTHTOKEN> --server tl.codesky.tech:7835
```
Credentials are saved to `~/.tunnelx/config.yaml`.

#### 2. Verify Client Configuration
```bash
tunnelx config check
```

#### 3. Start a Basic HTTP Tunnel
Forward local port 3000:
```bash
tunnelx http 3000
```
Output:
```
tunnelx v1.0.0 ready
Forwarding  https://brave-otter-7f3a.tl.codesky.tech → http://localhost:3000
```

#### 4. Forward with a Custom / Reserved Subdomain
```bash
tunnelx http 3000 --subdomain myapp
```
Creates `https://myapp.tl.codesky.tech`.

#### 5. Forward Vite, Next.js, or Webpack Dev Servers
Dev servers often enforce strict `Host` header checks. Use `--host-header rewrite`:
```bash
tunnelx http 5173 --host-header rewrite
```

#### 6. Forward a Non-Local or Specific IP Address
```bash
tunnelx http 192.168.1.100:8080
```

#### 7. Debugging / Verbose Output
```bash
tunnelx http 3000 -v
```

#### 8. Log Out
```bash
tunnelx logout
```

---

## 10. Server Administration CLI

When running via Docker, prefix commands with `docker exec -it tunnelxd /usr/local/bin/tunnelxd`:

| Task | Command |
|------|---------|
| Start Server | `tunnelxd serve --domain=example.com --tls-mode=acme ...` |
| Create Account | `tunnelxd users create <email> --db=<path>` |
| List Accounts | `tunnelxd users list --db=<path>` |
| Disable Account | `tunnelxd users disable <email> --db=<path>` |
| Enable Account | `tunnelxd users enable <email> --db=<path>` |
| Issue Token | `tunnelxd tokens issue <email> --name="Mac" --db=<path>` |
| List Tokens | `tunnelxd tokens list <email> --db=<path>` |
| Revoke Token | `tunnelxd tokens revoke <token_id> --db=<path>` |
| Reserve Subdomain | `tunnelxd reserve <subdomain> <email> --db=<path>` |
| List Reservations | `tunnelxd reserve list <email> --db=<path>` |
| Release Subdomain | `tunnelxd reserve release <subdomain> --db=<path>` |

---

## 11. Configuration Reference

### Server Configuration (`tunnelxd serve`)

| Flag | Default | Description |
|------|---------|-------------|
| `--domain` | `tl.codesky.tech` | Base domain for tunnels and portal |
| `--public-url` | *(derived)* | Public base URL (e.g. `https://tl.codesky.tech`) |
| `--control-addr` | `:7835` | Listen address for client agent connections |
| `--https-addr` | `:443` | Listen address for HTTPS public traffic |
| `--http-addr` | `:80` | Listen address for HTTP &rarr; HTTPS redirects |
| `--tls-mode` | `file` | TLS mode: `acme`, `file`, or `--dev` |
| `--acme-email` | | Required for ACME: Email for Let's Encrypt |
| `--acme-cf-token-env` | `CF_API_TOKEN` | Name of env var holding Cloudflare token |
| `--acme-cache-dir` | `acme-cache` | Directory to cache issued TLS certs |
| `--acme-staging` | `false` | Use Let's Encrypt Staging environment |
| `--db` | `tunnelx.db` | Path to SQLite database file |
| `--max-tunnels-per-account` | `4` | Max concurrent active tunnels per account |
| `--subdomain-lease` | `60s` | Reconnect lease time for subdomains |
| `--allow-anonymous` | `false` | Allow clients to connect without tokens |

### Client Configuration (`~/.tunnelx/config.yaml`)

```yaml
server_addr: tl.codesky.tech:7835
token: tx_7a8f9c...
ca_cert: ""        # Optional custom CA PEM path
insecure: false    # Skip TLS certificate validation (dev only)
```

---

## 12. Troubleshooting & Common Pitfalls

### 1. `Client sent an HTTP request to an HTTPS server`
- **Cause**: Trying to access `http://domain:8443` or `http://domain:443` with plain HTTP instead of HTTPS, or Nginx is using `proxy_pass http://` to the HTTPS port `8443`.
- **Fix**: Ensure Nginx uses the **stream module** (`deploy/nginx-tunnelx-stream.conf`) for port 443 so raw TLS passes through, and use `http://` only on port 80.

### 2. Nginx error: `unknown directive "stream"`
- **Cause**: The Nginx stream module is not loaded.
- **Fix**: On Ubuntu/Debian run `sudo apt install libnginx-mod-stream`. Ensure `stream { include /etc/nginx/stream.d/*.conf; }` is placed in `/etc/nginx/nginx.conf` at the root level, not inside `http { ... }`.

### 3. `connection refused` on port 7835 from client
- **Cause**: Port 7835 is blocked by a firewall or cloud security group.
- **Fix**: 
  - Ubuntu UFW: `sudo ufw allow 7835/tcp`
  - Cloud providers (AWS, GCP, Hetzner, Contabo): Add an inbound firewall rule permitting TCP traffic on port `7835`.

### 4. Let's Encrypt ACME DNS-01 errors or timeout
- **Cause**: Invalid Cloudflare API token or wrong permissions.
- **Fix**:
  - Ensure the Cloudflare token has **Zone &rarr; DNS &rarr; Edit** permission.
  - Verify Cloudflare DNS records for `tl` and `*.tl` are set to **DNS Only (Grey Cloud)**, NOT Proxied (Orange Cloud).

### 5. Vite or Next.js dev server returns `Invalid Host header` / `Blocked request`
- **Cause**: The dev server validates the HTTP `Host` header against `localhost`.
- **Fix**: Run `tunnelx http 5173 --host-header rewrite`.



