// discovery.go — localhost auto-discovery.
//
// A scan answers two questions about every configured port:
//
//  1. Is something listening? A port is healthy when the TCP handshake succeeds
//     or an HTTP request gets any response at all. A 404 still proves occupancy,
//     so it counts; only a refused connection is unhealthy.
//  2. What is listening? Each first-party service publishes a health document
//     that names itself ("atp", "pod", "song", "shepherd", "rapid"), so a scan
//     reports the service and version instead of an anonymous open port.
//
// App registration is deliberately narrow: only a discovered atp instance has
// an /api/apps surface to copy from, so only an atp identity triggers it.
package atp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DefaultPortsToScan lists the ports a default scan probes: the first-party
// service range plus two conventional alternates. It is a var, not a const,
// because a deployment is expected to override it (see AutoDiscoveryConfig.Ports
// and the atp -ports flag / ATP_PORTS environment variable).
var DefaultPortsToScan = []int{8080, 8081, 8082, 8083, 9080, 9090}

// healthPaths are the liveness documents the first-party services expose. atp
// and rapid answer /api/health, pod and shepherd answer /health, song answers
// /_song/health. One scanner tries all three so a single scan covers the family.
var healthPaths = []string{"/api/health", "/health", "/_song/health"}

// defaultProbeTimeout bounds each individual HTTP probe.
const defaultProbeTimeout = 2 * time.Second

// dialTimeout bounds the TCP occupancy check that runs before the HTTP probe.
const dialTimeout = 300 * time.Millisecond

// PortProbeResult is what one port probe learned.
type PortProbeResult struct {
	Port int
	Host string
	// Healthy reports that something is listening on the port: the TCP
	// handshake succeeded or an HTTP request got a response of any status.
	Healthy bool
	// Service is the identity the port advertised in its health document
	// ("atp", "pod", "song", "shepherd", "rapid"). Empty when unidentified.
	Service string
	// Version is the service version from the same document, when present.
	Version string
	// Instances is the list of instance names an atp health document reports.
	Instances []string
	// Error explains why the port is unhealthy.
	Error error
}

// AutoDiscoveryConfig configures the auto-discovery behaviour.
type AutoDiscoveryConfig struct {
	// Host is the interface to probe. Empty means "localhost". Set it to
	// "127.0.0.1" when a service binds IPv4 only and localhost resolves to IPv6
	// first, or to a LAN address to probe another machine.
	Host string
	// Ports to scan. Empty means DefaultPortsToScan.
	Ports []int
	// CheckTimeout bounds each HTTP probe. Zero means defaultProbeTimeout.
	CheckTimeout time.Duration
	// RegisterApps copies the app list from every discovered atp instance into
	// this instance's registry.
	RegisterApps bool
}

// AutoDiscoveryResult is the outcome of a scan.
type AutoDiscoveryResult struct {
	// Probes holds one entry per scanned port, in the order the ports were
	// supplied.
	Probes []PortProbeResult
	// Services maps a discovered service name to the ports it answered on.
	Services map[string][]int
	// TotalApps is how many apps were newly registered from discovered atp
	// instances.
	TotalApps int
	// InstanceNames is the union of instance names the discovered atp instances
	// reported.
	InstanceNames []string
	// Warnings records non-fatal problems, such as an app list that could not
	// be read from an otherwise healthy instance.
	Warnings []string
}

// AutoDiscovery scans the configured ports in parallel, identifies what answers
// on each, and optionally registers apps from discovered atp instances.
//
// It returns an error only when the scan itself cannot run. A port that refuses
// connections is a normal result (Healthy false), not an error.
func (s *Server) AutoDiscovery(ctx context.Context, config AutoDiscoveryConfig) (*AutoDiscoveryResult, error) {
	ports := config.Ports
	if len(ports) == 0 {
		ports = DefaultPortsToScan
	}
	timeout := config.CheckTimeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	host := config.Host
	if host == "" {
		host = "localhost"
	}

	probes := make([]PortProbeResult, len(ports))
	var wg sync.WaitGroup
	for i, port := range ports {
		wg.Add(1)
		go func(i, port int) {
			defer wg.Done()
			probes[i] = s.probePort(ctx, host, port, timeout)
		}(i, port)
	}
	wg.Wait()

	result := &AutoDiscoveryResult{
		Probes:   probes,
		Services: make(map[string][]int),
	}
	seen := make(map[string]bool)
	for _, pr := range probes {
		if !pr.Healthy {
			continue
		}
		if pr.Service != "" {
			result.Services[pr.Service] = append(result.Services[pr.Service], pr.Port)
		}
		for _, name := range pr.Instances {
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			result.InstanceNames = append(result.InstanceNames, name)
		}
		if config.RegisterApps && pr.Service == "atp" && pr.Port != s.ownPort() {
			n, err := s.registerDiscoveredApps(ctx, host, pr.Port, timeout)
			result.TotalApps += n
			if err != nil {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("port %d: %v", pr.Port, err))
			}
		}
	}
	return result, nil
}

