package atp

// Health reports the state of the platform and of the services it fronts. It is
// the one implementation of "is this deployment wired correctly?" that every atp
// application can call instead of repeating the same probes.
//
// Two levels are offered deliberately:
//
//   - Health is a cheap, read-only answer: atp's own surfaces plus whether each
//     service URL is configured. It is what a status page can poll as often as
//     it likes.
//   - Check additionally exercises each service with a real operation (a pod
//     write/read/delete, a song silo listing, a shepherd issue/verify, a vault
//     set/get/delete, a registry listing). It answers "is it actually working?",
//     not just "is it configured?", and leaves no probe data behind.
//
// Nothing here imposes authentication or changes the access model: a host
// chooses whether to expose the report.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Probe constants name the disposable data Check writes. They are deliberately
// namespaced so an operator can see and delete anything a crashed probe left.
const (
	ProbeTable  = "__atp_health"
	ProbeRecord = "probe"
	ProbeApp    = "__atp_health"
	ProbeSecret = "probe"
)

// ComponentStatus is one module's contribution to a HealthReport.
type ComponentStatus struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HealthReport is the shape of Health and Check.
type HealthReport struct {
	Status     string            `json:"status"` // healthy | degraded
	Version    string            `json:"version"`
	Timestamp  time.Time         `json:"timestamp"`
	Components []ComponentStatus `json:"components"`
	Failures   []string          `json:"failures,omitempty"`
}

// OK reports whether every component passed.
func (r HealthReport) OK() bool { return r.Status == "healthy" }

func (r *HealthReport) add(name string, ok bool, detail, errMsg string) {
	c := ComponentStatus{Name: name, OK: ok, Detail: detail, Error: errMsg}
	r.Components = append(r.Components, c)
	if !ok {
		if errMsg == "" {
			errMsg = "not available"
		}
		r.Failures = append(r.Failures, name+": "+errMsg)
	}
}

func (r *HealthReport) finish() HealthReport {
	r.Status = "healthy"
	if len(r.Failures) > 0 {
		r.Status = "degraded"
	}
	return *r
}

// Health reports which platform surfaces are configured without touching them.
func (s *Server) Health() HealthReport {
	r := HealthReport{Version: Version, Timestamp: time.Now().UTC()}
	if s == nil {
		r.add("atp", false, "", "server is nil")
		return r.finish()
	}
	r.add("atp", true, Version, "")
	r.add("pod", s.opts.PodURL != "", s.opts.PodURL, "")
	r.add("song", s.opts.SongURL != "", s.opts.SongURL, "")
	r.add("shepherd", s.opts.ShepherdURL != "", s.opts.ShepherdURL, "")
	r.add("secrets", s.vault != nil, "vault mounted", condErr(s.vault == nil, "secrets vault is not mounted"))
	r.add("registry", s.registry != nil, "function registry mounted", condErr(s.registry == nil, "function registry is not mounted"))
	return r.finish()
}

// Check exercises every service with a real, self-cleaning operation.
func (s *Server) Check() HealthReport {
	r := HealthReport{Version: Version, Timestamp: time.Now().UTC()}
	if s == nil {
		r.add("atp", false, "", "server is nil")
		return r.finish()
	}
	r.add("atp", true, Version, "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// pod: write, read back, delete over HTTP.
	base := s.opts.PodURL
	if _, err := s.doJSON(ctx, http.MethodPut, base+"/table/"+ProbeTable, map[string]string{"id": ProbeRecord, "probe": "ok"}); err != nil {
		r.add("pod", false, "", "write: "+err.Error())
	} else if _, err := s.doJSON(ctx, http.MethodGet, base+"/record/"+ProbeTable+"/"+ProbeRecord, nil); err != nil {
		r.add("pod", false, "", "read: "+err.Error())
	} else {
		_, derr := s.doJSON(ctx, http.MethodDelete, base+"/record/"+ProbeTable+"/"+ProbeRecord, nil)
		r.add("pod", derr == nil, "write/read/delete", errString(derr))
	}

	// song: list the silos through its status endpoint.
	if _, err := s.doJSON(ctx, http.MethodGet, s.opts.SongURL+"/_song/silos", nil); err != nil {
		r.add("song", false, "", err.Error())
	} else {
		r.add("song", true, "silo listing readable", "")
	}

	// shepherd: issue and verify a capability through its own API.
	if raw, err := s.issueProbeToken(ctx); err != nil {
		r.add("shepherd", false, "", "issue: "+err.Error())
	} else if _, err := s.doJSON(ctx, http.MethodPost, s.opts.ShepherdURL+"/api/token/verify", map[string]any{"token": raw}); err != nil {
		r.add("shepherd", false, "", "verify: "+err.Error())
	} else {
		r.add("shepherd", true, "issue/verify", "")
	}

	// secrets: set, read back and delete a disposable entry.
	if s.vault == nil {
		r.add("secrets", false, "", "vault is not mounted")
	} else {
		err := s.vault.Set(ProbeApp, ProbeSecret, "ok", "platform health probe")
		if err != nil {
			r.add("secrets", false, "", "write: "+err.Error())
		} else if v, ok := s.vault.Get(ProbeApp, ProbeSecret); !ok || v != "ok" {
			r.add("secrets", false, "", "read did not return the written value")
		} else {
			s.vault.Delete(ProbeApp, ProbeSecret)
			r.add("secrets", true, "write/read/delete", "")
		}
	}

	// registry: list the registered routes.
	if s.registry == nil {
		r.add("registry", false, "", "function registry is not mounted")
	} else {
		r.add("registry", true, fmt.Sprintf("%d route(s)", len(s.registry.Routes())), "")
	}

	return r.finish()
}

// issueProbeToken asks shepherd for a one-minute capability.
func (s *Server) issueProbeToken(ctx context.Context) (string, error) {
	body, err := s.doJSON(ctx, http.MethodPost, s.opts.ShepherdURL+"/api/keys", map[string]any{
		"subject": "atp-health",
		"ttl":     "60s",
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if payload.Token == "" {
		return "", fmt.Errorf("shepherd returned no token")
	}
	return payload.Token, nil
}

// doJSON performs one JSON request against a service, attaching shepherd's
// admin token when the target is shepherd.
func (s *Server) doJSON(ctx context.Context, method, url string, body any) ([]byte, error) {
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.opts.ShepherdAdmin != "" && s.isShepherdURL(url) {
		req.Header.Set("X-Shepherd-Admin", s.opts.ShepherdAdmin)
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return raw, nil
}

func (s *Server) isShepherdURL(url string) bool {
	return s.opts.ShepherdURL != "" && strings.HasPrefix(url, s.opts.ShepherdURL)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func condErr(cond bool, msg string) string {
	if cond {
		return msg
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
