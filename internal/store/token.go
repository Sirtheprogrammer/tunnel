package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
)

// TokenPrefix marks a tunnelx authtoken. Having a fixed, searchable prefix lets
// secret scanners recognise a leaked token, and lets us tell a token apart from
// something else a user pasted by mistake.
const TokenPrefix = "tx_"

// tokenBytes is the amount of entropy in a token. 32 bytes is well beyond
// guessing range and keeps the encoded form a manageable length.
const tokenBytes = 32

// tokenEncoding is URL-safe and unpadded, so a token survives being passed on a
// command line, in a URL, or in a YAML file without quoting or escaping.
var tokenEncoding = base64.RawURLEncoding

// NewToken returns a fresh authtoken. The plaintext is returned once and never
// stored; only its hash goes to the database.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return TokenPrefix + tokenEncoding.EncodeToString(b), nil
}

// HashToken returns the lookup hash for a token.
//
// A plain SHA-256 is the right choice here, unlike for a password: the token is
// 256 bits of uniform randomness, so there is no dictionary to attack and a
// slow KDF would only add latency to every agent connection.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// TokenDisplayPrefix returns the leading characters kept for display, so an
// operator can tell which token a row refers to without being able to use it.
func TokenDisplayPrefix(token string) string {
	const n = len(TokenPrefix) + 6
	if len(token) < n {
		return token
	}
	return token[:n]
}

// ValidTokenShape reports whether a string looks like a tunnelx token. It is a
// cheap check to reject obvious mistakes before touching the database; it says
// nothing about whether the token is real.
func ValidTokenShape(token string) bool {
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	raw, err := tokenEncoding.DecodeString(strings.TrimPrefix(token, TokenPrefix))
	return err == nil && len(raw) == tokenBytes
}

// equalHash compares two hashes in constant time.
func equalHash(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
