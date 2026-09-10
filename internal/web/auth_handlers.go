package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const sessionTTL = 30 * 24 * time.Hour

func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	user := h.getAuthenticatedUser(r)
	h.render(w, "landing.html", PageData{
		User: user,
	})
}

func (h *Handler) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if h.getAuthenticatedUser(r) != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	h.render(w, "login.html", PageData{
		FlashError:   r.URL.Query().Get("error"),
		FlashSuccess: r.URL.Query().Get("success"),
	})
}

func (h *Handler) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	if h.getAuthenticatedUser(r) != nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	h.render(w, "register.html", PageData{
		FlashError: r.URL.Query().Get("error"),
	})
}

func (h *Handler) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=Invalid+form+submission", http.StatusSeeOther)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	acc, err := h.cfg.Store.AuthenticatePassword(r.Context(), email, password)
	if err != nil {
		http.Redirect(w, r, "/login?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	sessionID, err := h.cfg.Store.CreateWebSession(r.Context(), acc.ID, sessionTTL)
	if err != nil {
		h.log.Error("create web session failed", "error", err)
		http.Redirect(w, r, "/login?error=Failed+to+create+session", http.StatusSeeOther)
		return
	}

	h.setSessionCookie(w, sessionID, time.Now().Add(sessionTTL))
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (h *Handler) handleRegisterPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/register?error=Invalid+form+submission", http.StatusSeeOther)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	acc, err := h.cfg.Store.CreateAccountWithPassword(r.Context(), email, password)
	if err != nil {
		http.Redirect(w, r, "/register?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	sessionID, err := h.cfg.Store.CreateWebSession(r.Context(), acc.ID, sessionTTL)
	if err != nil {
		h.log.Error("create web session failed", "error", err)
		http.Redirect(w, r, "/login?error=Account+created+please+sign+in", http.StatusSeeOther)
		return
	}

	h.setSessionCookie(w, sessionID, time.Now().Add(sessionTTL))
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (h *Handler) handleLogoutPost(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		_ = h.cfg.Store.DeleteWebSession(r.Context(), cookie.Value)
	}
	h.clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// GitHub OAuth 2.0 Flow
func (h *Handler) handleGitHubRedirect(w http.ResponseWriter, r *http.Request) {
	if h.cfg.GitHubClientID == "" {
		http.Redirect(w, r, "/login?error=GitHub+login+not+configured", http.StatusSeeOther)
		return
	}

	callbackURL := fmt.Sprintf("https://%s/auth/github/callback", h.cfg.Domain)
	ghURL := fmt.Sprintf("https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=user:email",
		url.QueryEscape(h.cfg.GitHubClientID),
		url.QueryEscape(callbackURL))

	http.Redirect(w, r, ghURL, http.StatusTemporaryRedirect)
}

func (h *Handler) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/login?error=GitHub+authorization+failed", http.StatusSeeOther)
		return
	}

	// 1. Exchange code for access token
	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     h.cfg.GitHubClientID,
		"client_secret": h.cfg.GitHubClientSecret,
		"code":          code,
	})

	tokenReq, err := http.NewRequestWithContext(r.Context(), "POST", "https://github.com/login/oauth/access_token", bytes.NewReader(tokenBody))
	if err != nil {
		http.Redirect(w, r, "/login?error=OAuth+token+exchange+failed", http.StatusSeeOther)
		return
	}
	tokenReq.Header.Set("Content-Type", "application/json")
	tokenReq.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(tokenReq)
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Redirect(w, r, "/login?error=Failed+to+exchange+GitHub+code", http.StatusSeeOther)
		return
	}
	defer resp.Body.Close()

	var tokenData struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenData); err != nil || tokenData.AccessToken == "" {
		http.Redirect(w, r, "/login?error=GitHub+OAuth+rejected", http.StatusSeeOther)
		return
	}

	// 2. Fetch GitHub User Profile
	userReq, _ := http.NewRequestWithContext(r.Context(), "GET", "https://api.github.com/user", nil)
	userReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
	userReq.Header.Set("Accept", "application/vnd.github.v3+json")

	userResp, err := http.DefaultClient.Do(userReq)
	if err != nil || userResp.StatusCode != http.StatusOK {
		http.Redirect(w, r, "/login?error=Failed+to+fetch+GitHub+profile", http.StatusSeeOther)
		return
	}
	defer userResp.Body.Close()

	var ghUser struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(userResp.Body).Decode(&ghUser); err != nil {
		http.Redirect(w, r, "/login?error=Failed+to+parse+GitHub+profile", http.StatusSeeOther)
		return
	}

	// 3. If primary email is private in profile, fetch from emails endpoint
	if ghUser.Email == "" {
		emailsReq, _ := http.NewRequestWithContext(r.Context(), "GET", "https://api.github.com/user/emails", nil)
		emailsReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
		emailsReq.Header.Set("Accept", "application/vnd.github.v3+json")

		if emailsResp, err := http.DefaultClient.Do(emailsReq); err == nil && emailsResp.StatusCode == http.StatusOK {
			defer emailsResp.Body.Close()
			var emails []struct {
				Email   string `json:"email"`
				Primary bool   `json:"primary"`
			}
			if err := json.NewDecoder(emailsResp.Body).Decode(&emails); err == nil {
				for _, e := range emails {
					if e.Primary {
						ghUser.Email = e.Email
						break
					}
				}
			}
		}
	}

	// 4. Find or create account in Store
	acc, err := h.cfg.Store.FindOrCreateGitHubAccount(r.Context(),
		fmt.Sprintf("%d", ghUser.ID),
		ghUser.Login,
		ghUser.Email,
		ghUser.AvatarURL,
	)
	if err != nil {
		h.log.Error("FindOrCreateGitHubAccount failed", "error", err)
		http.Redirect(w, r, "/login?error=Failed+to+link+account", http.StatusSeeOther)
		return
	}

	// 5. Create web session
	sessionID, err := h.cfg.Store.CreateWebSession(r.Context(), acc.ID, sessionTTL)
	if err != nil {
		http.Redirect(w, r, "/login?error=Failed+to+create+session", http.StatusSeeOther)
		return
	}

	h.setSessionCookie(w, sessionID, time.Now().Add(sessionTTL))
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}
