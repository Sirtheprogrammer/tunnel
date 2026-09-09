package server

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func testTunnel(label, account string) *Tunnel {
	return &Tunnel{ID: newID(), Label: label, AccountID: account}
}

func TestRegisterAndLookup(t *testing.T) {
	r := NewRegistry(time.Minute)
	tun := testTunnel("myapp", "acct-1")
	if err := r.Register(tun); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Lookup("myapp")
	if !ok || got != tun {
		t.Fatalf("Lookup(myapp) = %v, %v; want the registered tunnel", got, ok)
	}
	if _, ok := r.Lookup("other"); ok {
		t.Error("Lookup(other) matched something")
	}
}

func TestRegisterRejectsLiveDuplicate(t *testing.T) {
	r := NewRegistry(time.Minute)
	if err := r.Register(testTunnel("myapp", "acct-1")); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	// Even the same account cannot double-register a live label.
	err := r.Register(testTunnel("myapp", "acct-1"))
	if !errors.Is(err, ErrLabelTaken) {
		t.Fatalf("second Register error = %v, want ErrLabelTaken", err)
	}
}

func TestLeaseLetsOwnerReclaimButBlocksOthers(t *testing.T) {
	r := NewRegistry(time.Minute)
	first := testTunnel("myapp", "acct-1")
	if err := r.Register(first); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r.Unregister(first)

	if err := r.Register(testTunnel("myapp", "acct-2")); !errors.Is(err, ErrLabelLeased) {
		t.Fatalf("stranger reclaim error = %v, want ErrLabelLeased", err)
	}
	if err := r.Register(testTunnel("myapp", "acct-1")); err != nil {
		t.Fatalf("owner should reclaim its own subdomain within the lease, got: %v", err)
	}
}

func TestLeaseExpires(t *testing.T) {
	r := NewRegistry(time.Minute)
	now := time.Now()
	r.now = func() time.Time { return now }

	tun := testTunnel("myapp", "acct-1")
	if err := r.Register(tun); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r.Unregister(tun)

	now = now.Add(61 * time.Second) // past the lease
	if err := r.Register(testTunnel("myapp", "acct-2")); err != nil {
		t.Fatalf("after the lease expired another account should claim it, got: %v", err)
	}
}

func TestUnregisterDoesNotEvictItsReplacement(t *testing.T) {
	// A slow teardown of an old session must not remove the tunnel that has
	// already taken over the label, or a reconnecting agent would knock itself
	// offline moments after coming back.
	r := NewRegistry(time.Minute)
	old := testTunnel("myapp", "acct-1")
	if err := r.Register(old); err != nil {
		t.Fatalf("Register old: %v", err)
	}
	r.Unregister(old)

	replacement := testTunnel("myapp", "acct-1")
	if err := r.Register(replacement); err != nil {
		t.Fatalf("Register replacement: %v", err)
	}

	r.Unregister(old) // late teardown of the dead session

	got, ok := r.Lookup("myapp")
	if !ok {
		t.Fatal("the replacement tunnel was evicted by the old session's teardown")
	}
	if got != replacement {
		t.Error("Lookup returned the old tunnel, not the replacement")
	}
}

func TestRegisterGeneratedIsUnique(t *testing.T) {
	r := NewRegistry(0)
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		tun := &Tunnel{ID: newID(), AccountID: "acct-1"}
		if err := r.RegisterGenerated(tun); err != nil {
			t.Fatalf("RegisterGenerated: %v", err)
		}
		if tun.Label == "" {
			t.Fatal("RegisterGenerated left Label empty")
		}
		if seen[tun.Label] {
			t.Fatalf("label %q was handed out twice", tun.Label)
		}
		seen[tun.Label] = true
	}
	if r.Len() != 200 {
		t.Errorf("registry holds %d tunnels, want 200", r.Len())
	}
}

func TestCountFor(t *testing.T) {
	r := NewRegistry(0)
	for i := 0; i < 3; i++ {
		if err := r.Register(testTunnel(fmt.Sprintf("app-a%d", i), "acct-1")); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	if err := r.Register(testTunnel("app-b0", "acct-2")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := r.CountFor("acct-1"); got != 3 {
		t.Errorf("CountFor(acct-1) = %d, want 3", got)
	}
	if got := r.CountFor("acct-2"); got != 1 {
		t.Errorf("CountFor(acct-2) = %d, want 1", got)
	}
	if got := r.CountFor("nobody"); got != 0 {
		t.Errorf("CountFor(nobody) = %d, want 0", got)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	// The data plane reads this map on every request while agents connect and
	// disconnect, so run it under -race to catch unguarded access.
	r := NewRegistry(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				label := fmt.Sprintf("app-%d-%d", i, j)
				tun := testTunnel(label, fmt.Sprintf("acct-%d", i))
				if err := r.Register(tun); err != nil {
					t.Errorf("Register(%s): %v", label, err)
					return
				}
				r.Lookup(label)
				r.CountFor(tun.AccountID)
				r.Unregister(tun)
			}
		}(i)
	}
	wg.Wait()
	if r.Len() != 0 {
		t.Errorf("registry holds %d tunnels after all were unregistered, want 0", r.Len())
	}
}
