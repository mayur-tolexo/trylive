package builder

import (
	"sort"
	"strconv"
	"strings"
)

// reservedPorts are never treated as the app: sandboxd, ssh, and livereload,
// which many dev servers open before the page server itself.
var reservedPorts = map[int]bool{44772: true, 22: true, 35729: true}

// ParseListening extracts listening TCP ports from `ss -ltnH` output, falling
// back to /proc/net/tcp format (hex address:port with state 0A) when ss is
// missing. Reserved ports are dropped; the result is sorted and unique.
func ParseListening(out string) []int {
	seen := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// /proc/net/tcp: "sl local_address rem_address st ..." with st 0A = LISTEN.
		if len(fields) >= 4 && strings.HasSuffix(fields[0], ":") && len(fields[3]) == 2 {
			if fields[3] != "0A" {
				continue
			}
			if p := hexPort(fields[1]); p > 0 {
				seen[p] = true
			}
			continue
		}
		// ss: "LISTEN 0 511 0.0.0.0:3000 0.0.0.0:*" (or with -H, no header; state may be omitted with -l).
		for _, f := range fields {
			if i := strings.LastIndex(f, ":"); i > 0 && i < len(f)-1 && !strings.HasSuffix(f, ":*") {
				if p, err := strconv.Atoi(f[i+1:]); err == nil && p > 0 && p < 65536 {
					seen[p] = true
					break
				}
			}
		}
	}
	var ports []int
	for p := range seen {
		if !reservedPorts[p] {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	return ports
}

// hexPort decodes the port of a /proc/net/tcp local_address like 0100007F:0BB8.
func hexPort(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	p, err := strconv.ParseInt(addr[i+1:], 16, 32)
	if err != nil {
		return 0
	}
	return int(p)
}

// ChoosePort picks the app port: the recipe's hint if it is listening, else
// the lowest listening port. Zero means nothing usable is listening.
func ChoosePort(hint int, listening []int) int {
	for _, p := range listening {
		if p == hint {
			return p
		}
	}
	if len(listening) > 0 {
		return listening[0]
	}
	return 0
}
