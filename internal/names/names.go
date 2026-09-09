// Package names generates and validates the labels used as tunnel subdomains.
package names

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// Generated labels look like "brave-otter-7f3a": two words plus four hex digits.
// The suffix is what actually keeps collisions rare, so the word lists only
// need to be big enough to stay readable and non-repetitive.
var adjectives = []string{
	"amber", "bold", "brave", "brisk", "calm", "clever", "cosmic", "crimson",
	"curious", "dapper", "eager", "electric", "fluent", "gentle", "golden",
	"happy", "hidden", "humble", "ivory", "jolly", "keen", "lucid", "lunar",
	"mellow", "merry", "misty", "nimble", "noble", "polar", "prime", "quiet",
	"rapid", "royal", "rustic", "shiny", "silent", "silver", "smooth", "solar",
	"spry", "steady", "stellar", "sunny", "swift", "tidy", "tranquil", "urban",
	"vivid", "warm", "witty", "zesty",
}

var nouns = []string{
	"anchor", "arrow", "badger", "beacon", "bison", "canyon", "cedar", "comet",
	"coral", "crane", "delta", "dragon", "eagle", "ember", "falcon", "ferret",
	"forest", "fox", "garden", "harbor", "heron", "island", "jaguar", "kernel",
	"lantern", "lemur", "lynx", "maple", "meadow", "mesa", "nebula", "ocelot",
	"orchid", "otter", "panda", "pebble", "pilot", "prairie", "quartz", "raven",
	"reef", "river", "sable", "salmon", "signal", "summit", "tiger", "trail",
	"tundra", "walrus", "willow",
}

// Reserved labels can never be handed out as tunnel subdomains. "agent" is the
// control-plane endpoint, and the rest are kept back for our own use so we can
// add a marketing site or API later without breaking live tunnels.
var reserved = map[string]bool{
	"_acme-challenge": true,
	"admin":           true,
	"agent":           true,
	"api":             true,
	"app":             true,
	"assets":          true,
	"blog":            true,
	"cdn":             true,
	"dashboard":       true,
	"docs":            true,
	"mail":            true,
	"ns1":             true,
	"ns2":             true,
	"static":          true,
	"status":          true,
	"support":         true,
	"tunnel":          true,
	"tunnelx":         true,
	"www":             true,
}

// MaxLabelLen is the DNS limit for a single label.
const MaxLabelLen = 63

// MinLabelLen keeps user-chosen labels long enough to be meaningful and leaves
// short labels available for future service endpoints.
const MinLabelLen = 4

// Generate returns a random, human-pronounceable label. It never returns a
// reserved label, since every generated label carries a hex suffix and the
// reserved list contains no such names.
func Generate() (string, error) {
	adj, err := pick(adjectives)
	if err != nil {
		return "", err
	}
	noun, err := pick(nouns)
	if err != nil {
		return "", err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(0x10000))
	if err != nil {
		return "", fmt.Errorf("names: read randomness: %w", err)
	}
	return fmt.Sprintf("%s-%s-%04x", adj, noun, n.Int64()), nil
}

func pick(list []string) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	if err != nil {
		return "", fmt.Errorf("names: read randomness: %w", err)
	}
	return list[n.Int64()], nil
}

// IsReserved reports whether label is held back for our own use.
func IsReserved(label string) bool {
	return reserved[strings.ToLower(label)]
}

// Validate checks a user-supplied label against DNS rules and our reserved
// list. It returns the normalised (lowercased) label.
//
// The rules are deliberately stricter than DNS: no uppercase, no underscores,
// and no leading or trailing hyphen, so a label is always safe to interpolate
// into a URL and reads the same everywhere.
func Validate(label string) (string, error) {
	label = strings.ToLower(strings.TrimSpace(label))
	switch {
	case label == "":
		return "", fmt.Errorf("subdomain is empty")
	case len(label) < MinLabelLen:
		return "", fmt.Errorf("subdomain %q is shorter than %d characters", label, MinLabelLen)
	case len(label) > MaxLabelLen:
		return "", fmt.Errorf("subdomain %q is longer than %d characters", label, MaxLabelLen)
	case label[0] == '-' || label[len(label)-1] == '-':
		return "", fmt.Errorf("subdomain %q must not start or end with a hyphen", label)
	}
	for _, r := range label {
		isDigit := r >= '0' && r <= '9'
		isLower := r >= 'a' && r <= 'z'
		if !isDigit && !isLower && r != '-' {
			return "", fmt.Errorf("subdomain %q may only contain a-z, 0-9 and hyphens", label)
		}
	}
	if IsReserved(label) {
		return "", fmt.Errorf("subdomain %q is reserved", label)
	}
	return label, nil
}

// LabelFor extracts the tunnel label from an HTTP Host header, given the
// service's base domain. It strips any port, lowercases, and requires exactly
// one label in front of the base domain, so "a.b.tl.codesky.tech" is rejected
// rather than silently routed to "a".
func LabelFor(host, domain string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	// Trim a port. Host may be an IPv6 literal in brackets, which cannot carry
	// a tunnel label anyway, so treat any bracketed form as no match.
	if strings.HasPrefix(host, "[") {
		return "", false
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ".") // fully-qualified form
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))

	suffix := "." + domain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	label := host[:len(host)-len(suffix)]
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}
