package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tunnel/internal/store"
)

func setupTestWeb(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	s, err := store.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}

	h, err := New(Config{
		Domain: "tl.example.com",
		Store:  s,
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return h, s
}

func TestPublicRoutes(t *testing.T) {
	h, _ := setupTestWeb(t)

	routes := []struct {
		path         string
		expectedCode int
		contains     string
	}{
		{"/", http.StatusOK, "Expose your localhost"},
		{"/login", http.StatusOK, "Sign In"},
		{"/register", http.StatusOK, "Create an Account"},
		{"/static/app.css", http.StatusOK, "--bg-main"},
		{"/install.sh", http.StatusOK, "#!/bin/sh"},
		{"/install.ps1", http.StatusOK, "Installing TunnelX CLI"},
	}

	for _, tc := range routes {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", tc.path, nil)
			h.ServeHTTP(rec, req)

			if rec.Code != tc.expectedCode {
				t.Fatalf("expected status %d for %s, got %d", tc.expectedCode, tc.path, rec.Code)
			}
			if tc.contains != "" && !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("expected body to contain %q, body:\n%s", tc.contains, rec.Body.String()[:200])
			}
		})
	}
}

func TestAuthAndDashboardFlow(t *testing.T) {
	h, _ := setupTestWeb(t)

	// 1. Unauthorized GET /dashboard should redirect to /login
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/dashboard", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to login, got %d", rec.Code)
	}

	// 2. Register account
	form := url.Values{
		"email":    {"testuser@example.com"},
		"password": {"password123"},
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/auth/register", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected register redirect, got %d", rec.Code)
	}

	// Extract session cookie
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie on register")
	}

	// 3. Authenticated access to dashboard
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(sessionCookie)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on dashboard with cookie, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "testuser@example.com") {
		t.Error("expected dashboard to show user email")
	}

	// 4. Create an authtoken from dashboard
	tokForm := url.Values{"name": {"MacBook Air"}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/dashboard/tokens/create", strings.NewReader(tokForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected token creation redirect, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "created_token=tx_") {
		t.Fatalf("expected created_token in location redirect, got %s", loc)
	}

	// 5. Reserve a subdomain from dashboard
	subForm := url.Values{"subdomain": {"coolapp"}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/dashboard/subdomains/reserve", strings.NewReader(subForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookie)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected reserve redirect, got %d", rec.Code)
	}

	// 6. Logout
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(sessionCookie)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected logout redirect, got %d", rec.Code)
	}
}
