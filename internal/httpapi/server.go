// Package httpapi exposes sessions over HTTP: JSON endpoints, a server-sent
// event stream, a websocket terminal relay, the README badge, and the
// embedded web app.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mayur-tolexo/trylive/internal/recipe"
	"github.com/mayur-tolexo/trylive/internal/repo"
	"github.com/mayur-tolexo/trylive/internal/sandbox"
	"github.com/mayur-tolexo/trylive/internal/session"
	"github.com/mayur-tolexo/trylive/internal/store"
)

// Server wires the session manager to HTTP.
type Server struct {
	Sessions *session.Manager
	Store    store.Store
	Static   fs.FS // built web app; nil serves the API only
	Log      *slog.Logger

	metrics *metrics
}

// Handler builds the router. Call once.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	s.metrics = newMetrics()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /metrics", s.metrics.handler())
	mux.HandleFunc("POST /v1/sessions", s.handleCreate)
	mux.HandleFunc("GET /v1/sessions/{id}", s.handleGet)
	mux.HandleFunc("GET /v1/sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("GET /v1/sessions/{id}/pty", s.handlePTY)
	mux.HandleFunc("POST /v1/sessions/{id}/extend", s.handleExtend)
	mux.HandleFunc("GET /v1/builds/gh/{owner}/{repo}", s.handleBuild)
	mux.HandleFunc("GET /badge/gh/{owner}/{file}", s.handleBadge)
	if s.Static != nil {
		mux.Handle("/", spaHandler(s.Static))
	}
	return s.logRequests(mux)
}

// apiError is the wire shape of every failure.
type apiError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter int    `json:"retry_after_seconds,omitempty"`
}

// writeJSON serialises v with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError maps domain errors to the wire shape; unknown errors become a
// generic 500 so internals never leak.
func (s *Server) writeError(w http.ResponseWriter, err error) {
	status, ae := http.StatusInternalServerError, apiError{Code: "internal", Message: "something went wrong"}
	switch {
	case errors.Is(err, repo.ErrInvalidRef):
		status, ae = http.StatusBadRequest, apiError{Code: "invalid", Message: "that is not a GitHub repository"}
	case errors.Is(err, repo.ErrNotFound), errors.Is(err, store.ErrNotFound), errors.Is(err, sandbox.ErrNotFound):
		status, ae = http.StatusNotFound, apiError{Code: "not_found", Message: "not found"}
	case errors.Is(err, repo.ErrPrivate):
		status, ae = http.StatusUnprocessableEntity, apiError{Code: "unsupported", Message: "private repositories are not supported"}
	case errors.Is(err, repo.ErrTooLarge):
		status, ae = http.StatusUnprocessableEntity, apiError{Code: "unsupported", Message: "repository is too large to try live"}
	case errors.Is(err, repo.ErrRateLimit):
		status, ae = http.StatusServiceUnavailable, apiError{Code: "busy", Message: "GitHub rate limit reached, try again shortly", RetryAfter: 60}
	case errors.Is(err, session.ErrLimited):
		status, ae = http.StatusTooManyRequests, apiError{Code: "rate_limited", Message: "you already have a live session; it ends when you close it or after 15 minutes", RetryAfter: 60}
	case errors.Is(err, session.ErrBusy), errors.Is(err, sandbox.ErrBusy):
		status, ae = http.StatusServiceUnavailable, apiError{Code: "busy", Message: "every sandbox is in use right now", RetryAfter: 5}
	case errors.Is(err, session.ErrNotLive):
		status, ae = http.StatusConflict, apiError{Code: "invalid", Message: "session is not live"}
	case errors.Is(err, session.ErrAlreadyExtnd):
		status, ae = http.StatusBadRequest, apiError{Code: "invalid", Message: "session was already extended"}
	default:
		s.Log.Error("internal error", "err", err)
	}
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ae.RetryAfter))
	}
	writeJSON(w, status, map[string]apiError{"error": ae})
}

// deviceID validates the client's device header; missing means empty, which
// still counts as one device for limits.
var deviceRE = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func deviceID(r *http.Request) string {
	d := r.Header.Get("X-Device-Id")
	if !deviceRE.MatchString(d) {
		return "anon"
	}
	return d
}

// clientIP prefers the first X-Forwarded-For hop, else the peer address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Wire shapes.
type recipeJSON struct {
	Kind     string            `json:"kind"`
	Cwd      string            `json:"cwd"`
	Install  []string          `json:"install"`
	Start    string            `json:"start"`
	Port     int               `json:"port"`
	Env      map[string]string `json:"env"`
	Detector string            `json:"detector"`
}

type buildJSON struct {
	ID      string      `json:"id"`
	Owner   string      `json:"owner"`
	Repo    string      `json:"repo"`
	SHA     string      `json:"sha"`
	Status  string      `json:"status"`
	Kind    string      `json:"kind,omitempty"`
	Recipe  *recipeJSON `json:"recipe,omitempty"`
	Error   string      `json:"error,omitempty"`
	BuiltAt *time.Time  `json:"built_at,omitempty"`
	Visits  int         `json:"visits"`
}

