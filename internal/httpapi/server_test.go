package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"github.com/mayur-tolexo/trylive/internal/builder"
	"github.com/mayur-tolexo/trylive/internal/recipe"
	"github.com/mayur-tolexo/trylive/internal/repo"
	"github.com/mayur-tolexo/trylive/internal/sandbox"
	"github.com/mayur-tolexo/trylive/internal/session"
	"github.com/mayur-tolexo/trylive/internal/store"
)

type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, ref repo.Ref) (repo.Info, error) {
	if ref.Name == "missing" {
		return repo.Info{}, repo.ErrNotFound
	}
	return repo.Info{Ref: ref, DefaultBranch: "main", SHA: "abcdef1234567", CloneURL: "https://github.com/" + ref.Slug() + ".git"}, nil
}

type env struct {
	srv   *httptest.Server
	fake  *sandbox.Fake
	store *store.Memory
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := sandbox.NewFake()
	manifest, _ := json.Marshal(recipe.Manifest{PackageJSON: &recipe.PackageJSON{Scripts: map[string]string{"dev": "vite"}, DevDependencies: map[string]string{"vite": "5"}}})
	f.ExecScript["python3 inspect.py"] = sandbox.ExecResult{Stdout: string(manifest)}
	f.ExecScript["sh -c ss"] = sandbox.ExecResult{Stdout: "LISTEN 0 511 0.0.0.0:5173 0.0.0.0:*\n"}
	st := store.NewMemory()
	b := &builder.Builder{Sandbox: f, Store: st}
	m := &session.Manager{Store: st, Sandbox: f, Builder: b, Resolver: fakeResolver{}, Poll: 50 * time.Millisecond,
		Limits: session.Limits{PerDevice: 1, PerIP: 5, Global: 10, TTL: 15 * time.Minute, MaxTTL: 30 * time.Minute}}
	s := &Server{Sessions: m, Store: st, Static: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/a.js": {Data: []byte("js")}}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{srv: srv, fake: f, store: st}
}

// call performs a JSON request with a device id.
func (e *env) call(t *testing.T, method, path string, body any, dev string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, &buf)
	req.Header.Set("X-Device-Id", dev)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const dev = "11111111-2222-3333-4444-555555555555"

// readSSE collects events from the stream until want appears or the deadline.
func readSSE(t *testing.T, url, want string, d time.Duration) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	var events []string
	sc := bufio.NewScanner(resp.Body)
	var name string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			events = append(events, name+" "+strings.TrimPrefix(line, "data: "))
			if name == want {
				return events
			}
		}
	}
	return events
}

func TestCreateStreamLiveExtendAndPTY(t *testing.T) {
	e := newEnv(t)
	code, out := e.call(t, http.MethodPost, "/v1/sessions", map[string]string{"repo": "https://github.com/octo/app"}, dev)
	if code != 201 || out["status"] != "building" || out["build"].(map[string]any)["owner"] != "octo" {
		t.Fatalf("create = %d %v", code, out)
	}
	id := out["id"].(string)

	events := readSSE(t, e.srv.URL+"/v1/sessions/"+id+"/events", "ttl", 5*time.Second)
	joined := strings.Join(events, "\n")
	for _, want := range []string{`log {"line":"git clone`, `phase {"build_status":"ready"`, `preview {"port":5173,"url":"https://5173-`, `terminal {"ready":true}`, `ttl {"expires_at"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("stream missing %q:\n%s", want, joined)
		}
	}

	code, out = e.call(t, http.MethodGet, "/v1/sessions/"+id, nil, dev)
	if code != 200 || out["status"] != "live" || out["preview_url"] == "" || out["terminal_ready"] != true {
		t.Fatalf("get = %d %v", code, out)
	}
	rec := out["build"].(map[string]any)["recipe"].(map[string]any)
	if rec["detector"] != "package.json" || rec["port"].(float64) != 5173 {
		t.Errorf("recipe = %v", rec)
	}

	// Reconnecting replays the history.
	again := readSSE(t, e.srv.URL+"/v1/sessions/"+id+"/events", "ttl", 3*time.Second)
	if len(again) < 5 {
		t.Errorf("replay too short: %d events", len(again))
	}

	code, out = e.call(t, http.MethodPost, "/v1/sessions/"+id+"/extend", nil, dev)
	if code != 200 || out["extended"] != true {
		t.Fatalf("extend = %d %v", code, out)
	}
	if code, out = e.call(t, http.MethodPost, "/v1/sessions/"+id+"/extend", nil, dev); code != 400 || out["error"].(map[string]any)["code"] != "invalid" {
		t.Errorf("second extend = %d %v", code, out)
	}

	// Terminal relay: bytes echo back through the fake PTY; control frames pass as text.
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/v1/sessions/" + id + "/pty?cols=80&rows=24"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Write(ctx, websocket.MessageBinary, []byte("ls\n"))
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(data) != "echo:ls\n" {
		t.Fatalf("binary relay = %v %q %v", typ, data, err)
	}
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":100,"rows":30}`))
	typ, data, err = conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || string(data) != `ctl:{"type":"resize","cols":100,"rows":30}` {
		t.Fatalf("control relay = %v %q %v", typ, data, err)
	}
	conn.Close(websocket.StatusNormalClosure, "")

	// Latest build endpoint.
	code, out = e.call(t, http.MethodGet, "/v1/builds/gh/octo/app", nil, dev)
	if code != 200 || out["status"] != "ready" || out["visits"].(float64) != 1 {
		t.Errorf("build = %d %v", code, out)
	}
	if code, _ = e.call(t, http.MethodGet, "/v1/builds/gh/octo/never", nil, dev); code != 404 {
		t.Errorf("unknown build = %d", code)
	}

	// Platform reclaims the sandbox; the stream ends.
	sess, _ := e.store.GetSession(context.Background(), id)
	e.fake.Delete(context.Background(), sess.SandboxID)
	ended := readSSE(t, e.srv.URL+"/v1/sessions/"+id+"/events", "ended", 3*time.Second)
	if !strings.Contains(strings.Join(ended, "\n"), `ended {"reason":"session expired"}`) {
		t.Errorf("no ended event: %v", ended)
	}
	if code, out = e.call(t, http.MethodPost, "/v1/sessions/"+id+"/extend", nil, dev); code != 409 {
		t.Errorf("extend after end = %d %v", code, out)
	}
}

