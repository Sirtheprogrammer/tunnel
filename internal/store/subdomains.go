package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Reservation is a subdomain permanently held by an account.
type Reservation struct {
	Label     string
	AccountID string
	CreatedAt time.Time
}

// ReserveSubdomain permanently assigns a label to an account, so only that
// account can open a tunnel on it. Reserving a label already held by someone
// else fails rather than silently transferring it.
func (s *Store) ReserveSubdomain(ctx context.Context, label, accountID string) (*Reservation, error) {
	r := &Reservation{Label: label, AccountID: accountID, CreatedAt: time.Now()}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO reserved_subdomains (label, account_id, created_at) VALUES (?, ?, ?)`,
		r.Label, r.AccountID, r.CreatedAt.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			owner, lookupErr := s.SubdomainOwner(ctx, label)
			if lookupErr == nil && owner == accountID {
				return r, nil // already reserved by this account; nothing to do
			}
			return nil, fmt.Errorf("%s: %w", label, ErrLabelReserved)
		}
		return nil, fmt.Errorf("reserve subdomain: %w", err)
	}
	return r, nil
}

// ReleaseSubdomain drops a reservation.
func (s *Store) ReleaseSubdomain(ctx context.Context, label string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM reserved_subdomains WHERE label = ?`, label)
	if err != nil {
		return fmt.Errorf("release subdomain: %w", err)
	}
	return requireOneRow(res, label)
}

// SubdomainOwner returns the account holding label, or ErrNotFound if the label
// is not reserved at all.
func (s *Store) SubdomainOwner(ctx context.Context, label string) (string, error) {
	var accountID string
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id FROM reserved_subdomains WHERE label = ?`, label).Scan(&accountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("look up subdomain owner: %w", err)
	}
	return accountID, nil
}

// ListReservations returns an account's reserved subdomains.
func (s *Store) ListReservations(ctx context.Context, accountID string) ([]*Reservation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT label, account_id, created_at FROM reserved_subdomains
		  WHERE account_id = ? ORDER BY label`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list reservations: %w", err)
	}
	defer rows.Close()

	var out []*Reservation
	for rows.Next() {
		var r Reservation
		var created int64
		if err := rows.Scan(&r.Label, &r.AccountID, &created); err != nil {
			return nil, fmt.Errorf("read reservation: %w", err)
		}
		r.CreatedAt = time.Unix(created, 0)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// StartSession records a tunnel opening and returns the session row ID.
func (s *Store) StartSession(ctx context.Context, accountID, subdomain, localAddr, clientIP string) (string, error) {
	id := newRowID()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tunnel_sessions (id, account_id, subdomain, local_addr, client_ip, started_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, accountID, subdomain, localAddr, clientIP, now())
	if err != nil {
		return "", fmt.Errorf("record session start: %w", err)
	}
	return id, nil
}

// EndSession stamps a tunnel session as finished.
func (s *Store) EndSession(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tunnel_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL`,
		now(), sessionID)
	if err != nil {
		return fmt.Errorf("record session end: %w", err)
	}
	return nil
}
