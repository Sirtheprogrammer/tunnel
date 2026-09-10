package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"tunnel/internal/agent"
	"tunnel/internal/config"
)

func httpCmd() *cobra.Command {
	var (
		subdomain  string
		hostHeader string
		serverAddr string
		token      string
		insecure   bool
		noTLS      bool
		verbose    bool
		caCert     string
	)

	cmd := &cobra.Command{
		Use:   "http <port | host:port | url>",
		Short: "Forward a public HTTPS URL to a local HTTP service",
		Long: "Forward a public HTTPS URL to a local HTTP service.\n\n" +
			"The target can be a bare port (3000), an address (localhost:3000),\n" +
			"or a URL (http://127.0.0.1:3000).",
		Example: "  tunnelx http 3000\n" +
			"  tunnelx http localhost:8080 --subdomain myapp\n" +
			"  tunnelx http 5173 --host-header rewrite   # Vite and similar dev servers",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			local, err := parseLocalTarget(args[0])
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if serverAddr == "" {
				serverAddr = cfg.Server()
			}
			if token == "" {
				token = cfg.Token
			}
			if caCert == "" {
				caCert = cfg.CACert
			}

			mode, value, err := parseHostHeader(hostHeader)
			if err != nil {
				return err
			}

			level := slog.LevelWarn
			if verbose {
				level = slog.LevelDebug
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

			ag, err := agent.New(agent.Config{
				ServerAddr:      serverAddr,
				Token:           token,
				LocalAddr:       local,
				Subdomain:       subdomain,
				HostHeader:      mode,
				HostHeaderValue: value,
				Insecure:        insecure || cfg.Insecure,
				TLSDisabled:     noTLS,
				CACert:          caCert,
				Observer:        newConsole(cmd.OutOrStdout(), local),
				Logger:          logger,
			})
			if err != nil {
				return err
			}
			return ag.Run(cmd.Context())
		},
	}

	f := cmd.Flags()
	f.StringVar(&subdomain, "subdomain", "", "request a specific subdomain instead of a generated one")
	f.StringVar(&hostHeader, "host-header", string(agent.HostPreserve),
		"Host header sent to the local service: preserve, rewrite, or a literal value")
	f.StringVar(&serverAddr, "server", "", "tunnel server control endpoint (host:port)")
	f.StringVar(&token, "token", "", "authtoken to use instead of the saved one")
	f.BoolVar(&insecure, "insecure", false, "skip TLS certificate verification (development only)")
	f.BoolVar(&noTLS, "no-tls", false, "connect to the server without TLS (development only)")
	f.BoolVarP(&verbose, "verbose", "v", false, "log protocol detail to stderr")
	f.StringVar(&caCert, "ca-cert", "", "path to a custom CA certificate PEM file")
	return cmd
}

// parseLocalTarget normalises the many ways a developer might name their local
// service into a host:port address.
func parseLocalTarget(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("no local target given")
	}

	// A bare port is the common case: `tunnelx http 3000`.
	if port, err := strconv.Atoi(arg); err == nil {
		if port < 1 || port > 65535 {
			return "", fmt.Errorf("port %d is out of range", port)
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	}

	// Accept a URL so pasting from a browser or dev-server banner works.
	if strings.Contains(arg, "://") {
		scheme, rest, _ := strings.Cut(arg, "://")
		if scheme != "http" {
			return "", fmt.Errorf("only http targets are supported, got %q", scheme)
		}
		arg = rest
		if i := strings.IndexAny(arg, "/?#"); i >= 0 {
			arg = arg[:i]
		}
	}

	host, port, err := net.SplitHostPort(arg)
	if err != nil {
		return "", fmt.Errorf("could not read %q as a port or host:port address", arg)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", fmt.Errorf("%q is not a valid port", port)
	}
	return net.JoinHostPort(host, port), nil
}

// parseHostHeader reads the --host-header flag. Anything that is not one of the
// two policy names is taken as a literal Host value.
func parseHostHeader(v string) (agent.HostHeaderMode, string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", string(agent.HostPreserve):
		return agent.HostPreserve, "", nil
	case string(agent.HostRewrite):
		return agent.HostRewrite, "", nil
	default:
		if strings.ContainsAny(v, " \t\r\n") {
			return "", "", fmt.Errorf("invalid --host-header value %q", v)
		}
		return agent.HostPreserve, v, nil
	}
}