func TestErrorsAndLimits(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		body any
		dev  string
		code int
		ec   string
	}{
		{map[string]string{"repo": "nonsense"}, dev, 400, "invalid"},
		{"not json", dev, 400, "invalid"},
		{map[string]string{"repo": "octo/missing"}, dev, 404, "not_found"},
	}
	for _, c := range cases {
		code, out := e.call(t, http.MethodPost, "/v1/sessions", c.body, c.dev)
		if code != c.code || out["error"].(map[string]any)["code"] != c.ec {
			t.Errorf("%v: %d %v", c.body, code, out)
		}
	}
	// A second session from the same device replaces the first.
	code, first := e.call(t, http.MethodPost, "/v1/sessions", map[string]string{"repo": "octo/app"}, dev)
	if code != 201 {
		t.Fatalf("first create %d", code)
	}
	if code, _ := e.call(t, http.MethodPost, "/v1/sessions", map[string]string{"repo": "octo/app"}, dev); code != 201 {
		t.Errorf("replace = %d", code)
	}
	if code, out := e.call(t, http.MethodGet, "/v1/sessions/"+first["id"].(string), nil, dev); code != 200 || out["status"] != "ended" {
		t.Errorf("replaced session = %d %v", code, out)
	}
	// Other devices on the same IP hit the per-IP cap with a retry hint.
	for i := 0; i < 5; i++ {
		e.call(t, http.MethodPost, "/v1/sessions", map[string]string{"repo": "octo/app"}, "device-number-"+string(rune('a'+i)))
	}
	code, out := e.call(t, http.MethodPost, "/v1/sessions", map[string]string{"repo": "octo/app"}, "device-number-zz")
	if code != 429 || out["error"].(map[string]any)["code"] != "rate_limited" || out["error"].(map[string]any)["retry_after_seconds"] == nil {
		t.Errorf("limit = %d %v", code, out)
	}
	if code, _ := e.call(t, http.MethodGet, "/v1/sessions/nope", nil, dev); code != 404 {
		t.Errorf("missing session = %d", code)
	}
	if code, _ := e.call(t, http.MethodGet, "/v1/sessions/nope/events", nil, dev); code != 404 {
		t.Errorf("missing events = %d", code)
	}
}

func TestBadgeAndStatic(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.srv.URL + "/badge/gh/octo/app.svg")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := readAll(resp)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(body, "Try it live") {
		t.Errorf("badge = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, p := range []string{"/", "/gh/octo/app", "/anything/else"} {
		resp, _ := http.Get(e.srv.URL + p)
		body, _ := readAll(resp)
		if resp.StatusCode != 200 || !strings.Contains(body, "app") {
			t.Errorf("%s: %d %q", p, resp.StatusCode, body)
		}
	}
	resp, _ = http.Get(e.srv.URL + "/assets/a.js")
	if !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset cache = %q", resp.Header.Get("Cache-Control"))
	}
	resp, _ = http.Get(e.srv.URL + "/metrics")
	body, _ = readAll(resp)
	if !strings.Contains(body, "trylive_extends_total") {
		t.Errorf("metrics missing counters")
	}
}

func readAll(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
	}
	return b.String(), sc.Err()
}
