package server

import (
	"context"
	"strings"
	"testing"
)

// These only cover the fail-fast validation in BuildACMETLSConfig: anything
// past that point calls out to Let's Encrypt and Cloudflare, which has no
// place in a unit test. Catching a missing field here means an operator sees
// "--acme-email is required" instead of a struct-cast panic somewhere inside
// certmagic.
func TestBuildACMETLSConfigValidatesFields(t *testing.T) {
	valid := ACMEConfig{
		Domain:             "tl.codesky.tech",
		Email:              "you@codesky.tech",
		CloudflareAPIToken: "fake-token",
		CacheDir:           t.TempDir(),
	}

	tests := []struct {
		name    string
		mutate  func(c ACMEConfig) ACMEConfig
		wantErr string
	}{
		{
			name:    "missing domain",
			mutate:  func(c ACMEConfig) ACMEConfig { c.Domain = ""; return c },
			wantErr: "Domain is required",
		},
		{
			name:    "missing email",
			mutate:  func(c ACMEConfig) ACMEConfig { c.Email = ""; return c },
			wantErr: "Email is required",
		},
		{
			name:    "missing cloudflare token",
			mutate:  func(c ACMEConfig) ACMEConfig { c.CloudflareAPIToken = ""; return c },
			wantErr: "CloudflareAPIToken is required",
		},
		{
			name:    "missing cache dir",
			mutate:  func(c ACMEConfig) ACMEConfig { c.CacheDir = ""; return c },
			wantErr: "CacheDir is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildACMETLSConfig(context.Background(), tt.mutate(valid))
			if err == nil {
				t.Fatal("expected a validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
