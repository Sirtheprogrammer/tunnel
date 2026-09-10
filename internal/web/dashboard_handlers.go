package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"tunnel/internal/names"
	"tunnel/internal/store"
)

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request, user *store.Account) {
	ctx := r.Context()

	tokens, err := h.cfg.Store.ListTokens(ctx, user.ID)
	if err != nil {
		h.log.Warn("list tokens error", "account", user.ID, "error", err)
	}

	// Filter out revoked tokens for cleaner view
	var activeTokens []*store.Token
	for _, t := range tokens {
		if !t.Revoked {
			activeTokens = append(activeTokens, t)
		}
	}

	reservations, err := h.cfg.Store.ListReservations(ctx, user.ID)
	if err != nil {
		h.log.Warn("list reservations error", "account", user.ID, "error", err)
	}

	sessions, err := h.cfg.Store.ListSessionsForAccount(ctx, user.ID, 25)
	if err != nil {
		h.log.Warn("list sessions error", "account", user.ID, "error", err)
	}

	createdToken := r.URL.Query().Get("created_token")

	h.render(w, "dashboard.html", PageData{
		User:         user,
		Tokens:       activeTokens,
		Reservations: reservations,
		Sessions:     sessions,
		CreatedToken: createdToken,
		FlashError:   r.URL.Query().Get("error"),
		FlashSuccess: r.URL.Query().Get("success"),
	})
}

func (h *Handler) handleCreateToken(w http.ResponseWriter, r *http.Request, user *store.Account) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard?error=Invalid+request", http.StatusSeeOther)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	plaintext, _, err := h.cfg.Store.NewAccountToken(r.Context(), user.ID, name)
	if err != nil {
		h.log.Error("generate token failed", "error", err)
		http.Redirect(w, r, "/dashboard?error=Failed+to+generate+token", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/dashboard?created_token="+url.QueryEscape(plaintext), http.StatusSeeOther)
}

func (h *Handler) handleRevokeToken(w http.ResponseWriter, r *http.Request, user *store.Account) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard?error=Invalid+request", http.StatusSeeOther)
		return
	}

	tokenID := strings.TrimSpace(r.FormValue("token_id"))
	if tokenID == "" {
		http.Redirect(w, r, "/dashboard?error=Token+ID+required", http.StatusSeeOther)
		return
	}

	if err := h.cfg.Store.RevokeToken(r.Context(), tokenID); err != nil {
		h.log.Error("revoke token failed", "error", err)
		http.Redirect(w, r, "/dashboard?error=Failed+to+revoke+token", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/dashboard?success=Token+revoked+successfully", http.StatusSeeOther)
}

func (h *Handler) handleReserveSubdomain(w http.ResponseWriter, r *http.Request, user *store.Account) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard?error=Invalid+request", http.StatusSeeOther)
		return
	}

	subdomain := strings.ToLower(strings.TrimSpace(r.FormValue("subdomain")))
	validated, err := names.Validate(subdomain)
	if err != nil {
		http.Redirect(w, r, "/dashboard?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	if _, err := h.cfg.Store.ReserveSubdomain(r.Context(), validated, user.ID); err != nil {
		http.Redirect(w, r, "/dashboard?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/dashboard?success=Subdomain+reserved+successfully", http.StatusSeeOther)
}

func (h *Handler) handleReleaseSubdomain(w http.ResponseWriter, r *http.Request, user *store.Account) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard?error=Invalid+request", http.StatusSeeOther)
		return
	}

	subdomain := strings.ToLower(strings.TrimSpace(r.FormValue("subdomain")))
	if err := h.cfg.Store.ReleaseSubdomain(r.Context(), subdomain); err != nil {
		http.Redirect(w, r, "/dashboard?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/dashboard?success=Subdomain+released", http.StatusSeeOther)
}

// handleInstallScript serves a dynamic bash script for curl -fsSL https://domain/install.sh | bash
func (h *Handler) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	script := fmt.Sprintf(`#!/bin/sh
# TunnelX CLI Installer
set -e

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS (Windows users: install binary from dashboard)"; exit 1 ;;
esac

INSTALL_DIR="/usr/local/bin"
if [ ! -w "$INSTALL_DIR" ]; then
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
fi

echo "Downloading tunnelx for ${OS}/${ARCH}..."
URL="https://github.com/Sirtheprogrammer/tunnel/releases/latest/download/tunnelx-${OS}-${ARCH}.tar.gz"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

if curl -fsSL "$URL" -o "$TMP_DIR/tunnelx.tar.gz" 2>/dev/null; then
  tar -xzf "$TMP_DIR/tunnelx.tar.gz" -C "$TMP_DIR"
  chmod +x "$TMP_DIR/tunnelx"
  mv "$TMP_DIR/tunnelx" "$INSTALL_DIR/tunnelx"
  echo "✓ tunnelx installed successfully to $INSTALL_DIR/tunnelx"
  echo "Run 'tunnelx --help' to get started."
else
  echo "Could not download pre-built release package from $URL"
  echo "You can build directly with: go install tunnel/cmd/tunnelx@latest"
fi
`)
	_, _ = w.Write([]byte(script))
}

// handleInstallPS1 serves a dynamic PowerShell script for Windows: irm https://domain/install.ps1 | iex
func (h *Handler) handleInstallPS1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	script := `$ErrorActionPreference = 'Stop'
$Repo = "Sirtheprogrammer/tunnel"
$BinaryName = "tunnelx.exe"
$Arch = "amd64"
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { $Arch = "arm64" }

Write-Host "=> Installing TunnelX CLI for windows-$Arch..." -ForegroundColor Cyan
$Version = "v1.0.0"
try {
    $Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
    if ($Release.tag_name) { $Version = $Release.tag_name }
} catch {}

$DownloadUrl = "https://github.com/$Repo/releases/download/$Version/tunnelx-$Version-windows-$Arch.zip"
$InstallDir = Join-Path $env:LocalAppData "Programs\tunnelx"
if (-not (Test-Path $InstallDir)) { New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null }

$TempZip = Join-Path $env:TEMP "tunnelx.zip"
Invoke-WebRequest -Uri $DownloadUrl -OutFile $TempZip -UseBasicParsing
$TempExtract = Join-Path $env:TEMP "tunnelx_extracted"
Expand-Archive -Path $TempZip -DestinationPath $TempExtract -Force

$FoundBin = Get-ChildItem -Path $TempExtract -Filter "tunnelx.exe" -Recurse | Select-Object -First 1
if ($FoundBin) {
    Copy-Item -Path $FoundBin.FullName -Destination (Join-Path $InstallDir "tunnelx.exe") -Force
    Remove-Item -Path $TempZip -Force -ErrorAction SilentlyContinue
    Remove-Item -Path $TempExtract -Recurse -Force -ErrorAction SilentlyContinue

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
        $env:Path = "$env:Path;$InstallDir"
    }
    Write-Host "✓ Successfully installed tunnelx to $InstallDir\tunnelx.exe" -ForegroundColor Green
    Write-Host "Run 'tunnelx --help' to get started." -ForegroundColor Cyan
} else {
    Write-Error "Binary not found in archive"
}
`
	_, _ = w.Write([]byte(script))
}
