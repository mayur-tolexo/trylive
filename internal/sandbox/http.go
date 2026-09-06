package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Config locates the platform and the project every sandbox is created in.
type Config struct {
	APIBase   string // e.g. https://api.ai.neevcloud.com/agent
	APIKey    string
	OrgID     string
	ProjectID string
	Region    string // optional
}

// HTTP is the real Client.
type HTTP struct {
	cfg  Config
	http *http.Client
}

// NewHTTP validates cfg and returns a client. The HTTP client has no global
// timeout because exec and log streams run for minutes; every call carries a
// context deadline instead.
func NewHTTP(cfg Config) (*HTTP, error) {
	if cfg.APIBase == "" || cfg.APIKey == "" || cfg.OrgID == "" || cfg.ProjectID == "" {
		return nil, fmt.Errorf("sandbox: api base, api key, org id and project id are required")
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	return &HTTP{cfg: cfg, http: &http.Client{}}, nil
}

// createRequest mirrors the platform's create body.
type createRequest struct {
	Name      string     `json:"name,omitempty"`
	Region    string     `json:"region,omitempty"`
	Restore   string     `json:"restore,omitempty"`
	Resources *Resources `json:"resources,omitempty"`
	Egress    *Egress    `json:"egress,omitempty"`
	Lifecycle *Lifecycle `json:"lifecycle,omitempty"`
}

func (c *HTTP) Create(ctx context.Context, spec CreateSpec) (Sandbox, error) {
	body := createRequest{Name: spec.Name, Region: c.cfg.Region, Restore: spec.Restore, Resources: spec.Resources, Egress: &spec.Egress}
	if spec.Lifecycle != (Lifecycle{}) {
		lc := spec.Lifecycle
		body.Lifecycle = &lc
	}
	var sb Sandbox
	status, err := c.control(ctx, http.MethodPost, c.path("sandboxes"), body, &sb)
	if err != nil {
		return Sandbox{}, err
	}
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable:
		return Sandbox{}, ErrBusy
	case status != http.StatusCreated && status != http.StatusOK:
		return Sandbox{}, fmt.Errorf("create sandbox: status %d", status)
	}
	return sb, nil
}

func (c *HTTP) Get(ctx context.Context, id string) (Sandbox, error) {
	var sb Sandbox
	status, err := c.control(ctx, http.MethodGet, c.path("sandboxes", id), nil, &sb)
	if err != nil {
		return Sandbox{}, err
	}
	if status == http.StatusNotFound {
		return Sandbox{}, ErrNotFound
	}
	if status != http.StatusOK {
		return Sandbox{}, fmt.Errorf("get sandbox %s: status %d", id, status)
	}
	return sb, nil
}

