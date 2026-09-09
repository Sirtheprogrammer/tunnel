package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	// A file in t.TempDir rather than :memory:, so each test gets a genuinely
	// private database and the WAL pragmas behave as they do in production.
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTokenShape(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) {
		t.Errorf("token %q lacks the %q prefix", tok, TokenPrefix)
	}
	if !ValidTokenShape(tok) {
		t.Errorf("ValidTokenShape rejected a freshly generated token %q", tok)
	}
	for _, bad := range []string{"", "hunter2", "tx_", "tx_not-base64!!", TokenPrefix + "c2hvcnQ"} {
		if ValidTokenShape(bad) {
			t.Errorf("ValidTokenShape accepted %q", bad)
		}
	}
}

func TestTokensAreDistinct(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("NewToken produced a duplicate: %q", tok)
		}
		seen[tok] = true
	}
}

func TestPlaintextTokenIsNeverStored(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	acct, err := s.CreateAccount(ctx, "dev@codesky.tech")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	plaintext, tok, err := s.NewAccountToken(ctx, acct.ID, "laptop")
	if err != nil {
		t.Fatalf("NewAccountToken: %v", err)
	}

	// Scan every text column for the plaintext. Storing it would turn a
	// database leak into a full account takeover.
	rows, err := s.DB().QueryContext(ctx, `SELECT id, account_id, prefix, name FROM tokens`)
	if err != nil {
		t.Fatalf("query tokens: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, account, prefix, name string
		if err := rows.Scan(&id, &account, &prefix, &name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, v := range []string{id, account, prefix, name} {
			if v == plaintext {
				t.Fatal("the plaintext token was stored in the database")
			}
		}
	}
	if tok.Prefix == plaintext {
		t.Fatal("the display prefix is the whole token")
	}
	if !strings.HasPrefix(plaintext, tok.Prefix) {
		t.Errorf("prefix %q does not match token %q", tok.Prefix, plaintext)
	}
}

func TestAuthenticateToken(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	acct, err := s.CreateAccount(ctx, "dev@codesky.tech")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	plaintext, tok, err := s.NewAccountToken(ctx, acct.ID, "laptop")
	if err != nil {
		t.Fatalf("NewAccountToken: %v", err)
	}

	got, err := s.AuthenticateToken(ctx, plaintext)
	if err != nil {
		t.Fatalf("AuthenticateToken: %v", err)
	}
	if got.ID != acct.ID {
		t.Errorf("resolved account = %q, want %q", got.ID, acct.ID)
	}

	// An unrelated but well-formed token must not authenticate.
	other, _ := NewToken()
	if _, err := s.AuthenticateToken(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token error = %v, want ErrNotFound", err)
	}

	if err := s.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := s.AuthenticateToken(ctx, plaintext); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token error = %v, want ErrNotFound", err)
	}
}

func TestDisabledAccountCannotAuthenticate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	acct, _ := s.CreateAccount(ctx, "abuser@example.com")
	plaintext, _, err := s.NewAccountToken(ctx, acct.ID, "")
	if err != nil {
		t.Fatalf("NewAccountToken: %v", err)
	}
	if _, err := s.AuthenticateToken(ctx, plaintext); err != nil {
		t.Fatalf("token should work before disabling: %v", err)
	}

	if err := s.SetAccountDisabled(ctx, acct.ID, true); err != nil {
		t.Fatalf("SetAccountDisabled: %v", err)
	}
	if _, err := s.AuthenticateToken(ctx, plaintext); !errors.Is(err, ErrNotFound) {
		t.Errorf("disabled account error = %v, want ErrNotFound", err)
	}
}

func TestDuplicateEmailRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.CreateAccount(ctx, "dev@codesky.tech"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// Case and surrounding space must not create a second account.
	if _, err := s.CreateAccount(ctx, "  DEV@Codesky.Tech "); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email error = %v, want ErrEmailTaken", err)
	}
}

func TestReservedSubdomainOwnership(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	alice, _ := s.CreateAccount(ctx, "alice@example.com")
	bob, _ := s.CreateAccount(ctx, "bob@example.com")

	if _, err := s.ReserveSubdomain(ctx, "myapp", alice.ID); err != nil {
		t.Fatalf("ReserveSubdomain: %v", err)
	}
	// Re-reserving for the same account is a harmless no-op.
	if _, err := s.ReserveSubdomain(ctx, "myapp", alice.ID); err != nil {
		t.Errorf("re-reserving for the owner should succeed, got: %v", err)
	}
	if _, err := s.ReserveSubdomain(ctx, "myapp", bob.ID); !errors.Is(err, ErrLabelReserved) {
		t.Errorf("stranger reservation error = %v, want ErrLabelReserved", err)
	}

	auth := NewAuthenticator(s)
	if err := auth.AllowSubdomain(ctx, alice.ID, "myapp"); err != nil {
		t.Errorf("owner should be allowed its reserved subdomain: %v", err)
	}
	if err := auth.AllowSubdomain(ctx, bob.ID, "myapp"); err == nil {
		t.Error("a stranger was allowed a reserved subdomain")
	}
	// An unreserved label stays first-come, first-served.
	if err := auth.AllowSubdomain(ctx, bob.ID, "unclaimed"); err != nil {
		t.Errorf("unreserved label should be allowed: %v", err)
	}
	// Service-reserved labels are refused regardless of ownership.
	if err := auth.AllowSubdomain(ctx, alice.ID, "dashboard"); err == nil {
		t.Error("a service-reserved label was allowed")
	}
}

func TestAuthenticatorRejectsEmptyTokenByDefault(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	auth := NewAuthenticator(s)

	if _, err := auth.Authenticate(ctx, ""); err == nil {
		t.Fatal("an empty token was accepted; the service would be an open relay")
	}

	auth.AllowAnonymous = true
	id, err := auth.Authenticate(ctx, "")
	if err != nil {
		t.Fatalf("anonymous auth: %v", err)
	}
	if id != "anonymous" {
		t.Errorf("anonymous account = %q, want %q", id, "anonymous")
	}
}

func TestAuthenticatorErrorDoesNotLeakWhy(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	auth := NewAuthenticator(s)

	acct, _ := s.CreateAccount(ctx, "dev@codesky.tech")
	plaintext, tok, _ := s.NewAccountToken(ctx, acct.ID, "")
	if err := s.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	unknown, _ := NewToken()

	_, errRevoked := auth.Authenticate(ctx, plaintext)
	_, errUnknown := auth.Authenticate(ctx, unknown)
	if errRevoked == nil || errUnknown == nil {
		t.Fatal("both tokens should have been rejected")
	}
	if errRevoked.Error() != errUnknown.Error() {
		t.Errorf("rejection messages differ and reveal token state:\n revoked: %v\n unknown: %v",
			errRevoked, errUnknown)
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	acct, _ := s.CreateAccount(ctx, "dev@codesky.tech")
	id, err := s.StartSession(ctx, acct.ID, "myapp", "127.0.0.1:3000", "203.0.113.5")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := s.EndSession(ctx, id); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	var ended *int64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT ended_at FROM tunnel_sessions WHERE id = ?`, id).Scan(&ended); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if ended == nil {
		t.Error("ended_at was not stamped")
	}
}
