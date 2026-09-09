package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"tunnel/internal/names"
	"tunnel/internal/store"
)

// dbFlag is shared by every admin command.
func addDBFlag(cmd *cobra.Command, target *string) {
	cmd.PersistentFlags().StringVar(target, "db", defaultDBPath, "path to the control-plane database")
}

const defaultDBPath = "tunnelx.db"

// withStore opens the database, runs fn, and closes it.
func withStore(ctx context.Context, path string, fn func(context.Context, *store.Store) error) error {
	s, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(ctx, s)
}

func usersCmd() *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "users",
		Short: "Manage accounts and authtokens",
	}
	addDBFlag(cmd, &dbPath)

	create := &cobra.Command{
		Use:   "create <email>",
		Short: "Create an account and print a new authtoken",
		Long: "Create an account and print a new authtoken.\n\n" +
			"The token is shown once and cannot be recovered afterwards, since only\n" +
			"its hash is stored. Give it to the user to run: tunnelx login <token>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(cmd.Context(), dbPath, func(ctx context.Context, s *store.Store) error {
				acct, err := s.CreateAccount(ctx, args[0])
				if err != nil {
					return err
				}
				plaintext, _, err := s.NewAccountToken(ctx, acct.ID, "initial")
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Account created\n  email:   %s\n  id:      %s\n\n", acct.Email, acct.ID)
				fmt.Fprintf(out, "Authtoken (shown once, store it now):\n\n  %s\n\n", plaintext)
				fmt.Fprintf(out, "The user runs:\n  tunnelx login %s\n", plaintext)
				return nil
			})
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withStore(cmd.Context(), dbPath, func(ctx context.Context, s *store.Store) error {
				accounts, err := s.ListAccounts(ctx)
				if err != nil {
					return err
				}
				if len(accounts) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No accounts yet. Create one with: tunnelxd users create <email>")
					return nil
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
				fmt.Fprintln(w, "ID\tEMAIL\tCREATED\tSTATUS")
				for _, a := range accounts {
					status := "active"
					if a.Disabled {
						status = "disabled"
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
						a.ID, a.Email, a.CreatedAt.Format(time.DateOnly), status)
				}
				return w.Flush()
			})
		},
	}

	disable := &cobra.Command{
		Use:   "disable <email>",
		Short: "Disable an account so its tokens stop working",
		Args:  cobra.ExactArgs(1),
		RunE:  setDisabled(&dbPath, true),
	}
	enable := &cobra.Command{
		Use:   "enable <email>",
		Short: "Re-enable a disabled account",
		Args:  cobra.ExactArgs(1),
		RunE:  setDisabled(&dbPath, false),
	}

	cmd.AddCommand(create, list, disable, enable)
	return cmd
}

func setDisabled(dbPath *string, disabled bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return withStore(cmd.Context(), *dbPath, func(ctx context.Context, s *store.Store) error {
			acct, err := s.AccountByEmail(ctx, args[0])
			if err != nil {
				return accountLookupError(args[0], err)
			}
			if err := s.SetAccountDisabled(ctx, acct.ID, disabled); err != nil {
				return err
			}
			verb := "enabled"
			if disabled {
				verb = "disabled"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Account %s %s\n", acct.Email, verb)
			return nil
		})
	}
}

func tokensCmd() *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Manage authtokens",
	}
	addDBFlag(cmd, &dbPath)

	issue := &cobra.Command{
		Use:   "issue <email>",
		Short: "Issue an additional authtoken for an existing account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, _ := cmd.Flags().GetString("name")
			return withStore(cmd.Context(), dbPath, func(ctx context.Context, s *store.Store) error {
				acct, err := s.AccountByEmail(ctx, args[0])
				if err != nil {
					return accountLookupError(args[0], err)
				}
				plaintext, _, err := s.NewAccountToken(ctx, acct.ID, name)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"Authtoken for %s (shown once):\n\n  %s\n", acct.Email, plaintext)
				return nil
			})
		},
	}
	issue.Flags().String("name", "", "label for this token, to tell devices apart")

	list := &cobra.Command{
		Use:   "list <email>",
		Short: "List an account's tokens",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(cmd.Context(), dbPath, func(ctx context.Context, s *store.Store) error {
				acct, err := s.AccountByEmail(ctx, args[0])
				if err != nil {
					return accountLookupError(args[0], err)
				}
				tokens, err := s.ListTokens(ctx, acct.ID)
				if err != nil {
					return err
				}
				if len(tokens) == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "%s has no tokens\n", acct.Email)
					return nil
				}
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 3, ' ', 0)
				fmt.Fprintln(w, "ID\tPREFIX\tNAME\tCREATED\tLAST USED\tSTATUS")
				for _, t := range tokens {
					lastUsed := "never"
					if t.LastUsedAt != nil {
						lastUsed = t.LastUsedAt.Format(time.DateTime)
					}
					status := "active"
					if t.Revoked {
						status = "revoked"
					}
					fmt.Fprintf(w, "%s\t%s...\t%s\t%s\t%s\t%s\n",
						t.ID, t.Prefix, orDash(t.Name), t.CreatedAt.Format(time.DateOnly), lastUsed, status)
				}
				return w.Flush()
			})
		},
	}

	revoke := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke a token by its ID (from tokens list)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(cmd.Context(), dbPath, func(ctx context.Context, s *store.Store) error {
				if err := s.RevokeToken(ctx, args[0]); err != nil {
					if errors.Is(err, store.ErrNotFound) {
						return fmt.Errorf("no token with ID %q; list them with: tunnelxd tokens list <email>", args[0])
					}
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Token %s revoked\n", args[0])
				return nil
			})
		},
	}

	cmd.AddCommand(issue, list, revoke)
	return cmd
}

func reserveCmd() *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "reserve <subdomain> <email>",
		Short: "Reserve a subdomain for an account",
		Long: "Reserve a subdomain so only the named account can open a tunnel on it.\n" +
			"Unreserved subdomains stay first-come, first-served.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			label, err := names.Validate(args[0])
			if err != nil {
				return err
			}
			return withStore(cmd.Context(), dbPath, func(ctx context.Context, s *store.Store) error {
				acct, err := s.AccountByEmail(ctx, args[1])
				if err != nil {
					return accountLookupError(args[1], err)
				}
				if _, err := s.ReserveSubdomain(ctx, label, acct.ID); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Reserved %s for %s\n", label, acct.Email)
				return nil
			})
		},
	}
	addDBFlag(cmd, &dbPath)

	release := &cobra.Command{
		Use:   "release <subdomain>",
		Short: "Release a reserved subdomain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withStore(cmd.Context(), dbPath, func(ctx context.Context, s *store.Store) error {
				if err := s.ReleaseSubdomain(ctx, args[0]); err != nil {
					if errors.Is(err, store.ErrNotFound) {
						return fmt.Errorf("%q is not reserved", args[0])
					}
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Released %s\n", args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(release)
	return cmd
}

// accountLookupError turns a missing account into advice rather than a bare
// "not found", since the operator is usually one typo away from success.
func accountLookupError(email string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no account for %q; create one with: tunnelxd users create %s", email, email)
	}
	return err
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ensureDBDir creates the parent directory for a database path, so pointing
// --db at /var/lib/tunnelx/tunnelx.db works on a fresh host.
func ensureDBDir(path string) error {
	dir := dirOf(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return ""
}
