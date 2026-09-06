package httpapi

import (
	"fmt"
	"net/http"
	"strings"
)

// badgeSVG is the README badge: a play mark and "Try it live" in the accent
// colour, sized like the common shields so it sits well next to them.
const badgeSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="94" height="20" role="img" aria-label="Try it live">
<title>Try it live</title>
<linearGradient id="s" x2="0" y2="100%%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>
<clipPath id="r"><rect width="94" height="20" rx="3" fill="#fff"/></clipPath>
<g clip-path="url(#r)"><rect width="22" height="20" fill="#0f1115"/><rect x="22" width="72" height="20" fill="#3ddc84"/><rect width="94" height="20" fill="url(#s)"/></g>
<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" text-rendering="geometricPrecision" font-size="110">
<path d="M8 5 L16 10 L8 15 Z" fill="#3ddc84"/>
<text aria-hidden="true" x="580" y="150" fill="#010101" fill-opacity=".3" transform="scale(.1)" textLength="620">Try it live</text>
<text x="580" y="140" transform="scale(.1)" fill="#0f1115" textLength="620">Try it live</text>
</g>
</svg>`

// handleBadge serves the badge. It is identical for every repo today; the
// owner/repo path keeps per-repo variants (status, visit counts) possible
// without changing the URLs already embedded in READMEs.
func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	// The mux cannot match "{repo}.svg", so the suffix is checked here.
	if r.PathValue("owner") == "" || !strings.HasSuffix(r.PathValue("file"), ".svg") || len(r.PathValue("file")) == 4 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprint(w, badgeSVG)
}
