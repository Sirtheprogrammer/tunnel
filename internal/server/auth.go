package server

import (
	"context"
	"errors"

	"tunnel/internal/names"
)

// ErrUnauthorized is returned by an Authenticator for an unusable token. The
// message is shown verbatim to the user by the CLI, so it is phrased for them.
var ErrUnauthorized = errors.New("invalid or expired authtoken; run: tunnelx login <token>")

// OpenAuth accepts every connection and assigns them all one account. It exists
// for local development and tests; the SQLite-backed authenticator in
// internal/store is what production uses.
//
// It deliberately refuses reserved labels but allows any other custom
// subdomain, matching the real authenticator minus per-account ownership.
type OpenAuth struct {
	// AccountID is reported for every connection. Defaults to "local".
	AccountID string
}

func (a OpenAuth) Authenticate(context.Context, string) (string, error) {
	if a.AccountID != "" {
		return a.AccountID, nil
	}
	return "local", nil
}

func (a OpenAuth) AllowSubdomain(_ context.Context, _, label string) error {
	if names.IsReserved(label) {
		return errors.New("subdomain is reserved")
	}
	return nil
}
