package repo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParse(t *testing.T) {
	good := map[string]Ref{
		"owner/repo":                                   {"owner", "repo"},
		"  Owner/My.Repo_x  ":                          {"Owner", "My.Repo_x"},
		"https://github.com/owner/repo":                {"owner", "repo"},
		"https://github.com/owner/repo.git":            {"owner", "repo"},
		"https://github.com/owner/repo/":               {"owner", "repo"},
		"https://github.com/owner/repo/tree/main/docs": {"owner", "repo"},
		"http://www.github.com/owner/repo":             {"owner", "repo"},
		"github.com/owner/repo":                        {"owner", "repo"},
		"git@github.com:owner/repo.git":                {"owner", "repo"},
	}
	for in, want := range good {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "owner", "owner/", "/repo", "https://gitlab.com/o/r", "owner/re po", "owner/../x", "-owner/repo", "https://github.com/onlyowner"} {
		if _, err := Parse(in); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Parse(%q) err = %v", in, err)
		}
	}
}

func TestGitHubResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r":
			w.Write([]byte(`{"default_branch":"main","size":1234,"private":false,"clone_url":"https://github.com/o/r.git"}`))
		case "/repos/o/r/commits/main":
			w.Write([]byte(`{"sha":"abc123"}`))
		case "/repos/o/priv":
			w.Write([]byte(`{"default_branch":"main","size":1,"private":true}`))
		case "/repos/o/big":
			w.Write([]byte(`{"default_branch":"main","size":9999999,"private":false}`))
		case "/repos/o/limited":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(403)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	g := NewGitHub("tok")
	g.Base = srv.URL
	ctx := context.Background()

	info, err := g.Resolve(ctx, Ref{"o", "r"})
	if err != nil || info.SHA != "abc123" || info.DefaultBranch != "main" || info.CloneURL == "" {
		t.Fatalf("Resolve = %+v, %v", info, err)
	}
	for name, want := range map[string]error{"priv": ErrPrivate, "big": ErrTooLarge, "limited": ErrRateLimit, "missing": ErrNotFound} {
		if _, err := g.Resolve(ctx, Ref{"o", name}); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", name, err, want)
		}
	}
}
