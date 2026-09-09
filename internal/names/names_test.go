package names

import (
	"regexp"
	"strings"
	"testing"
)

func TestGenerateShape(t *testing.T) {
	want := regexp.MustCompile(`^[a-z]+-[a-z]+-[0-9a-f]{4}$`)
	seen := make(map[string]bool)
	for i := 0; i < 500; i++ {
		got, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !want.MatchString(got) {
			t.Fatalf("Generate returned %q, want match %s", got, want)
		}
		if _, err := Validate(got); err != nil {
			t.Fatalf("Generate returned %q which fails Validate: %v", got, err)
		}
		seen[got] = true
	}
	// Not a strict guarantee, but 500 draws from a ~170M space colliding more
	// than a handful of times means the generator is not actually random.
	if len(seen) < 495 {
		t.Errorf("got %d distinct names out of 500, generator looks biased", len(seen))
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		valid bool
	}{
		{"simple", "myapp", "myapp", true},
		{"with hyphen", "my-cool-app", "my-cool-app", true},
		{"with digits", "app2026", "app2026", true},
		{"uppercase normalised", "MyApp", "myapp", true},
		{"surrounding space trimmed", "  myapp  ", "myapp", true},
		{"max length", strings.Repeat("a", 63), strings.Repeat("a", 63), true},

		{"empty", "", "", false},
		{"too short", "abc", "", false},
		{"too long", strings.Repeat("a", 64), "", false},
		{"leading hyphen", "-myapp", "", false},
		{"trailing hyphen", "myapp-", "", false},
		{"underscore", "my_app", "", false},
		{"dot", "my.app", "", false},
		{"slash", "my/app", "", false},
		{"reserved", "dashboard", "", false},
		{"reserved uppercase", "ADMIN", "", false},
		{"reserved control endpoint", "agent", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Validate(tt.in)
			if tt.valid && err != nil {
				t.Fatalf("Validate(%q) = error %v, want %q", tt.in, err, tt.want)
			}
			if !tt.valid && err == nil {
				t.Fatalf("Validate(%q) = %q, want an error", tt.in, got)
			}
			if got != tt.want {
				t.Errorf("Validate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLabelFor(t *testing.T) {
	const domain = "tl.codesky.tech"
	tests := []struct {
		name  string
		host  string
		want  string
		match bool
	}{
		{"plain", "myapp.tl.codesky.tech", "myapp", true},
		{"with port", "myapp.tl.codesky.tech:443", "myapp", true},
		{"uppercase", "MyApp.TL.Codesky.Tech", "myapp", true},
		{"fully qualified", "myapp.tl.codesky.tech.", "myapp", true},
		{"generated name", "brave-otter-7f3a.tl.codesky.tech", "brave-otter-7f3a", true},

		{"bare domain", "tl.codesky.tech", "", false},
		{"nested label", "a.b.tl.codesky.tech", "", false},
		{"wrong domain", "myapp.example.com", "", false},
		{"suffix lookalike", "myapp.eviltl.codesky.tech", "", false},
		{"empty", "", "", false},
		{"ipv6 literal", "[::1]:443", "", false},
		{"parent of domain", "codesky.tech", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := LabelFor(tt.host, domain)
			if ok != tt.match {
				t.Fatalf("LabelFor(%q) matched = %v, want %v (got %q)", tt.host, ok, tt.match, got)
			}
			if got != tt.want {
				t.Errorf("LabelFor(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
