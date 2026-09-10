package server

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
)

// ACMEConfig configures automatic certificate issuance for the wildcard
// domain via Let's Encrypt's DNS-01 challenge.
//
// DNS-01 is required (not HTTP-01) because a wildcard certificate cannot be
// issued any other way, and because the origin is DNS-only (grey-cloud) rather
// than proxied: see the "Edge" decision in the project plan. Cloudflare is the
// only DNS provider wired in for now; the field name says so rather than
// pretending this is provider-agnostic.
type ACMEConfig struct {
	// Domain is the base domain to obtain a certificate for. Both the apex
	// (needed because a wildcard cert does not cover it) and the wildcard are
	// requested: Domain and "*."+Domain.
	Domain string

	// Email is given to Let's Encrypt for expiry notices. Required by our
	// agreement with the ACME CA, not used for anything else.
	Email string

	// CloudflareAPIToken authorises the DNS-01 challenge. It needs Zone:DNS:Edit
	// on the zone that owns Domain -- nothing broader. A token, not the legacy
	// global API key: a leaked token is scoped to one zone, a leaked key is not.
	CloudflareAPIToken string

	// CacheDir stores issued certificates and account keys on disk so a
	// restart does not re-issue (and does not hit Let's Encrypt's rate limits).
	CacheDir string

	// Staging routes requests through Let's Encrypt's staging CA, which issues
	// untrusted certificates but has far higher rate limits. Use it while
	// testing the DNS-01 wiring itself, before pointing at the real CA.
	Staging bool
}

// BuildACMETLSConfig obtains (or loads a cached) wildcard certificate and
// returns a *tls.Config that serves it, renewing automatically in the
// background for as long as the process runs.
//
// This blocks until the initial certificate is issued (or loaded from cache),
// since a server with no certificate has nothing useful to do; callers should
// not call this on every request or connection. Renewal happens automatically
// in the background for the lifetime of the process -- the returned
// *tls.Config always serves a current certificate without further calls here.
func BuildACMETLSConfig(ctx context.Context, cfg ACMEConfig) (*tls.Config, error) {
	if cfg.Domain == "" {
		return nil, fmt.Errorf("acme: Domain is required")
	}
	if cfg.Email == "" {
		return nil, fmt.Errorf("acme: Email is required")
	}
	if cfg.CloudflareAPIToken == "" {
		return nil, fmt.Errorf("acme: CloudflareAPIToken is required")
	}
	if cfg.CacheDir == "" {
		return nil, fmt.Errorf("acme: CacheDir is required")
	}

	certmagic.DefaultACME.Agreed = true
	certmagic.DefaultACME.Email = cfg.Email
	certmagic.DefaultACME.DNS01Solver = &certmagic.DNS01Solver{
		DNSManager: certmagic.DNSManager{
			DNSProvider: &cloudflare.Provider{APIToken: cfg.CloudflareAPIToken},
		},
	}
	if cfg.Staging {
		certmagic.DefaultACME.CA = certmagic.LetsEncryptStagingCA
	}
	certmagic.Default.Storage = &certmagic.FileStorage{Path: cfg.CacheDir}

	magic := certmagic.NewDefault()
	// The apex is requested alongside the wildcard because a certificate for
	// "*.tl.codesky.tech" does not cover "tl.codesky.tech" itself, and we serve
	// a landing/404 page there too.
	domains := []string{cfg.Domain, "*." + cfg.Domain}
	if err := magic.ManageSync(ctx, domains); err != nil {
		return nil, fmt.Errorf("acme: obtain certificate for %v: %w", domains, err)
	}
	return magic.TLSConfig(), nil
}