// ownPort returns the port this server listens on, or 0 when Addr does not name
// a concrete port. A scan uses it to avoid re-registering its own apps.
func (s *Server) ownPort() int {
	addr := s.Addr()
	if addr == "" {
		return 0
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		if n, err := net.LookupPort("tcp", port); err == nil {
			return n
		}
	}
	return 0
}

// probePort probes one host:port. It never returns an error: a refused
// connection is reported as Healthy false with the reason in Error.
func (s *Server) probePort(ctx context.Context, host string, port int, timeout time.Duration) PortProbeResult {
	res := PortProbeResult{Port: port, Host: host}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	// Occupancy: a completed TCP handshake is proof enough on its own.
	tcpOK := false
	if conn, err := net.DialTimeout("tcp", addr, dialTimeout); err == nil {
		tcpOK = true
		_ = conn.Close()
	}

	// Identity: ask each candidate health path. Any HTTP response, whatever the
	// status, is also proof that the port is occupied.
	client := &http.Client{Timeout: timeout}
	var (
		httpOK   bool
		lastErr  error
		identity healthDoc
	)
	for _, path := range healthPaths {
		doc, responded, err := fetchHealthDoc(ctx, client, host, port, path)
		if responded {
			httpOK = true
		}
		if err != nil && lastErr == nil {
			// Keep the first failure: it is the most representative reason.
			lastErr = err
		}
		if doc.Service != "" || doc.Version != "" {
			identity = doc
			break
		}
	}

	res.Healthy = tcpOK || httpOK
	if !res.Healthy {
		if lastErr == nil {
			lastErr = fmt.Errorf("connection refused")
		}
		res.Error = lastErr
	}
	res.Service = identity.Service
	if identity.looksLikeATP() {
		res.Service = "atp"
	}
	res.Version = identity.Version
	res.Instances = identity.Instances
	return res
}

// healthDoc is the subset of a first-party health document a scan reads.
//
// Components is kept raw because its shape is not consistent across the family:
// atp publishes an array of component objects, rapid publishes an object map.
// Decoding into a slice would fail on rapid's document and lose the identity
// fields after it.
type healthDoc struct {
	Service    string          `json:"service"`
	Version    string          `json:"version"`
	Instances  []string        `json:"instances"`
	Components json.RawMessage `json:"components"`
}

// looksLikeATP reports whether an unidentified document carries atp's signature:
// a components array and a version. It covers atp builds that predate the
// service field.
func (d healthDoc) looksLikeATP() bool {
	if d.Service != "" || d.Version == "" {
		return false
	}
	trimmed := bytes.TrimSpace(d.Components)
	return len(trimmed) > 0 && trimmed[0] == '['
}

// fetchHealthDoc GETs one health path. responded reports whether any HTTP
// response arrived (which proves the port is occupied), independently of
// whether the document parsed.
func fetchHealthDoc(ctx context.Context, client *http.Client, host string, port int, path string) (doc healthDoc, responded bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(port)), path), nil)
	if err != nil {
		return healthDoc{}, false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return healthDoc{}, false, err
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return healthDoc{}, true, nil
	}
	if readErr != nil {
		return healthDoc{}, true, readErr
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return healthDoc{}, true, nil
	}
	return doc, true, nil
}

// registerDiscoveredApps copies the app list from a discovered atp instance into
// this instance's registry and reports how many apps were newly registered.
func (s *Server) registerDiscoveredApps(ctx context.Context, host string, port int, timeout time.Duration) (int, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/api/apps", net.JoinHostPort(host, strconv.Itoa(port))), nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("read app list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("read app list: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Apps []App `json:"apps"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return 0, fmt.Errorf("decode app list: %w", err)
	}
	added := 0
	for _, app := range payload.Apps {
		if app.Name == "" {
			continue
		}
		if _, exists := s.apps.Get(app.Name); exists {
			continue
		}
		if err := s.apps.Register(app); err != nil {
			continue
		}
		added++
	}
	return added, nil
}
