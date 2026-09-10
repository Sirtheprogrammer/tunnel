package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"tunnel/internal/server"
	"tunnel/internal/store"
	"tunnel/internal/web"
)

// defaultCFTokenEnv names the environment variable holding the Cloudflare API
// token for ACME DNS-01. Naming the variable rather than taking the token
// directly as a flag keeps it out of shell history and `ps`.
const defaultCFTokenEnv = "CF_API_TOKEN"

func serveCmd() *cobra.Command {
	var (
		domain         string
		controlAddr    string
		httpAddr       string
		httpsAddr      string
		publicURL      string
		tlsMode        string
		certFile       string
		keyFile        string
		acmeEmail      string
		acmeCFTokenEnv string
		acmeCacheDir   string
		acmeStaging    bool
		devMode        bool
		maxTunnels     int
		leaseTTL       time.Duration
		verbose        bool
		dbPath         string
		allowAnonymous bool
		enablePortal   bool
		ghClientID     string
		ghClientSecret string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the tunnel server",
		Long: "Run the tunnel server.\n\n" +
			"--tls-mode file (the default) takes a wildcard certificate for *.<domain>\n" +
			"via --tls-cert/--tls-key, e.g. a Cloudflare Origin Certificate.\n\n" +
			"--tls-mode acme obtains and renews a free wildcard certificate from Let's\n" +
			"Encrypt automatically, using a DNS-01 challenge against Cloudflare. This\n" +
			"needs a Cloudflare API token scoped to Zone:DNS:Edit on <domain>'s zone,\n" +
			"read from the environment variable named by --acme-cf-token-env.\n\n" +
			"For local development, --dev serves plain HTTP and accepts agent\n" +
			"connections without TLS, ignoring --tls-mode.",
		Example: "  tunnelxd serve --domain tl.codesky.tech --tls-cert origin.pem --tls-key origin.key\n" +
			"  CF_API_TOKEN=... tunnelxd serve --domain tl.codesky.tech --tls-mode acme --acme-email you@codesky.tech\n" +
			"  tunnelxd serve --dev --domain lvh.me --https-addr :8443",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

			var tlsCfg *tls.Config
			scheme := "https"
			switch {
			case devMode:
				scheme = "http"
				log.Warn("running in development mode: no TLS on either listener")

			case tlsMode == "acme":
				if certFile != "" || keyFile != "" {
					return errors.New("--tls-cert/--tls-key are not used with --tls-mode acme")
				}
				if acmeEmail == "" {
					return errors.New("--acme-email is required with --tls-mode acme")
				}
				token := os.Getenv(acmeCFTokenEnv)
				if token == "" {
					return fmt.Errorf(
						"environment variable %s is not set; it must hold a Cloudflare API "+
							"token with Zone:DNS:Edit on the zone that owns %s",
						acmeCFTokenEnv, domain)
				}
				log.Info("requesting certificate via ACME DNS-01; this blocks until issued",
					"domain", domain, "staging", acmeStaging)
				cfg, err := server.BuildACMETLSConfig(cmd.Context(), server.ACMEConfig{
					Domain:             domain,
					Email:              acmeEmail,
					CloudflareAPIToken: token,
					CacheDir:           acmeCacheDir,
					Staging:            acmeStaging,
				})
				if err != nil {
					return fmt.Errorf("acme: %w", err)
				}
				tlsCfg = cfg
				log.Info("certificate ready")

			case tlsMode == "file":
				if certFile == "" || keyFile == "" {
					return errors.New(
						"--tls-cert and --tls-key are required with --tls-mode file (the default) unless --dev is set")
				}
				cert, err := tls.LoadX509KeyPair(certFile, keyFile)
				if err != nil {
					return fmt.Errorf("load certificate: %w", err)
				}
				tlsCfg = &tls.Config{
					Certificates: []tls.Certificate{cert},
					MinVersion:   tls.VersionTLS12,
				}

			default:
				return fmt.Errorf("--tls-mode must be %q or %q, got %q", "file", "acme", tlsMode)
			}

			publicPort, err := portFromAddr(httpsAddr)
			if err != nil {
				return fmt.Errorf("--https-addr: %w", err)
			}

			if err := ensureDBDir(dbPath); err != nil {
				return fmt.Errorf("create database directory: %w", err)
			}
			db, err := store.Open(cmd.Context(), dbPath)
			if err != nil {
				return err
			}
			defer db.Close()

			auth := store.NewAuthenticator(db)
			if allowAnonymous {
				log.Warn("running with --allow-anonymous: agents without a valid authtoken " +
					"will connect as a shared account; do not use this on a public domain")
				auth.AllowAnonymous = true
				auth.AnonymousAccountID = "anonymous"
			}

			var webPortal http.Handler
			if enablePortal {
				wp, err := web.New(web.Config{
					Domain:             domain,
					Store:              db,
					GitHubClientID:     ghClientID,
					GitHubClientSecret: ghClientSecret,
					Logger:             log,
				})
				if err != nil {
					return fmt.Errorf("init web portal: %w", err)
				}
				webPortal = wp
			}

			srv, err := server.New(server.Config{
				Domain:               domain,
				ControlAddr:          controlAddr,
				MaxTunnelsPerAccount: maxTunnels,
				DisconnectLease:      leaseTTL,
				PublicURL:            publicURL,
				PublicScheme:         scheme,
				PublicPort:           publicPort,
				Auth:                 auth,
				SessionLogger:        db,
				WebPortal:            webPortal,
				TLSConfig:            tlsCfg,
				Logger:               log,
			})
			if err != nil {
				return err
			}
			defer srv.Close()

			ctlLn, err := net.Listen("tcp", controlAddr)
			if err != nil {
				return fmt.Errorf("listen on control address %s: %w", controlAddr, err)
			}

			g, ctx := errgroup.WithContext(cmd.Context())
			g.Go(func() error { return srv.ServeControl(ctx, ctlLn) })

			dataSrv := &http.Server{
				Addr:              httpsAddr,
				Handler:           srv.Handler(),
				TLSConfig:         tlsCfg,
				ReadHeaderTimeout: 20 * time.Second,
				// No WriteTimeout: tunnels legitimately carry long-lived
				// responses such as SSE streams and large downloads, and a
				// write deadline would sever them mid-flight.
				IdleTimeout: 120 * time.Second,
			}
			g.Go(func() error { return listenAndServe(ctx, dataSrv, tlsCfg != nil, log, "data") })

			if httpAddr != "" {
				redirect := &http.Server{
					Addr:              httpAddr,
					Handler:           srv.Handler(),
					ReadHeaderTimeout: 20 * time.Second,
				}
				if !devMode {
					redirect.Handler = srv.RedirectHandler()
				}
				g.Go(func() error { return listenAndServe(ctx, redirect, false, log, "redirect") })
			}

			log.Info("tunnelxd ready", "domain", domain, "control", controlAddr, "data", httpsAddr)
			if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&domain, "domain", "tl.codesky.tech", "base domain tunnels are published under")
	f.StringVar(&controlAddr, "control-addr", ":7835", "listen address for agent connections")
	f.StringVar(&httpsAddr, "https-addr", ":443", "listen address for public tunnel traffic")
	f.StringVar(&httpAddr, "http-addr", ":80", "listen address for the HTTP redirect (empty to disable)")
	f.StringVar(&publicURL, "public-url", "", "base URL for public tunnels (overrides scheme/port derived from listen address)")
	f.StringVar(&tlsMode, "tls-mode", "file", "certificate source: file (static cert/key) or acme (auto via Let's Encrypt DNS-01)")
	f.StringVar(&certFile, "tls-cert", "", "wildcard certificate for *.<domain> (--tls-mode file)")
	f.StringVar(&keyFile, "tls-key", "", "private key for --tls-cert (--tls-mode file)")
	f.StringVar(&acmeEmail, "acme-email", "", "contact email for Let's Encrypt expiry notices (--tls-mode acme)")
	f.StringVar(&acmeCFTokenEnv, "acme-cf-token-env", defaultCFTokenEnv,
		"environment variable holding the Cloudflare API token (--tls-mode acme)")
	f.StringVar(&acmeCacheDir, "acme-cache-dir", "acme-cache",
		"directory to store issued certificates and account keys (--tls-mode acme)")
	f.BoolVar(&acmeStaging, "acme-staging", false,
		"use Let's Encrypt's staging CA, for testing the DNS-01 wiring without hitting rate limits (--tls-mode acme)")
	f.BoolVar(&devMode, "dev", false, "serve without TLS, for local development (ignores --tls-mode)")
	f.IntVar(&maxTunnels, "max-tunnels-per-account", 4, "concurrent tunnel limit per account (0 for unlimited)")
	f.DurationVar(&leaseTTL, "subdomain-lease", 60*time.Second,
		"how long a subdomain is held for its owner after a disconnect")
	f.BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
	f.StringVar(&dbPath, "db", defaultDBPath, "path to the control-plane database")
	f.BoolVar(&allowAnonymous, "allow-anonymous", false,
		"accept agents without an authtoken, sharing one account (development only)")
	f.BoolVar(&enablePortal, "enable-portal", true, "serve web landing page and auth dashboard on apex domain")
	f.StringVar(&ghClientID, "github-client-id", os.Getenv("GITHUB_CLIENT_ID"), "GitHub OAuth Client ID")
	f.StringVar(&ghClientSecret, "github-client-secret", os.Getenv("GITHUB_CLIENT_SECRET"), "GitHub OAuth Client Secret")
	return cmd
}

// listenAndServe runs an HTTP server and shuts it down gracefully when ctx ends.
func listenAndServe(ctx context.Context, srv *http.Server, useTLS bool, log *slog.Logger, name string) error {
	go func() {
		<-ctx.Done()
		// Give in-flight requests a moment, but do not wait on streaming
		// responses that may never end on their own.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close()
		}
	}()

	log.Info("listener started", "name", name, "addr", srv.Addr, "tls", useTLS)
	var err error
	if useTLS {
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s listener: %w", name, err)
}

// portFromAddr extracts the port from a listen address so generated URLs carry
// it when it is not the scheme default.
func portFromAddr(addr string) (int, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	var n int
	if _, err := fmt.Sscanf(port, "%d", &n); err != nil {
		return 0, fmt.Errorf("%q is not a numeric port", port)
	}
	return n, nil
}
