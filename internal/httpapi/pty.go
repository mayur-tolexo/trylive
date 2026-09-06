package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/coder/websocket"

	"github.com/mayur-tolexo/trylive/internal/sandbox"
)

// handlePTY upgrades the browser connection and relays frames to the
// sandbox terminal: binary frames are terminal bytes in both directions,
// text frames are JSON control messages passed through unchanged.
func (s *Server) handlePTY(w http.ResponseWriter, r *http.Request) {
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	pty, err := s.Sessions.PTY(r.Context(), r.PathValue("id"), cols, rows)
	if err != nil {
		s.writeError(w, err)
		return
	}
	// The origin check is skipped: the session id in the path is the
	// capability, and the terminal runs in a throwaway sandbox.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		pty.Close()
		return
	}
	conn.SetReadLimit(1 << 20)
	s.metrics.ptys.Inc()
	relay(r.Context(), &wsPeer{conn}, pty)
}

// wsPeer adapts the browser websocket to the same shape as the sandbox PTY.
type wsPeer struct{ c *websocket.Conn }

func (p *wsPeer) Read(ctx context.Context) ([]byte, bool, error) {
	typ, data, err := p.c.Read(ctx)
	return data, typ == websocket.MessageBinary, err
}

func (p *wsPeer) Write(ctx context.Context, data []byte, binary bool) error {
	typ := websocket.MessageText
	if binary {
		typ = websocket.MessageBinary
	}
	return p.c.Write(ctx, typ, data)
}

func (p *wsPeer) Close() error { return p.c.Close(websocket.StatusNormalClosure, "") }

// relay copies frames both ways until either side closes or ctx ends.
func relay(ctx context.Context, browser, pty sandbox.PTYConn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer browser.Close()
	defer pty.Close()
	errc := make(chan error, 2)
	pump := func(from, to sandbox.PTYConn) {
		for {
			data, binary, err := from.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			if err := to.Write(ctx, data, binary); err != nil {
				errc <- err
				return
			}
		}
	}
	go pump(browser, pty)
	go pump(pty, browser)
	// The first side to fail or close ends both pumps via the deferred cancel.
	<-errc
}
