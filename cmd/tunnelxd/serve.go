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
)

func serveCmd() *cobra.Command {
	var (
		domain      string
		controlAddr string
		httpAddr    string
		httpsAddr   string
		certFile    string
		keyFile     string
		devMode     bool
		maxTunnels  int
		leaseTTL    time.Duration
		verbose     bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the tunnel server",
		Long: "Run the tunnel server.\n\n" +
			"In production, supply a wildcard certificate for *.<domain> with\n" +
			"--tls-cert and --tls-key. For local development, --dev serves plain\n" +
			"HTTP and accepts agent connections without TLS.",
		Example: "  tunnelxd serve --domain tl.codesky.tech --tls-cert cert.pem --tls-key key.pem\n" +
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
			if devMode {
				scheme = "http"
				log.Warn("running in development mode: no TLS on either listener")
			} else {
				if certFile == "" || keyFile == "" {
					return errors.New("--tls-cert and --tls-key are required unless --dev is set")
				}
				cert, err := tls.LoadX509KeyPair(certFile, keyFile)
				if err != nil {
					return fmt.Errorf("load certificate: %w", err)
				}
				tlsCfg = &tls.Config{
					Certificates: []tls.Certificate{cert},
					MinVersion:   tls.VersionTLS12,
				}
			}

			publicPort, err := portFromAddr(httpsAddr)
			if err != nil {
				return fmt.Errorf("--https-addr: %w", err)
			}

			srv, err := server.New(server.Config{
				Domain:               domain,
				ControlAddr:          controlAddr,
				MaxTunnelsPerAccount: maxTunnels,
				DisconnectLease:      leaseTTL,
				PublicScheme:         scheme,
				PublicPort:           publicPort,
				// M2 replaces this with the SQLite-backed authenticator.
				Auth:      server.OpenAuth{},
				TLSConfig: tlsCfg,
				Logger:    log,
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
	f.StringVar(&certFile, "tls-cert", "", "wildcard certificate for *.<domain>")
	f.StringVar(&keyFile, "tls-key", "", "private key for --tls-cert")
	f.BoolVar(&devMode, "dev", false, "serve without TLS, for local development")
	f.IntVar(&maxTunnels, "max-tunnels-per-account", 4, "concurrent tunnel limit per account (0 for unlimited)")
	f.DurationVar(&leaseTTL, "subdomain-lease", 60*time.Second,
		"how long a subdomain is held for its owner after a disconnect")
	f.BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")
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
