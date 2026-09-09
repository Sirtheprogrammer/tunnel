// Package config loads and saves the tunnelx client configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

// DefaultServerAddr is the production control endpoint. It sits on a
// DNS-only record so agent connections reach the origin directly.
const DefaultServerAddr = "agent.tl.codesky.tech:7835"

// Client is the on-disk client configuration, ~/.tunnelx/config.yaml.
type Client struct {
	// Token authenticates this machine. Written by `tunnelx login`.
	Token string `yaml:"token,omitempty"`

	// ServerAddr overrides the control endpoint, for self-hosted servers.
	ServerAddr string `yaml:"server_addr,omitempty"`

	// Insecure skips certificate verification. Development only.
	Insecure bool `yaml:"insecure,omitempty"`

	// InspectAddr is the default listen address for the web inspector.
	InspectAddr string `yaml:"inspect_addr,omitempty"`
}

// Path returns the config file location, honouring TUNNELX_CONFIG.
func Path() (string, error) {
	if p := os.Getenv("TUNNELX_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(dir, ".tunnelx", "config.yaml"), nil
}

// Load reads the config. A missing file is not an error: it returns defaults,
// so a first run without `tunnelx login` still works against a server that
// allows anonymous tunnels.
func Load() (*Client, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Client{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Client
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// Save writes the config with owner-only permissions, since it holds a token.
func (c *Client) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	// Write to a temporary file and rename, so an interrupted save cannot leave
	// a truncated config that locks the user out of their own token.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Server returns the control endpoint to dial.
func (c *Client) Server() string {
	if c.ServerAddr != "" {
		return c.ServerAddr
	}
	return DefaultServerAddr
}
