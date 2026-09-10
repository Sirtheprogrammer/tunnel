// Command tunnelx opens an HTTPS tunnel from a public URL to a local port.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"tunnel/internal/agent"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	agent.SetVersion(version)

	// Cancel on Ctrl-C so the tunnel closes cleanly and the server releases the
	// subdomain immediately rather than waiting for a keepalive to lapse.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd().ExecuteContext(ctx); err != nil {
		// Cobra has already printed usage errors; only report real failures.
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "tunnelx",
		Short: "Expose a local port at a public HTTPS URL",
		Long: "tunnelx forwards traffic from a public HTTPS URL to a service running\n" +
			"on your machine, so you can test webhooks, share work in progress, and\n" +
			"debug on real devices without deploying.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(httpCmd(), loginCmd(), logoutCmd(), configCmd(), versionCmd())
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the tunnelx version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "tunnelx", version)
			return nil
		},
	}
}
