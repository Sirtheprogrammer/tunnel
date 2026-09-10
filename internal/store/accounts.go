package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Account is a tunnelx user.
type Account struct {
	ID             string
	Email          string
	PasswordHash   string
	GitHubID       string
	GitHubUsername string
	AvatarURL      string
	CreatedAt      time.Time
	Disabled       bool
}

// Token is an issued authtoken. The plaintext is never stored, so it is absent
// from this struct; NewAccountToken returns it once at creation.
type Token struct {
	ID         string
	AccountID  string
	Prefix     string
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Revoked    bool
}

func newRowID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// CreateAccount adds an account. Emails are normalised and must be unique.
func (s *Store) CreateAccount(ctx context.Context, email string) (*Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("email is required")
	}
	if strings.Count(email, "@") != 1 || strings.LastIndex(email, ".") < strings.LastIndex(email, "@") {
		return nil, fmt.Errorf("invalid email address: %q", email)
	}
	a := &Account{ID: newRowID(), Email: email, CreatedAt: time.Now()}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO accounts (id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled)
		 VALUES (?, ?, '', '', '', '', ?, 0)`,
		a.ID, a.Email, a.CreatedAt.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%s: %w", email, ErrEmailTaken)
		}
		return nil, fmt.Errorf("create account: %w", err)
	}
	return a, nil
}

// AccountByEmail looks up an account.
func (s *Store) AccountByEmail(ctx context.Context, email string) (*Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled
		 FROM accounts WHERE email = ?`, email)
	return scanAccount(row)
}

// AccountByID looks up an account.
func (s *Store) AccountByID(ctx context.Context, id string) (*Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled
		 FROM accounts WHERE id = ?`, id)
	return scanAccount(row)
}

func scanAccount(row *sql.Row) (*Account, error) {
	var a Account
	var created int64
	var disabled int
	if err := row.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.GitHubID, &a.GitHubUsername, &a.AvatarURL, &created, &disabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read account: %w", err)
	}
	a.CreatedAt = time.Unix(created, 0)
	a.Disabled = disabled != 0
	return &a, nil
}

// ListAccounts returns every account, oldest first.
func (s *Store) ListAccounts(ctx context.Context) ([]*Account, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled
		 FROM accounts ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var out []*Account
	for rows.Next() {
		var a Account
		var created int64
		var disabled int
		if err := rows.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.GitHubID, &a.GitHubUsername, &a.AvatarURL, &created, &disabled); err != nil {
			return nil, fmt.Errorf("read account: %w", err)
		}
		a.CreatedAt = time.Unix(created, 0)
		a.Disabled = disabled != 0
		out = append(out, &a)
	}
	return out, rows.Err()
}

// SetAccountDisabled enables or disables an account. A disabled account cannot
// authenticate, which is how an operator cuts off abuse without deleting data.
func (s *Store) SetAccountDisabled(ctx context.Context, accountID string, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET disabled = ? WHERE id = ?`, v, accountID)
	if err != nil {
		return fmt.Errorf("update account: %w", err)
	}
	return requireOneRow(res, accountID)
}

// NewAccountToken issues a token for an account. The plaintext is returned only
// here; afterwards only its hash exists.
func (s *Store) NewAccountToken(ctx context.Context, accountID, name string) (plaintext string, tok *Token, err error) {
	plaintext, err = NewToken()
	if err != nil {
		return "", nil, err
	}
	tok = &Token{
		ID:        newRowID(),
		AccountID: accountID,
		Prefix:    TokenDisplayPrefix(plaintext),
		Name:      strings.TrimSpace(name),
		CreatedAt: time.Now(),
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tokens (id, account_id, token_hash, prefix, name, created_at, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		tok.ID, tok.AccountID, HashToken(plaintext), tok.Prefix, tok.Name, tok.CreatedAt.Unix())
	if err != nil {
		return "", nil, fmt.Errorf("create token: %w", err)
	}
	return plaintext, tok, nil
}

// AuthenticateToken resolves a token to its account, rejecting revoked tokens
// and disabled accounts.
func (s *Store) AuthenticateToken(ctx context.Context, token string) (*Account, error) {
	if !ValidTokenShape(token) {
		return nil, ErrNotFound
	}
	hash := HashToken(token)

	var (
		tokenID    string
		storedHash []byte
		revoked    int
		accountID  string
		email      string
		created    int64
		disabled   int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT t.id, t.token_hash, t.revoked, a.id, a.email, a.created_at, a.disabled
		   FROM tokens t JOIN accounts a ON a.id = t.account_id
		  WHERE t.token_hash = ?`, hash).
		Scan(&tokenID, &storedHash, &revoked, &accountID, &email, &created, &disabled)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("look up token: %w", err)
	}

	// The unique index already matched the hash; the constant-time compare
	// guards against a future lookup path that is less exact.
	if !equalHash(hash, storedHash) {
		return nil, ErrNotFound
	}
	if revoked != 0 {
		return nil, fmt.Errorf("token was revoked: %w", ErrNotFound)
	}
	if disabled != 0 {
		return nil, fmt.Errorf("account is disabled: %w", ErrNotFound)
	}

	// Best effort: a failed timestamp update must not deny a valid connection.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now(), tokenID, time.Now().Add(-time.Minute).Unix()); err != nil {
		_ = err
	}

	return &Account{
		ID:        accountID,
		Email:     email,
		CreatedAt: time.Unix(created, 0),
		Disabled:  false,
	}, nil
}

// ListTokens returns an account's tokens, newest first.
func (s *Store) ListTokens(ctx context.Context, accountID string) ([]*Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, account_id, prefix, name, created_at, last_used_at, revoked
		   FROM tokens WHERE account_id = ? ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	var out []*Token
	for rows.Next() {
		var t Token
		var created int64
		var lastUsed *int64
		var revoked int
		if err := rows.Scan(&t.ID, &t.AccountID, &t.Prefix, &t.Name, &created, &lastUsed, &revoked); err != nil {
			return nil, fmt.Errorf("read token: %w", err)
		}
		t.CreatedAt = time.Unix(created, 0)
		t.LastUsedAt = unixPtr(lastUsed)
		t.Revoked = revoked != 0
		out = append(out, &t)
	}
	return out, rows.Err()
}

// RevokeToken marks a token unusable. Revoking is preferred over deleting so
// the audit trail of what was issued survives.
func (s *Store) RevokeToken(ctx context.Context, tokenID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE id = ?`, tokenID)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return requireOneRow(res, tokenID)
}

func requireOneRow(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return nil
}

// isUniqueViolation reports whether err is a UNIQUE constraint failure. The
// pure-Go driver does not export a typed error for this, so the message is the
// only signal available.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}
