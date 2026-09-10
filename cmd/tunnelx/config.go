package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"tunnel/internal/config"
)

func loginCmd() *cobra.Command {
	var serverAddr string

	cmd := &cobra.Command{
		Use:   "login <authtoken>",
		Short: "Save an authtoken for this machine",
		Long: "Save an authtoken to ~/.tunnelx/config.yaml so future tunnels\n" +
			"authenticate automatically. Ask your tunnelx administrator for a token.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token := strings.TrimSpace(args[0])
			if token == "" {
				return fmt.Errorf("authtoken is empty")
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.Token = token
			if serverAddr != "" {
				cfg.ServerAddr = serverAddr
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			path, _ := config.Path()
			fmt.Fprintf(cmd.OutOrStdout(), "Authtoken saved to %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "also save a custom server control endpoint")
	return cmd
}

func logoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove the saved authtoken",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.Token = ""
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Logged out. Token removed from configuration.")
			return nil
		},
	}
	return cmd
}

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the tunnelx configuration",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file location",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Show the effective configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Config file:  %s\n", path)
			fmt.Fprintf(out, "Server:       %s\n", cfg.Server())
			fmt.Fprintf(out, "Authtoken:    %s\n", maskToken(cfg.Token))
			if cfg.Insecure {
				fmt.Fprintf(out, "Insecure:     yes (certificate verification is off)\n")
			}
			return nil
		},
	})

	return cmd
}

// maskToken shows just enough of a token to tell which one is configured,
// without printing a working credential to a terminal that may be shared.
func maskToken(token string) string {
	if token == "" {
		return "(not set; run: tunnelx login <token>)"
	}
	if len(token) <= 12 {
		return strings.Repeat("*", len(token))
	}
	return token[:8] + strings.Repeat("*", 8) + token[len(token)-4:]
}
