package atp

// Health reports the state of the platform and of the modules it mounts. It is
// the one implementation of "is this deployment wired correctly?" that every
// atp application can call instead of repeating the same nil checks and probes.
//
// Two levels are offered deliberately:
//
//   - Health is a cheap, read-only answer: which module accessors are mounted.
//     It is what a status page can poll as often as it likes.
//   - Check additionally exercises each module with a real operation (a pod
//     write/read/delete, a shepherd issue/verify, a vault set/get/delete, a
//     registry listing). It answers "is it actually working?", not just
//     "is it configured?", and leaves no probe data behind.
//
// Nothing here imposes authentication or changes the access model: a host
// chooses whether to expose the report.

import (
	"fmt"
	"time"

	"azzurrotech/shepherd/token"
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

// Health reports which platform surfaces are mounted without touching them.
func (s *Server) Health() HealthReport {
	r := HealthReport{Version: Version, Timestamp: time.Now().UTC()}
	if s == nil {
		r.add("atp", false, "", "server is nil")
		return r.finish()
	}
	r.add("atp", true, Version, "")
	r.add("pod", s.podStore != nil, "store mounted", condErr(s.podStore == nil, "store is not mounted"))
	r.add("song", s.song != nil && s.song.Store() != nil, "static host mounted", condErr(s.song == nil, "host is not mounted"))
	r.add("shepherd", s.shepherd != nil && s.shepherd.Manager() != nil, "token manager mounted", condErr(s.shepherd == nil, "token manager is not mounted"))
	r.add("secrets", s.vault != nil, "vault mounted", condErr(s.vault == nil, "secrets vault is not mounted"))
	r.add("registry", s.registry != nil, "function registry mounted", condErr(s.registry == nil, "function registry is not mounted"))
	return r.finish()
}

// Check exercises every module with a real, self-cleaning operation.
func (s *Server) Check() HealthReport {
	r := HealthReport{Version: Version, Timestamp: time.Now().UTC()}
	if s == nil {
		r.add("atp", false, "", "server is nil")
		return r.finish()
	}
	r.add("atp", true, Version, "")

	// pod: write, read back, delete.
	if s.podStore == nil {
		r.add("pod", false, "", "store is not mounted")
	} else {
		_, _, werr := s.podStore.Upsert(ProbeTable, ProbeRecord, map[string]string{"probe": "ok"})
		if werr != nil {
			r.add("pod", false, "", "write: "+werr.Error())
		} else if got, rerr := s.podStore.Get(ProbeTable, ProbeRecord); rerr != nil {
			r.add("pod", false, "", "read: "+rerr.Error())
		} else {
			_ = s.podStore.Delete(ProbeTable, ProbeRecord)
			r.add("pod", got != nil, "write/read/delete", "")
		}
	}

	// song: list the silos through the real store.
	if s.song == nil || s.song.Store() == nil {
		r.add("song", false, "", "static host is not mounted")
	} else if _, err := s.song.Store().ListSilos(); err != nil {
		r.add("song", false, "", err.Error())
	} else {
		r.add("song", true, "static store readable", "")
	}

	// shepherd: issue and verify a capability through its own manager.
	if s.shepherd == nil || s.shepherd.Manager() == nil {
		r.add("shepherd", false, "", "token manager is not mounted")
	} else {
		m := s.shepherd.Manager()
		raw, _, err := m.Issue(token.IssueOptions{Subject: "atp-health", TTL: time.Minute})
		if err != nil {
			r.add("shepherd", false, "", "issue: "+err.Error())
		} else if _, err := m.Verify(raw); err != nil {
			r.add("shepherd", false, "", "verify: "+err.Error())
		} else {
			r.add("shepherd", true, "issue/verify", "")
		}
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

func condErr(cond bool, msg string) string {
	if cond {
		return msg
	}
	return ""
}