type sessionJSON struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	Build         buildJSON  `json:"build"`
	PreviewURL    string     `json:"preview_url,omitempty"`
	TerminalReady bool       `json:"terminal_ready"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Extended      bool       `json:"extended"`
	EndedReason   string     `json:"ended_reason,omitempty"`
}

func toBuild(b store.Build) buildJSON {
	out := buildJSON{ID: b.ID, Owner: b.Owner, Repo: b.Repo, SHA: b.SHA, Status: b.Status, Kind: b.Kind, Error: b.Error, BuiltAt: b.BuiltAt, Visits: b.Visits}
	if b.Recipe != nil {
		out.Recipe = toRecipe(*b.Recipe)
	}
	return out
}

func toRecipe(r recipe.Recipe) *recipeJSON {
	env := r.Env
	if env == nil {
		env = map[string]string{}
	}
	install := r.Install
	if install == nil {
		install = []string{}
	}
	return &recipeJSON{Kind: r.Kind, Cwd: r.Cwd, Install: install, Start: r.Start, Port: r.Port, Env: env, Detector: r.Detector}
}

func toSession(s store.Session, b store.Build) sessionJSON {
	return sessionJSON{ID: s.ID, Status: s.Status, Build: toBuild(b), PreviewURL: s.PreviewURL, TerminalReady: s.TerminalReady, ExpiresAt: s.ExpiresAt, Extended: s.Extended, EndedReason: s.EndedReason}
}

// handleCreate starts a session for a repository reference.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo string `json:"repo"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || json.Unmarshal(raw, &body) != nil || strings.TrimSpace(body.Repo) == "" {
		s.writeError(w, repo.ErrInvalidRef)
		return
	}
	sess, bld, err := s.Sessions.Create(r.Context(), deviceID(r), clientIP(r), body.Repo)
	if err != nil {
		s.metrics.sessions.WithLabelValues("refused").Inc()
		s.writeError(w, err)
		return
	}
	s.metrics.sessions.WithLabelValues("created").Inc()
	writeJSON(w, http.StatusCreated, toSession(sess, bld))
}

// handleGet returns a session's current state.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	sess, bld, err := s.Sessions.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSession(sess, bld))
}

// handleExtend grants the one allowed extension.
func (s *Server) handleExtend(w http.ResponseWriter, r *http.Request) {
	sess, err := s.Sessions.Extend(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.metrics.extends.Inc()
	writeJSON(w, http.StatusOK, map[string]any{"expires_at": sess.ExpiresAt, "extended": true})
}

// handleBuild returns the latest build for a repo ("how we ran it").
func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	b, err := s.Store.LatestBuild(r.Context(), r.PathValue("owner"), r.PathValue("repo"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toBuild(b))
}

// handleEvents streams a session as server-sent events. Every event carries
// a JSON body; the history is replayed on each connect so reconnects are safe.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Sessions.Subscribe(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, errors.New("streaming unsupported"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Heartbeats keep proxies from closing an idle stream during a long install.
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			writeSSE(w, ev)
			flusher.Flush()
		case <-beat.C:
			io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// writeSSE encodes one event in the frontend's contract (snake_case keys).
func writeSSE(w io.Writer, ev session.Event) {
	var data any
	switch d := ev.Data.(type) {
	case session.LogData:
		data = map[string]any{"ts": d.TS, "phase": d.Phase, "line": d.Line}
	case session.PhaseData:
		data = map[string]any{"build_status": d.BuildStatus, "session_status": d.SessionStatus, "message": d.Message}
	case session.PreviewData:
		data = map[string]any{"url": d.URL, "port": d.Port}
	case session.TerminalData:
		data = map[string]any{"ready": d.Ready}
	case session.TTLData:
		data = map[string]any{"expires_at": d.ExpiresAt, "extended": d.Extended}
	case session.EndedData:
		data = map[string]any{"reason": d.Reason}
	case session.ErrorData:
		data = map[string]any{"code": d.Code, "message": d.Message}
	default:
		data = d
	}
	b, _ := json.Marshal(data)
	io.WriteString(w, "event: "+ev.Name+"\ndata: "+string(b)+"\n\n")
}

// logRequests emits one line per API request.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/v1/") && !strings.HasSuffix(r.URL.Path, "/events") {
			s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

// statusWriter captures the status for logging while keeping the underlying
// writer's Flush and Hijack available to SSE and websockets.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach Hijack for the websocket upgrade.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// spaHandler serves the built app, falling back to index.html for client
// routes; hashed assets are cached for a year, the shell never.
func spaHandler(static fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		f, err := static.Open(p)
		if err != nil {
			p = "index.html"
			if f, err = static.Open(p); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		defer f.Close()
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if ct := mime.TypeByExtension(path.Ext(p)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		io.Copy(w, f)
	})
}
