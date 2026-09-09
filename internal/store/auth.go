package store

import (
	"context"
	"errors"
	"fmt"

	"tunnel/internal/names"
)

// Authenticator adapts a Store to the server's Authenticator interface.
type Authenticator struct {
	store *Store

	// AllowAnonymous lets agents connect without a token, receiving the
	// account named by AnonymousAccountID. It exists for a self-hosted server
	// on a private network; leaving it off is the right default on a public
	// domain, where an open tunnel service becomes a phishing relay.
	AllowAnonymous     bool
	AnonymousAccountID string
}

// NewAuthenticator returns an Authenticator backed by s.
func NewAuthenticator(s *Store) *Authenticator {
	return &Authenticator{store: s}
}

// errUnauthorized is phrased for the developer who sees it in their terminal.
var errUnauthorized = errors.New("invalid or expired authtoken; run: tunnelx login <token>")

// Authenticate resolves a token to an account ID.
//
// Every failure returns the same message. Distinguishing "no such token" from
// "revoked" from "account disabled" would tell someone probing tokens which
// guesses were closer.
func (a *Authenticator) Authenticate(ctx context.Context, token string) (string, error) {
	if token == "" {
		if a.AllowAnonymous {
			id := a.AnonymousAccountID
			if id == "" {
				id = "anonymous"
			}
			return id, nil
		}
		return "", errors.New("no authtoken configured; run: tunnelx login <token>")
	}
	account, err := a.store.AuthenticateToken(ctx, token)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", errUnauthorized
		}
		return "", fmt.Errorf("authenticate: %w", err)
	}
	return account.ID, nil
}

// AllowSubdomain reports whether an account may claim a label.
//
// An unreserved label is first-come first-served, matching how a generated
// subdomain behaves. A reserved label belongs to exactly one account.
func (a *Authenticator) AllowSubdomain(ctx context.Context, accountID, label string) error {
	if names.IsReserved(label) {
		return fmt.Errorf("subdomain %q is reserved", label)
	}
	owner, err := a.store.SubdomainOwner(ctx, label)
	if errors.Is(err, ErrNotFound) {
		return nil // not reserved by anyone
	}
	if err != nil {
		return fmt.Errorf("check subdomain reservation: %w", err)
	}
	if owner != accountID {
		return fmt.Errorf("subdomain %q is reserved by another account", label)
	}
	return nil
}
