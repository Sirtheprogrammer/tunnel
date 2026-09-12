package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	githubRepoAPIURL    = "https://api.github.com/repos/Sirtheprogrammer/tunnel"
	githubStarsCacheTTL = 10 * time.Minute
)

// starsCache holds the last known star count for the repo, shared across all
// visitors. Without this, every page load would call api.github.com directly,
// and that endpoint caps unauthenticated requests at 60/hour per IP — a limit
// a handful of concurrent visitors behind the same proxy/NAT could exhaust.
type starsCache struct {
	mu        sync.Mutex
	count     int
	fetchedAt time.Time
}

// handleGitHubStars returns {"stars": N} for the repo, serving from an
// in-memory cache and only calling the GitHub API once the cache goes stale.
func (h *Handler) handleGitHubStars(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	h.stars.mu.Lock()
	fresh := time.Since(h.stars.fetchedAt) < githubStarsCacheTTL
	count := h.stars.count
	h.stars.mu.Unlock()

	if fresh {
		json.NewEncoder(w).Encode(map[string]int{"stars": count})
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, githubRepoAPIURL, nil)
	if err == nil {
		req.Header.Set("Accept", "application/vnd.github+json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var repo struct {
					StargazersCount int `json:"stargazers_count"`
				}
				if json.NewDecoder(resp.Body).Decode(&repo) == nil {
					h.stars.mu.Lock()
					h.stars.count = repo.StargazersCount
					h.stars.fetchedAt = time.Now()
					count = h.stars.count
					h.stars.mu.Unlock()
				}
			}
		}
	}

	// On any failure above, count still holds the last known (possibly stale)
	// value rather than zero, so a transient GitHub error doesn't blank the badge.
	json.NewEncoder(w).Encode(map[string]int{"stars": count})
}
