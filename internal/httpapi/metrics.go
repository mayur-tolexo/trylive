package httpapi

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics are the operational signals: session demand and refusals,
// extensions, and terminals opened.
type metrics struct {
	reg      *prometheus.Registry
	sessions *prometheus.CounterVec
	extends  prometheus.Counter
	ptys     prometheus.Counter
}

// newMetrics registers on a private registry so only deliberate signals are
// exposed.
func newMetrics() *metrics {
	m := &metrics{
		reg:      prometheus.NewRegistry(),
		sessions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trylive_sessions_total", Help: "Session requests by outcome."}, []string{"outcome"}),
		extends:  prometheus.NewCounter(prometheus.CounterOpts{Name: "trylive_extends_total", Help: "Session extensions granted."}),
		ptys:     prometheus.NewCounter(prometheus.CounterOpts{Name: "trylive_ptys_total", Help: "Terminal websockets relayed."}),
	}
	m.reg.MustRegister(m.sessions, m.extends, m.ptys)
	return m
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
