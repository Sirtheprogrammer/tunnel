package main

import (
	"testing"

	"tunnel/internal/agent"
)

func TestParseLocalTarget(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		valid bool
	}{
		{"bare port", "3000", "127.0.0.1:3000", true},
		{"low port", "80", "127.0.0.1:80", true},
		{"max port", "65535", "127.0.0.1:65535", true},
		{"host and port", "localhost:8080", "localhost:8080", true},
		{"ip and port", "127.0.0.1:5000", "127.0.0.1:5000", true},
		{"other host", "192.168.1.10:3000", "192.168.1.10:3000", true},
		{"url", "http://localhost:3000", "localhost:3000", true},
		{"url with path", "http://127.0.0.1:5173/index.html", "127.0.0.1:5173", true},
		{"url with query", "http://localhost:8000/?a=b", "localhost:8000", true},
		{"empty host in pair", ":9000", "127.0.0.1:9000", true},
		{"surrounding space", "  3000  ", "127.0.0.1:3000", true},

		{"empty", "", "", false},
		{"port zero", "0", "", false},
		{"port too high", "70000", "", false},
		{"https url", "https://localhost:3000", "", false},
		{"no port", "localhost", "", false},
		{"non numeric port", "localhost:web", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLocalTarget(tt.in)
			if tt.valid && err != nil {
				t.Fatalf("parseLocalTarget(%q) = error %v, want %q", tt.in, err, tt.want)
			}
			if !tt.valid && err == nil {
				t.Fatalf("parseLocalTarget(%q) = %q, want an error", tt.in, got)
			}
			if got != tt.want {
				t.Errorf("parseLocalTarget(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseHostHeader(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantMode  agent.HostHeaderMode
		wantValue string
		valid     bool
	}{
		{"default", "", agent.HostPreserve, "", true},
		{"preserve", "preserve", agent.HostPreserve, "", true},
		{"rewrite", "rewrite", agent.HostRewrite, "", true},
		{"uppercase", "REWRITE", agent.HostRewrite, "", true},
		{"literal value", "api.internal", agent.HostPreserve, "api.internal", true},
		{"literal with port", "localhost:3000", agent.HostPreserve, "localhost:3000", true},

		{"value with space", "not a host", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, value, err := parseHostHeader(tt.in)
			if tt.valid && err != nil {
				t.Fatalf("parseHostHeader(%q) = error %v", tt.in, err)
			}
			if !tt.valid && err == nil {
				t.Fatalf("parseHostHeader(%q) = %q/%q, want an error", tt.in, mode, value)
			}
			if mode != tt.wantMode || value != tt.wantValue {
				t.Errorf("parseHostHeader(%q) = %q/%q, want %q/%q",
					tt.in, mode, value, tt.wantMode, tt.wantValue)
			}
		})
	}
}

func TestMaskToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "(not set; run: tunnelx login <token>)"},
		{"short", "*****"},
		{"tx_abcdefghijklmnopqrstuvwxyz", "tx_abcde********wxyz"},
	}
	for _, tt := range tests {
		if got := maskToken(tt.in); got != tt.want {
			t.Errorf("maskToken(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMaskTokenNeverLeaksWholeToken(t *testing.T) {
	// The point of masking is that a screenshot or shared terminal cannot be
	// used to recover the credential.
	const token = "tx_2Nf8xKq1LmZpRvW4tYbCdE6gHjK9sAuP"
	if got := maskToken(token); got == token {
		t.Fatal("maskToken returned the token unchanged")
	}
}
