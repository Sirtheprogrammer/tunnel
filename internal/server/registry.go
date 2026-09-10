package server

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"tunnel/internal/names"
	"tunnel/internal/proto"
)

// Errors returned when a label cannot be claimed.
var (
	// ErrLabelTaken means another live tunnel is serving the label.
	ErrLabelTaken = errors.New("subdomain is in use")
	// ErrLabelLeased means the label was recently used by a different account
	// and is held briefly so its owner can reclaim it after a reconnect.
	ErrLabelLeased = errors.New("subdomain is reserved by a recent session")
)

// Tunnel is one live forwarding route: a public label bound to an agent.
type Tunnel struct {
	ID        string
	Label     string
	AccountID string
	LocalAddr string
	Proto     proto.Proto
	URL       string
	CreatedAt time.Time
	SessionRowID string

	// sess is the agent session that serves this tunnel. Data streams are
	// opened on it.
	sess *Session
}

// lease holds a label for its previous owner for a short window after a
// disconnect, so a reconnecting agent keeps its URL instead of being handed a
// new one mid-demo.
type lease struct {
	accountID string
	expires   time.Time
}

// Registry maps public labels to live tunnels.
//
// All exported methods are safe for concurrent use; the data plane reads from
// it on every request, while agent connect and disconnect write to it.
type Registry struct {
	mu       sync.RWMutex
	tunnels  map[string]*Tunnel
	leases   map[string]lease
	leaseTTL time.Duration

	// now is swappable so tests can drive lease expiry without sleeping.
	now func() time.Time
}

// NewRegistry returns an empty registry. A leaseTTL of zero disables leases.
func NewRegistry(leaseTTL time.Duration) *Registry {
	return &Registry{
		tunnels:  make(map[string]*Tunnel),
		leases:   make(map[string]lease),
		leaseTTL: leaseTTL,
		now:      time.Now,
	}
}

// Register claims t.Label for t. It fails if the label is serving another
// tunnel, or is leased to a different account.
func (r *Registry) Register(t *Tunnel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registerLocked(t)
}

func (r *Registry) registerLocked(t *Tunnel) error {
	if _, ok := r.tunnels[t.Label]; ok {
		return fmt.Errorf("%q: %w", t.Label, ErrLabelTaken)
	}
	if l, ok := r.leases[t.Label]; ok {
		if r.now().After(l.expires) {
			delete(r.leases, t.Label)
		} else if l.accountID != t.AccountID {
			return fmt.Errorf("%q: %w", t.Label, ErrLabelLeased)
		}
	}
	delete(r.leases, t.Label)
	r.tunnels[t.Label] = t
	return nil
}

// RegisterGenerated assigns t a random free label and registers it. It sets
// t.Label on success.
func (r *Registry) RegisterGenerated(t *Tunnel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Collisions are rare, so a handful of attempts is plenty; failing loudly
	// beats looping forever if the namespace is somehow exhausted.
	for i := 0; i < 12; i++ {
		label, err := names.Generate()
		if err != nil {
			return err
		}
		t.Label = label
		if err := r.registerLocked(t); err == nil {
			return nil
		}
	}
	t.Label = ""
	return errors.New("could not find a free subdomain after 12 attempts")
}

// Unregister removes t and starts a lease on its label. It is a no-op if the
// label has since been claimed by a different tunnel, so a slow teardown cannot
// evict the session that replaced it.
func (r *Registry) Unregister(t *Tunnel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.tunnels[t.Label]
	if !ok || cur != t {
		return
	}
	delete(r.tunnels, t.Label)
	if r.leaseTTL > 0 && t.AccountID != "" {
		r.leases[t.Label] = lease{accountID: t.AccountID, expires: r.now().Add(r.leaseTTL)}
	}
}

// Lookup returns the tunnel serving label.
func (r *Registry) Lookup(label string) (*Tunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tunnels[label]
	return t, ok
}

// CountFor returns how many live tunnels an account holds, used to enforce the
// per-account limit.
func (r *Registry) CountFor(accountID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, t := range r.tunnels {
		if t.AccountID == accountID {
			n++
		}
	}
	return n
}

// Count returns the number of live tunnels.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tunnels)
}

// Len returns the number of live tunnels.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tunnels)
}

// sweepLeases drops expired leases. Leases are also checked lazily on
// register, so this only bounds memory for labels that are never reused.
func (r *Registry) sweepLeases() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for label, l := range r.leases {
		if now.After(l.expires) {
			delete(r.leases, label)
		}
	}
}

// SweepLoop periodically drops expired leases until done is closed.
func (r *Registry) SweepLoop(done <-chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			r.sweepLeases()
		}
	}
}
