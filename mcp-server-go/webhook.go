// Package main: optional webhook listener for git push events.
// Receives JSON from Gitea/GitLab/GitHub/Gogs, extracts repo + branch, and
// triggers deploy.DeployFromGit asynchronously. Only active when the
// CUBE_WEBHOOK_ENABLED env var is set to "true".
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// webhookConfig is populated from env vars at startup.
var webhookConfig = struct {
	enabled bool
	secret  string
}{
	enabled: strings.EqualFold(os.Getenv("CUBE_WEBHOOK_ENABLED"), "true"),
	secret:  os.Getenv("CUBE_WEBHOOK_SECRET"),
}

// gitWebhookPayload is a permissive struct that covers the common shapes used
// by GitHub, Gitea, Gogs (repository.clone_url), GitLab (project.git_http_url),
// and generic providers (repository.url). Unknown fields are ignored.
type gitWebhookPayload struct {
	Ref        string `json:"ref"`    // e.g. "refs/heads/main"
	After      string `json:"after"`  // commit SHA after push
	Before     string `json:"before"` // commit SHA before push (empty on create)
	Repository struct {
		CloneURL string `json:"clone_url"` // GitHub, Gitea, Gogs
		URL      string `json:"url"`       // generic fallback
		HTMLURL  string `json:"html_url"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Project struct {
		GitHTTPURL string `json:"git_http_url"` // GitLab
		WebURL     string `json:"web_url"`
	} `json:"project"`
	// GitLab uses "checkout_sha" instead of "after"
	CheckoutSHA string `json:"checkout_sha"`
	// Gitea/Gogs/GitHub push event type discriminator
	Commit string `json:"commits"` // present on push events (ignored, used for detection)
}

// webhookResponse is the JSON returned to the webhook caller.
type webhookResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	RepoURL   string `json:"repo_url,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Event     string `json:"event,omitempty"`
}

// handleGitWebhook is the HTTP handler for POST /webhook/git.
// It parses provider-specific JSON, validates an optional secret, and fires
// off an asynchronous deploy.DeployFromGit. Returns 200 immediately with the
// deploy trigger status; 400 on a malformed/unparseable payload; 401 if a
// configured secret is missing or wrong.
func handleGitWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Method check
	if r.Method != http.MethodPost {
		writeWebhookJSON(w, http.StatusMethodNotAllowed, webhookResponse{
			Status:  "error",
			Message: fmt.Sprintf("method %s not allowed; use POST", r.Method),
		})
		return
	}

	// Optional secret validation (X-Git-Token header OR ?token= query param)
	if webhookConfig.secret != "" {
		provided := r.Header.Get("X-Git-Token")
		if provided == "" {
			provided = r.URL.Query().Get("token")
		}
		if provided != webhookConfig.secret {
			writeWebhookJSON(w, http.StatusUnauthorized, webhookResponse{
				Status:  "error",
				Message: "invalid or missing webhook secret",
			})
			return
		}
	}

	// Parse JSON body
	var payload gitWebhookPayload
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&payload); err != nil {
		writeWebhookJSON(w, http.StatusBadRequest, webhookResponse{
			Status:  "error",
			Message: fmt.Sprintf("invalid JSON body: %v", err),
		})
		return
	}

	// Extract repo URL across provider formats.
	// Order: GitHub/Gitea/Gogs (repository.clone_url) → GitLab (project.git_http_url) → generic (repository.url)
	repoURL := payload.Repository.CloneURL
	if repoURL == "" {
		repoURL = payload.Project.GitHTTPURL
	}
	if repoURL == "" {
		repoURL = payload.Repository.URL
	}
	if repoURL == "" {
		writeWebhookJSON(w, http.StatusBadRequest, webhookResponse{
			Status:  "error",
			Message: "could not extract repository URL from payload (tried repository.clone_url, project.git_http_url, repository.url)",
		})
		return
	}

	// Extract branch from ref (e.g. "refs/heads/main" → "main").
	// Fall back to "main" if absent (some create/tag events omit it).
	branch := branchFromRef(payload.Ref)

	// Commit SHA: GitLab uses checkout_sha, others use after.
	commitSHA := payload.After
	if commitSHA == "" {
		commitSHA = payload.CheckoutSHA
	}

	// Event type: presence of a ref + commits indicates a push.
	// 0000000… before indicates branch creation; after all-zeros indicates deletion.
	event := "push"
	if isZeroSHA(payload.Before) {
		event = "create"
	} else if isZeroSHA(payload.After) {
		event = "delete"
	}

	// Fire deploy asynchronously so we can return 200 immediately.
	go func(p gitWebhookPayload, url, br, sha, ev string) {
		// Skip delete events — nothing to deploy.
		if ev == "delete" {
			fmt.Fprintf(os.Stderr, "[webhook] ignoring %s event for %s @ %s\n", ev, url, br)
			return
		}
		fmt.Fprintf(os.Stderr, "[webhook] triggering deploy: %s @ %s (sha=%s, event=%s)\n", url, br, sha, ev)
		_, err := deploy.DeployFromGit(url, br, "", []int{8000}, nil, "", "", 256, 1.0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[webhook] deploy failed for %s: %v\n", url, err)
		} else {
			fmt.Fprintf(os.Stderr, "[webhook] deploy triggered for %s\n", url)
		}
	}(payload, repoURL, branch, commitSHA, event)

	// Respond immediately — deploy runs in background.
	writeWebhookJSON(w, http.StatusOK, webhookResponse{
		Status:    "accepted",
		Message:   "deploy triggered",
		RepoURL:   repoURL,
		Branch:    branch,
		CommitSHA: commitSHA,
		Event:     event,
	})
}

// branchFromRef converts a git ref like "refs/heads/main" or "refs/tags/v1"
// into the short branch/tag name ("main", "v1"). Returns "main" for empty input.
func branchFromRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "main"
	}
	for _, prefix := range []string{"refs/heads/", "refs/tags/", "refs/"} {
		if strings.HasPrefix(ref, prefix) {
			trimmed := strings.TrimPrefix(ref, prefix)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ref
}

// isZeroSHA returns true for the all-zero SHA used by git to signal branch
// creation (before) or deletion (after).
func isZeroSHA(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	for _, c := range sha {
		if c != '0' {
			return false
		}
	}
	return true
}

// writeWebhookJSON writes a webhookResponse with the given HTTP status code.
func writeWebhookJSON(w http.ResponseWriter, status int, resp webhookResponse) {
	w.WriteHeader(status)
	// Marshal directly (not via toJSON) so a marshal failure here doesn't
	// recurse or depend on other files; fall back to a minimal body.
	b, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, `{"status":"error","message":"internal marshal failure"}`, status)
		return
	}
	w.Write(b)
}
