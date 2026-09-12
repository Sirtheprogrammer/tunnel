package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"tunnel/internal/store"
)

//go:embed static/* templates/*
var embeddedFS embed.FS

const sessionCookieName = "tx_session"

// Config carries parameters for the web portal.
type Config struct {
	Domain             string
	Store              *store.Store
	GitHubClientID     string
	GitHubClientSecret string
	Logger             *slog.Logger
}

// Handler serves the web portal (landing page, auth, dashboard).
type Handler struct {
	cfg       Config
	templates map[string]*template.Template
	mux       *http.ServeMux
	log       *slog.Logger
	stars     starsCache
}

// New creates and initializes a web portal HTTP handler.
func New(cfg Config) (*Handler, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	h := &Handler{
		cfg:       cfg,
		templates: make(map[string]*template.Template),
		mux:       http.NewServeMux(),
		log:       cfg.Logger,
	}

	if err := h.loadTemplates(); err != nil {
		return nil, fmt.Errorf("load web templates: %w", err)
	}

	h.registerRoutes()
	return h, nil
}

func (h *Handler) loadTemplates() error {
	// Parse base layout first
	baseBytes, err := embeddedFS.ReadFile("templates/base.html")
	if err != nil {
		return err
	}

	pages := []string{"landing.html", "login.html", "register.html", "dashboard.html"}
	for _, page := range pages {
		pageBytes, err := embeddedFS.ReadFile("templates/" + page)
		if err != nil {
			return fmt.Errorf("read %s: %w", page, err)
		}

		tmpl := template.New(page)
		if _, err := tmpl.Parse(string(baseBytes)); err != nil {
			return fmt.Errorf("parse base for %s: %w", page, err)
		}
		if _, err := tmpl.Parse(string(pageBytes)); err != nil {
			return fmt.Errorf("parse page %s: %w", page, err)
		}

		h.templates[page] = tmpl
	}
	return nil
}

func (h *Handler) registerRoutes() {
	// Static assets
	staticSub, _ := fs.Sub(embeddedFS, "static")
	h.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Install scripts: curl ... | bash and irm ... | iex
	h.mux.HandleFunc("/install.sh", h.handleInstallScript)
	h.mux.HandleFunc("/install.ps1", h.handleInstallPS1)

	// GitHub star count for the landing page header
	h.mux.HandleFunc("/api/github-stars", h.handleGitHubStars)

	// Public landing & auth
	h.mux.HandleFunc("/", h.handleRoot)
	h.mux.HandleFunc("/login", h.handleLoginPage)
	h.mux.HandleFunc("/register", h.handleRegisterPage)
	h.mux.HandleFunc("/auth/login", h.handleLoginPost)
	h.mux.HandleFunc("/auth/register", h.handleRegisterPost)
	h.mux.HandleFunc("/auth/logout", h.handleLogoutPost)

	// GitHub OAuth
	h.mux.HandleFunc("/auth/github", h.handleGitHubRedirect)
	h.mux.HandleFunc("/auth/github/callback", h.handleGitHubCallback)

	// Authenticated Dashboard
	h.mux.HandleFunc("/dashboard", h.requireAuth(h.handleDashboard))
	h.mux.HandleFunc("/dashboard/tokens/create", h.requireAuth(h.handleCreateToken))
	h.mux.HandleFunc("/dashboard/tokens/revoke", h.requireAuth(h.handleRevokeToken))
	h.mux.HandleFunc("/dashboard/subdomains/reserve", h.requireAuth(h.handleReserveSubdomain))
	h.mux.HandleFunc("/dashboard/subdomains/release", h.requireAuth(h.handleReleaseSubdomain))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// PageData carries template values.
type PageData struct {
	Domain        string
	User          *store.Account
	GitHubEnabled bool
	FlashError    string
	FlashSuccess  string

	// Dashboard specific
	Tokens       []*store.Token
	Reservations []*store.Reservation
	Sessions     []*store.TunnelSession
	CreatedToken string
}

func (h *Handler) render(w http.ResponseWriter, page string, data PageData) {
	tmpl, ok := h.templates[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	data.Domain = h.cfg.Domain
	data.GitHubEnabled = h.cfg.GitHubClientID != "" && h.cfg.GitHubClientSecret != ""

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		h.log.Error("render template error", "page", page, "error", err)
	}
}

// requireAuth middleware ensures the user is logged in via session cookie.
func (h *Handler) requireAuth(next func(w http.ResponseWriter, r *http.Request, acc *store.Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := h.getAuthenticatedUser(r)
		if acc == nil {
			http.Redirect(w, r, "/login?error=Please+sign+in+first", http.StatusSeeOther)
			return
		}
		next(w, r, acc)
	}
}

func (h *Handler) getAuthenticatedUser(r *http.Request) *store.Account {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	acc, err := h.cfg.Store.ValidateWebSession(r.Context(), cookie.Value)
	if err != nil {
		return nil
	}
	return acc
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   true,
	})
}
