package store

import (
	"context"
	"testing"
	"time"
)

func TestCreateAccountWithPasswordAndAuth(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	// 1. Create account
	acc, err := s.CreateAccountWithPassword(ctx, "dev@example.com", "secret123")
	if err != nil {
		t.Fatalf("CreateAccountWithPassword failed: %v", err)
	}
	if acc.Email != "dev@example.com" {
		t.Errorf("got email %q, want dev@example.com", acc.Email)
	}

	// 2. Reject short password
	if _, err := s.CreateAccountWithPassword(ctx, "dev2@example.com", "123"); err == nil {
		t.Errorf("expected error for short password")
	}

	// 3. Authenticate with correct password
	authed, err := s.AuthenticatePassword(ctx, "dev@example.com", "secret123")
	if err != nil {
		t.Fatalf("AuthenticatePassword failed: %v", err)
	}
	if authed.ID != acc.ID {
		t.Errorf("got ID %s, want %s", authed.ID, acc.ID)
	}

	// 4. Authenticate with incorrect password
	if _, err := s.AuthenticatePassword(ctx, "dev@example.com", "wrongpass"); err == nil {
		t.Errorf("expected authentication failure for wrong password")
	}
}

func TestFindOrCreateGitHubAccount(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	// 1. First login creates account
	acc, err := s.FindOrCreateGitHubAccount(ctx, "gh_12345", "octocat", "octo@github.com", "https://avatar.com/1")
	if err != nil {
		t.Fatalf("FindOrCreateGitHubAccount: %v", err)
	}
	if acc.GitHubUsername != "octocat" || acc.GitHubID != "gh_12345" {
		t.Errorf("unexpected account fields: %+v", acc)
	}

	// 2. Second login returns existing account and updates fields if changed
	acc2, err := s.FindOrCreateGitHubAccount(ctx, "gh_12345", "octocat_renamed", "octo@github.com", "https://avatar.com/2")
	if err != nil {
		t.Fatalf("FindOrCreateGitHubAccount second call: %v", err)
	}
	if acc2.ID != acc.ID {
		t.Errorf("got different account ID on second login: %s vs %s", acc2.ID, acc.ID)
	}
	if acc2.GitHubUsername != "octocat_renamed" {
		t.Errorf("username not updated: %s", acc2.GitHubUsername)
	}
}

func TestWebSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	acc, err := s.CreateAccount(ctx, "sessionuser@example.com")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Create session
	token, err := s.CreateWebSession(ctx, acc.ID, time.Hour)
	if err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}
	if token == "" {
		t.Fatal("empty session token")
	}

	// Validate session
	validated, err := s.ValidateWebSession(ctx, token)
	if err != nil {
		t.Fatalf("ValidateWebSession: %v", err)
	}
	if validated.ID != acc.ID {
		t.Errorf("got account ID %s, want %s", validated.ID, acc.ID)
	}

	// Delete session (logout)
	if err := s.DeleteWebSession(ctx, token); err != nil {
		t.Fatalf("DeleteWebSession: %v", err)
	}

	// Validate after delete should fail
	if _, err := s.ValidateWebSession(ctx, token); err == nil {
		t.Error("expected error validating deleted session")
	}
}
