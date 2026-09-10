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

	"golang.org/x/crypto/bcrypt"
)

// TunnelSession represents an audit record of a past or active tunnel session.
type TunnelSession struct {
	ID        string
	AccountID string
	Subdomain string
	LocalAddr string
	ClientIP  string
	StartedAt time.Time
	EndedAt   *time.Time
}

// CreateAccountWithPassword creates a new account with a bcrypt-hashed password.
func (s *Store) CreateAccountWithPassword(ctx context.Context, email, password string) (*Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("email is required")
	}
	if strings.Count(email, "@") != 1 || strings.LastIndex(email, ".") < strings.LastIndex(email, "@") {
		return nil, fmt.Errorf("invalid email address: %q", email)
	}
	if len(password) < 6 {
		return nil, errors.New("password must be at least 6 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	a := &Account{
		ID:           newRowID(),
		Email:        email,
		PasswordHash: string(hash),
		CreatedAt:    time.Now(),
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO accounts (id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled)
		 VALUES (?, ?, ?, '', '', '', ?, 0)`,
		a.ID, a.Email, a.PasswordHash, a.CreatedAt.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%s: %w", email, ErrEmailTaken)
		}
		return nil, fmt.Errorf("create account: %w", err)
	}
	return a, nil
}

// AuthenticatePassword verifies email and password, returning the Account if valid.
func (s *Store) AuthenticatePassword(ctx context.Context, email, password string) (*Account, error) {
	acc, err := s.AccountByEmail(ctx, email)
	if err != nil {
		return nil, errors.New("invalid email or password")
	}
	if acc.Disabled {
		return nil, errors.New("account is disabled")
	}
	if acc.PasswordHash == "" {
		return nil, errors.New("account does not have a password configured (try signing in with GitHub)")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(password)); err != nil {
		return nil, errors.New("invalid email or password")
	}
	return acc, nil
}

// FindOrCreateGitHubAccount finds an account linked to the GitHub ID, or links to an
// existing account with the same email, or creates a brand new account.
func (s *Store) FindOrCreateGitHubAccount(ctx context.Context, githubID, username, email, avatarURL string) (*Account, error) {
	if githubID == "" {
		return nil, errors.New("github_id is required")
	}

	// 1. Try to find by GitHub ID first
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled
		 FROM accounts WHERE github_id = ?`, githubID)
	acc, err := scanAccount(row)
	if err == nil {
		// Update username/avatar if changed
		if acc.GitHubUsername != username || acc.AvatarURL != avatarURL {
			_, _ = s.db.ExecContext(ctx,
				`UPDATE accounts SET github_username = ?, avatar_url = ? WHERE id = ?`,
				username, avatarURL, acc.ID)
			acc.GitHubUsername = username
			acc.AvatarURL = avatarURL
		}
		return acc, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// 2. If email provided, check if account already exists with that email
	if email != "" {
		email = strings.ToLower(strings.TrimSpace(email))
		acc, err := s.AccountByEmail(ctx, email)
		if err == nil {
			// Link existing account to GitHub
			_, err := s.db.ExecContext(ctx,
				`UPDATE accounts SET github_id = ?, github_username = ?, avatar_url = ? WHERE id = ?`,
				githubID, username, avatarURL, acc.ID)
			if err != nil {
				return nil, fmt.Errorf("link github account: %w", err)
			}
			acc.GitHubID = githubID
			acc.GitHubUsername = username
			acc.AvatarURL = avatarURL
			return acc, nil
		}
	} else {
		// Default email if user has no public email on GitHub
		email = fmt.Sprintf("%s@users.noreply.github.com", strings.ToLower(username))
	}

	// 3. Create new account
	a := &Account{
		ID:             newRowID(),
		Email:          email,
		GitHubID:       githubID,
		GitHubUsername: username,
		AvatarURL:      avatarURL,
		CreatedAt:      time.Now(),
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO accounts (id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled)
		 VALUES (?, ?, '', ?, ?, ?, ?, 0)`,
		a.ID, a.Email, a.GitHubID, a.GitHubUsername, a.AvatarURL, a.CreatedAt.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			// Email collision with generated email
			a.Email = fmt.Sprintf("%s-%s@users.noreply.github.com", strings.ToLower(username), a.ID[:6])
			_, err = s.db.ExecContext(ctx,
				`INSERT INTO accounts (id, email, password_hash, github_id, github_username, avatar_url, created_at, disabled)
				 VALUES (?, ?, '', ?, ?, ?, ?, 0)`,
				a.ID, a.Email, a.GitHubID, a.GitHubUsername, a.AvatarURL, a.CreatedAt.Unix())
			if err != nil {
				return nil, fmt.Errorf("create github account: %w", err)
			}
		} else {
			return nil, fmt.Errorf("create github account: %w", err)
		}
	}
	return a, nil
}

// CreateWebSession creates an authenticated session token for the web portal.
func (s *Store) CreateWebSession(ctx context.Context, accountID string, ttl time.Duration) (string, error) {
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	sessionID := hex.EncodeToString(tokenBytes[:])
	now := time.Now()
	expiresAt := now.Add(ttl)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO web_sessions (id, account_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		sessionID, accountID, now.Unix(), expiresAt.Unix())
	if err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}
	return sessionID, nil
}

// ValidateWebSession looks up a session token and returns the active Account if valid.
func (s *Store) ValidateWebSession(ctx context.Context, sessionID string) (*Account, error) {
	if sessionID == "" {
		return nil, ErrNotFound
	}
	now := time.Now().Unix()
	row := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.email, a.password_hash, a.github_id, a.github_username, a.avatar_url, a.created_at, a.disabled
		 FROM accounts a
		 INNER JOIN web_sessions s ON s.account_id = a.id
		 WHERE s.id = ? AND s.expires_at > ?`,
		sessionID, now)

	acc, err := scanAccount(row)
	if err != nil {
		return nil, err
	}
	if acc.Disabled {
		return nil, errors.New("account disabled")
	}
	return acc, nil
}

// DeleteWebSession removes a session on logout.
func (s *Store) DeleteWebSession(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE id = ?`, sessionID)
	return err
}

// ListSessionsForAccount returns recent tunnel sessions for an account.
func (s *Store) ListSessionsForAccount(ctx context.Context, accountID string, limit int) ([]*TunnelSession, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, account_id, subdomain, local_addr, client_ip, started_at, ended_at
		 FROM tunnel_sessions
		 WHERE account_id = ?
		 ORDER BY started_at DESC
		 LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list tunnel sessions: %w", err)
	}
	defer rows.Close()

	var out []*TunnelSession
	for rows.Next() {
		var ts TunnelSession
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&ts.ID, &ts.AccountID, &ts.Subdomain, &ts.LocalAddr, &ts.ClientIP, &started, &ended); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		ts.StartedAt = time.Unix(started, 0)
		if ended.Valid {
			t := time.Unix(ended.Int64, 0)
			ts.EndedAt = &t
		}
		out = append(out, &ts)
	}
	return out, rows.Err()
}