// WaitReady polls every second until Ready with a connect URL, giving up on
// a terminal phase or the timeout.
func (c *HTTP) WaitReady(ctx context.Context, id string, timeout time.Duration) (Sandbox, error) {
	deadline := time.Now().Add(timeout)
	for {
		sb, err := c.Get(ctx, id)
		if err != nil {
			return Sandbox{}, err
		}
		if sb.Phase == PhaseReady && sb.ConnectURL != "" {
			return sb, nil
		}
		if sb.Phase == PhasePaused || sb.Phase == "RestoreFailed" {
			return sb, fmt.Errorf("sandbox %s entered %s while waiting for Ready", id, sb.Phase)
		}
		if time.Now().After(deadline) {
			return sb, fmt.Errorf("sandbox %s not Ready after %s (phase %s)", id, timeout, sb.Phase)
		}
		select {
		case <-ctx.Done():
			return sb, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (c *HTTP) Delete(ctx context.Context, id string) error {
	status, err := c.control(ctx, http.MethodDelete, c.path("sandboxes", id), nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK && status != http.StatusNotFound {
		return fmt.Errorf("delete sandbox %s: status %d", id, status)
	}
	return nil
}

func (c *HTTP) ExposePort(ctx context.Context, id string, port int) (string, error) {
	var out struct {
		Port       int    `json:"port"`
		PreviewURL string `json:"preview_url"`
	}
	status, err := c.control(ctx, http.MethodPost, c.path("sandboxes", id, "ports"), map[string]int{"port": port}, &out)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", fmt.Errorf("expose port %d on %s: status %d", port, id, status)
	}
	return out.PreviewURL, nil
}

func (c *HTTP) Keepalive(ctx context.Context, id string) error {
	status, err := c.control(ctx, http.MethodPost, c.path("sandboxes", id, "keepalive"), nil, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("keepalive %s: status %d", id, status)
	}
	return nil
}

func (c *HTTP) UpdateTimeout(ctx context.Context, id string, lc Lifecycle) error {
	status, err := c.control(ctx, http.MethodPut, c.path("sandboxes", id, "timeout"), lc, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("update timeout %s: status %d", id, status)
	}
	return nil
}

func (c *HTTP) CreateSnapshot(ctx context.Context, id, name string) (Snapshot, error) {
	var snap Snapshot
	body := map[string]string{}
	if name != "" {
		body["name"] = name
	}
	status, err := c.control(ctx, http.MethodPost, c.path("sandboxes", id, "snapshots"), body, &snap)
	if err != nil {
		return Snapshot{}, err
	}
	if status != http.StatusAccepted && status != http.StatusCreated && status != http.StatusOK {
		return Snapshot{}, fmt.Errorf("create snapshot of %s: status %d", id, status)
	}
	return snap, nil
}

func (c *HTTP) GetSnapshot(ctx context.Context, snapshotID string) (Snapshot, error) {
	var snap Snapshot
	status, err := c.control(ctx, http.MethodGet, c.path("snapshots", snapshotID), nil, &snap)
	if err != nil {
		return Snapshot{}, err
	}
	if status == http.StatusNotFound {
		return Snapshot{}, ErrNotFound
	}
	if status != http.StatusOK {
		return Snapshot{}, fmt.Errorf("get snapshot %s: status %d", snapshotID, status)
	}
	return snap, nil
}

// WaitSnapshot polls every two seconds until Ready or Failed.
func (c *HTTP) WaitSnapshot(ctx context.Context, snapshotID string, timeout time.Duration) (Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snap, err := c.GetSnapshot(ctx, snapshotID)
		if err != nil {
			return Snapshot{}, err
		}
		switch snap.Status {
		case SnapshotReady:
			return snap, nil
		case SnapshotFailed:
			return snap, fmt.Errorf("snapshot %s failed: %s", snapshotID, snap.Error)
		}
		if time.Now().After(deadline) {
			return snap, fmt.Errorf("snapshot %s not Ready after %s (status %s)", snapshotID, timeout, snap.Status)
		}
		select {
		case <-ctx.Done():
			return snap, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// execRequest is sandboxd's exec body; processes/start shares the shape
// with "program" in place of "command".
type execRequest struct {
	Command   string   `json:"command,omitempty"`
	Program   string   `json:"program,omitempty"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	Env       []string `json:"env,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
	Stdin     string   `json:"stdin,omitempty"`
}

// streamEvent is one NDJSON frame from exec or processes/logs.
type streamEvent struct {
	Type       string `json:"type"`
	Data       []byte `json:"data,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// Exec runs a command and streams its output to out (when non-nil) while
// collecting it; a non-zero exit is returned in the result, not as an error.
func (c *HTTP) Exec(ctx context.Context, sb Sandbox, spec ExecSpec, out io.Writer) (ExecResult, error) {
	body := execRequest{Command: spec.Command, Args: spec.Args, Cwd: spec.Cwd, Env: spec.Env, TimeoutMS: spec.TimeoutMS, Stdin: spec.Stdin}
	resp, err := c.dataStream(ctx, sb, "/v1/exec", body)
	if err != nil {
		return ExecResult{}, err
	}
	defer resp.Body.Close()
	var res ExecResult
	var stdout, stderr bytes.Buffer
	res.ExitCode = -1
	err = readStream(resp.Body, func(ev streamEvent) {
		switch ev.Type {
		case "stdout":
			stdout.Write(ev.Data)
			if out != nil {
				out.Write(ev.Data)
			}
		case "stderr":
			stderr.Write(ev.Data)
			if out != nil {
				out.Write(ev.Data)
			}
		case "exit":
			if ev.ExitCode != nil {
				res.ExitCode = *ev.ExitCode
			}
		}
	})
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	return res, err
}

func (c *HTTP) WriteFile(ctx context.Context, sb Sandbox, path string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := strings.TrimRight(sb.ConnectURL, "/") + "/v1/files/write?path=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	c.dataHeaders(req)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("write %s: %s", path, readErr(resp))
	}
	return nil
}

func (c *HTTP) StartProcess(ctx context.Context, sb Sandbox, spec ExecSpec) (Process, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body := execRequest{Program: spec.Command, Args: spec.Args, Cwd: spec.Cwd, Env: spec.Env, Stdin: spec.Stdin}
	resp, err := c.dataJSON(ctx, sb, "/v1/processes/start", body)
	if err != nil {
		return Process{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return Process{}, fmt.Errorf("start process: %s", readErr(resp))
	}
	var p Process
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return Process{}, err
	}
	return p, nil
}

// StreamLogs follows the process from the start of retained output.
func (c *HTTP) StreamLogs(ctx context.Context, sb Sandbox, processID string, fn func(LogChunk)) error {
	body := map[string]any{"process_id": processID, "cursor": 0, "follow": true}
	resp, err := c.dataStream(ctx, sb, "/v1/processes/logs", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readStream(resp.Body, func(ev streamEvent) {
		if ev.Type == "stdout" || ev.Type == "stderr" {
			fn(LogChunk{Stream: ev.Type, Data: ev.Data})
		}
	})
}

// DialPTY opens the terminal websocket with API-key auth on the upgrade.
func (c *HTTP) DialPTY(ctx context.Context, sb Sandbox, opts PTYOptions) (PTYConn, error) {
	q := url.Values{}
	if opts.Program != "" {
		q.Set("program", opts.Program)
	}
	for _, a := range opts.Args {
		q.Add("arg", a)
	}
	if opts.Cols > 0 {
		q.Set("cols", strconv.Itoa(opts.Cols))
	}
	if opts.Rows > 0 {
		q.Set("rows", strconv.Itoa(opts.Rows))
	}
	u := strings.TrimRight(sb.ConnectURL, "/") + "/v1/pty?" + q.Encode()
	u = "ws" + strings.TrimPrefix(u, "http")
	hdr := http.Header{}
	hdr.Set("X-Api-Key", c.cfg.APIKey)
	hdr.Set("X-Protocol-Version", "1")
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return nil, fmt.Errorf("dial pty: %w", err)
	}
	// Terminal output can burst; the default 32 KiB limit would drop the socket.
	conn.SetReadLimit(4 << 20)
	return &wsConn{conn: conn}, nil
}

// wsConn adapts a coder/websocket connection to PTYConn.
type wsConn struct{ conn *websocket.Conn }

func (w *wsConn) Read(ctx context.Context) ([]byte, bool, error) {
	typ, data, err := w.conn.Read(ctx)
	return data, typ == websocket.MessageBinary, err
}

func (w *wsConn) Write(ctx context.Context, data []byte, binary bool) error {
	typ := websocket.MessageText
	if binary {
		typ = websocket.MessageBinary
	}
	return w.conn.Write(ctx, typ, data)
}

func (w *wsConn) Close() error { return w.conn.Close(websocket.StatusNormalClosure, "") }

// path builds a project-scoped control-plane URL.
func (c *HTTP) path(parts ...string) string {
	return c.cfg.APIBase + "/api/v1beta1/orgs/" + c.cfg.OrgID + "/projects/" + c.cfg.ProjectID + "/" + strings.Join(parts, "/")
}

// control performs a control-plane call, decoding JSON into out for 2xx.
func (c *HTTP) control(ctx context.Context, method, u string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound &&
		resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
		return resp.StatusCode, fmt.Errorf("%s %s: %s", method, u, readErr(resp))
	}
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, u, err)
		}
	} else {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	}
	return resp.StatusCode, nil
}

// dataJSON posts a JSON body to sandboxd and returns the raw response.
func (c *HTTP) dataJSON(ctx context.Context, sb Sandbox, route string, in any) (*http.Response, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(sb.ConnectURL, "/")+route, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	c.dataHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

// dataStream is dataJSON for NDJSON responses; non-2xx becomes an error.
func (c *HTTP) dataStream(ctx context.Context, sb Sandbox, route string, in any) (*http.Response, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(sb.ConnectURL, "/")+route, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	c.dataHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", route, readErr(resp))
	}
	return resp, nil
}

// readStream decodes NDJSON frames until EOF; an error frame ends the stream
// with an error.
func readStream(r io.Reader, fn func(streamEvent)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("stream frame: %w", err)
		}
		if ev.Type == "error" {
			return fmt.Errorf("stream: %s: %s", ev.ReasonCode, ev.Message)
		}
		fn(ev)
	}
	return sc.Err()
}

// dataHeaders sets sandboxd auth on a data-plane request.
func (c *HTTP) dataHeaders(req *http.Request) {
	req.Header.Set("X-Api-Key", c.cfg.APIKey)
	req.Header.Set("X-Protocol-Version", "1")
}

// readErr summarises an error response body.
func readErr(resp *http.Response) string {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}
