// control.go — atp's instance-control API and the guard that protects it.
//
// The control surface is deliberately small:
//
//	GET  /api/instances            every instance, its state and health
//	GET  /api/instances/{name}     one instance (?log=N adds the log tail)
//	POST /api/instances/{name}     {"action":"start"|"stop"|"restart"}
//
// Starting and stopping processes is a privileged operation. When
// ATP_ADMIN_TOKEN is set, every control call must carry it in X-ATP-Admin.
// When it is not set, control is restricted to loopback clients, which keeps a
// laptop stack usable without turning a reachable atp into a remote shell.
package atp

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// --- handlers ---

func (s *Server) serveInstances(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeControl(w, r) {
		return
	}
	if s.instances == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"supervised": false,
			"instances":  []InstanceStatus{},
			"detail":     "no instance manifest configured",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"supervised": s.supervise,
		"log_dir":    s.instances.LogDir(),
		"instances":  s.instances.Status(r.Context()),
	})
}

func (s *Server) serveInstance(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeControl(w, r) {
		return
	}
	if s.instances == nil {
		writeErr(w, http.StatusNotFound, "no instance manifest configured")
		return
	}
	name := PathParam(r, "name")
	statuses := s.instances.Status(r.Context())
	for _, st := range statuses {
		if st.Name != name {
			continue
		}
		if raw := r.URL.Query().Get("log"); raw != "" {
			n, _ := strconv.Atoi(raw)
			lines, err := s.instances.Tail(name, n)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"instance": st, "log": nonNil(lines)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"instance": st})
		return
	}
	writeErr(w, http.StatusNotFound, "unknown instance "+name)
}

func (s *Server) serveInstanceAction(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeControl(w, r) {
		return
	}
	if s.instances == nil {
		writeErr(w, http.StatusConflict, "no instance manifest configured")
		return
	}
	name := PathParam(r, "name")
	var body struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &body) {
		return
	}

	var (
		st  InstanceStatus
		err error
	)
	switch strings.ToLower(strings.TrimSpace(body.Action)) {
	case "start", "":
		st, err = s.instances.Start(r.Context(), name)
	case "stop":
		st, err = s.instances.Stop(r.Context(), name)
	case "restart":
		st, err = s.instances.Restart(r.Context(), name)
	default:
		writeErr(w, http.StatusBadRequest, "action must be start, stop or restart")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"instance": st, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instance": st})
}

// --- authorization ---

// authorizeControl enforces the control-plane policy: a shared admin token when
// one is configured, loopback-only access otherwise.
func (s *Server) authorizeControl(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.AdminToken != "" {
		got := r.Header.Get("X-ATP-Admin")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.AdminToken)) == 1 {
			return true
		}
		writeErr(w, http.StatusUnauthorized, "admin token required (X-ATP-Admin)")
		return false
	}
	if isLoopbackRequest(r) {
		return true
	}
	writeErr(w, http.StatusForbidden, "instance control is loopback-only; set ATP_ADMIN_TOKEN to allow remote control")
	return false
}

// isLoopbackRequest reports whether the TCP peer is the local machine. It does
// not trust forwarding headers on purpose: a proxy would otherwise turn every
// remote client into a local one.
func isLoopbackRequest(r *http.Request) bool {
	// An empty RemoteAddr means the handler was called in-process (a test or an
	// embedder), which is at least as trusted as a loopback socket.
	if r.RemoteAddr == "" {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// shepherdAdminRoutes are the shepherd paths that require admin authority. The
// public verification, provider and tracking routes are not in the set.
var shepherdAdminRoutes = map[string]bool{
	"/api/keys":                true,
	"/api/keys/block":          true,
	"/api/keys/magic":          true,
	"/api/keys/javascript":     true,
	"/api/keys/regenerate":     true,
	"/api/revoke":              true,
	"/api/revoke/block":        true,
	"/api/firewall/rules":      true,
	"/api/firewall/rules/{id}": true,
	"/api/ratelimit/status":    true,
	"/api/ratelimit/reset":     true,
	"/api/upstreams":           true,
	"/api/upstreams/{prefix}":  true,
}

// shepherdRouteNeedsAdmin reports whether a registered shepherd route is a
// management endpoint.
func shepherdRouteNeedsAdmin(pattern string) bool {
	return shepherdAdminRoutes[pattern]
}
