// Package repo parses repository references and resolves them against
// GitHub: default branch, head commit, size, visibility.
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Ref is a parsed owner/repo pair.
type Ref struct {
	Owner string
	Name  string
}

// Slug is the canonical "owner/repo" form.
func (r Ref) Slug() string { return r.Owner + "/" + r.Name }

// Info is what resolution learns about a repository.
type Info struct {
	Ref
	DefaultBranch string
	SHA           string
	SizeKB        int
	Private       bool
	CloneURL      string
}

// Resolution failures the API maps to "unsupported" or "not_found".
var (
	ErrInvalidRef = errors.New("repo: not a GitHub repository reference")
	ErrNotFound   = errors.New("repo: repository not found")
	ErrPrivate    = errors.New("repo: repository is private")
	ErrTooLarge   = errors.New("repo: repository is too large")
	ErrRateLimit  = errors.New("repo: GitHub rate limit reached")
)

// MaxSizeKB bounds what a build sandbox will clone.
const MaxSizeKB = 500 * 1024

var (
	slugRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$`)
	partRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Parse accepts "owner/repo", "github.com/owner/repo", full https URLs with
// or without .git and trailing paths, and git@github.com:owner/repo.git.
func Parse(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, ErrInvalidRef
	}
	if strings.HasPrefix(s, "git@github.com:") {
		s = "https://github.com/" + strings.TrimPrefix(s, "git@github.com:")
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "www.github.com/") {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		u, err := url.Parse(s)
		if err != nil || (u.Host != "github.com" && u.Host != "www.github.com") {
			return Ref{}, ErrInvalidRef
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return Ref{}, ErrInvalidRef
		}
		s = parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	}
	if !slugRE.MatchString(s) {
		return Ref{}, ErrInvalidRef
	}
	owner, name, _ := strings.Cut(s, "/")
	name = strings.TrimSuffix(name, ".git")
	if !partRE.MatchString(name) || name == "." || name == ".." {
		return Ref{}, ErrInvalidRef
	}
	return Ref{Owner: owner, Name: name}, nil
}

// Resolver looks up repository metadata; the GitHub implementation is the
// only real one.
type Resolver interface {
	Resolve(ctx context.Context, ref Ref) (Info, error)
}

// GitHub resolves through the REST API. A token raises the rate limit from
// 60 to 5000 requests per hour and is strongly recommended in production.
type GitHub struct {
	Token string
	HTTP  *http.Client
	Base  string
}

// NewGitHub returns a resolver for api.github.com.
func NewGitHub(token string) *GitHub {
	return &GitHub{Token: token, HTTP: &http.Client{Timeout: 15 * time.Second}, Base: "https://api.github.com"}
}

// Resolve fetches the repository record and the head commit of its default
// branch, rejecting private or oversized repositories.
func (g *GitHub) Resolve(ctx context.Context, ref Ref) (Info, error) {
	var meta struct {
		DefaultBranch string `json:"default_branch"`
		Size          int    `json:"size"`
		Private       bool   `json:"private"`
		CloneURL      string `json:"clone_url"`
	}
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/%s", ref.Owner, ref.Name), &meta); err != nil {
		return Info{}, err
	}
	if meta.Private {
		return Info{}, ErrPrivate
	}
	if meta.Size > MaxSizeKB {
		return Info{}, ErrTooLarge
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/%s/commits/%s", ref.Owner, ref.Name, url.PathEscape(meta.DefaultBranch)), &commit); err != nil {
		return Info{}, err
	}
	return Info{Ref: ref, DefaultBranch: meta.DefaultBranch, SHA: commit.SHA, SizeKB: meta.Size, CloneURL: meta.CloneURL}, nil
}

// get performs one API call, mapping 404 and 403-rate-limit to typed errors.
func (g *GitHub) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "trylive")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return ErrRateLimit
	case resp.StatusCode >= 300:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("github %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
