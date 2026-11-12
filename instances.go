// instances.go — atp's control plane for the first-party services.
//
// Each BLUE service (pod, song, shepherd, stenella, rapid) is a standalone
// binary: it can be started on its own, pointed at a remote peer, or run under
// a different supervisor. When atp is asked to manage instances, it does three
// things and nothing more:
//
//   - it starts the configured command as a child process and records the PID;
//   - it waits for the instance's own health endpoint before reporting "up";
//   - it stops the child with SIGTERM, then SIGKILL, when asked.
//
// It never imports the services. The manifest is the whole contract, so an
// operator can adopt an already-running instance (atp will not spawn a second
// one) or leave a service out and run it elsewhere.
package atp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Instance states reported by the supervisor.
const (
	InstanceStateStopped   = "stopped"   // nothing is listening
	InstanceStateStarting  = "starting"  // a child was spawned, health not yet green
	InstanceStateUp        = "up"        // managed child, health green
	InstanceStateExternal  = "external"  // health green, not started by atp
	InstanceStateUnhealthy = "unhealthy" // managed child alive, health red
	InstanceStateFailed    = "failed"    // managed child exited
)

// DefaultInstanceHealthTimeout bounds how long Start waits for an instance's
// health endpoint before giving up and reporting it unhealthy.
const DefaultInstanceHealthTimeout = 15 * time.Second

// InstanceSpec is one manageable service instance. It is the unit of the
// manifest and of the /api/instances API.
type InstanceSpec struct {
	// Name is the instance key used by the API and by Captured().
	Name string `json:"name"`
	// Dir is the working directory of the child. Relative paths resolve
	// against the manifest's directory.
	Dir string `json:"dir,omitempty"`
	// Command is the executable: an absolute path, a path relative to Dir,
	// or a name resolved through PATH.
	Command string `json:"command"`
	// Args are passed verbatim to Command.
	Args []string `json:"args,omitempty"`
	// Env is merged over atp's own environment for the child.
	Env map[string]string `json:"env,omitempty"`
	// URL is the instance's base URL (scheme://host:port).
	URL string `json:"url"`
	// Health is the path atp probes to answer "is it up?". "GET <URL><Health>"
	// must return a 2xx status.
	Health string `json:"health,omitempty"`
	// Restart asks atp to relaunch the child after an unexpected exit.
	Restart bool `json:"restart,omitempty"`
	// Capture names log prefixes whose trailing value atp should remember.
	// The canonical use is shepherd's printed admin token:
	//
	//	"capture": {"shepherd_admin": "admin token (send as X-Shepherd-Admin):"}
	//
	// It lets atp hold the capability without duplicating shepherd's KDF and
	// without storing a shared secret in the manifest.
	Capture map[string]string `json:"capture,omitempty"`
}

// Manifest is the JSON document atp loads to know which instances exist.
type Manifest struct {
	// LogDir receives one file per instance. Relative paths resolve against
	// the manifest's directory. Default: <manifest dir>/logs.
	LogDir string `json:"log_dir,omitempty"`
	// HealthTimeout is how long Start waits for each instance, as a Go
	// duration string ("15s"). Default: DefaultInstanceHealthTimeout.
	HealthTimeout string `json:"health_timeout,omitempty"`
	// Instances are started in this order and stopped in reverse order, so
	// dependencies must appear first.
	Instances []InstanceSpec `json:"instances"`
}

// InstanceStatus is the live answer for one instance.
type InstanceStatus struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	OK       bool   `json:"ok"`
	Managed  bool   `json:"managed"`
	PID      int    `json:"pid,omitempty"`
	URL      string `json:"url,omitempty"`
	Health   string `json:"health,omitempty"`
	Uptime   string `json:"uptime,omitempty"`
	Restarts int    `json:"restarts,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Error    string `json:"error,omitempty"`
	Log      string `json:"log,omitempty"`
}

// managedProc is one child process atp owns.
type managedProc struct {
	cmd       *exec.Cmd
	pid       int
	started   time.Time
	stopping  bool
	done      chan struct{}
	exitCode  int
	exitError error
}

// Supervisor owns zero or more child processes. It is safe for concurrent use.
type Supervisor struct {
	mu       sync.Mutex
	specs    []InstanceSpec
	byName   map[string]InstanceSpec
	order    []string
	root     string // manifest directory
	logDir   string
	timeout  time.Duration
	client   *http.Client
	procs    map[string]*managedProc
	restarts map[string]int
	halted   map[string]bool
	captured map[string]string
	closed   bool
	stopCh   chan struct{}
}

// LoadManifest reads a manifest file and builds a Supervisor from it. Relative
// directories and log paths resolve against the manifest's own directory, so a
// manifest can be committed next to the projects it launches.
func LoadManifest(path string) (*Supervisor, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("atp: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("atp: parse manifest %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)

	logDir := m.LogDir
	if logDir == "" {
		logDir = filepath.Join(dir, "logs")
	} else if !filepath.IsAbs(logDir) {
		logDir = filepath.Join(dir, logDir)
	}
	timeout := DefaultInstanceHealthTimeout
	if m.HealthTimeout != "" {
		d, err := time.ParseDuration(m.HealthTimeout)
		if err != nil {
			return nil, fmt.Errorf("atp: manifest health_timeout: %w", err)
		}
		timeout = d
	}
	for i := range m.Instances {
		expandSpec(&m.Instances[i])
	}
	return NewSupervisor(m.Instances, dir, logDir, timeout)
}

// NewSupervisor validates specs and returns a supervisor that has not started
// anything yet.
func NewSupervisor(specs []InstanceSpec, root, logDir string, timeout time.Duration) (*Supervisor, error) {
	if timeout <= 0 {
		timeout = DefaultInstanceHealthTimeout
	}
	if logDir == "" && root != "" {
		logDir = filepath.Join(root, "logs")
	}
	s := &Supervisor{
		specs:    make([]InstanceSpec, 0, len(specs)),
		byName:   make(map[string]InstanceSpec, len(specs)),
		order:    make([]string, 0, len(specs)),
		root:     root,
		logDir:   logDir,
		timeout:  timeout,
		client:   &http.Client{Timeout: 3 * time.Second},
		procs:    map[string]*managedProc{},
		restarts: map[string]int{},
		halted:   map[string]bool{},
		captured: map[string]string{},
		stopCh:   make(chan struct{}),
	}
	for _, spec := range specs {
		if !isToken(spec.Name) {
			return nil, fmt.Errorf("atp: invalid instance name %q", spec.Name)
		}
		if _, dup := s.byName[spec.Name]; dup {
			return nil, fmt.Errorf("atp: duplicate instance %q", spec.Name)
		}
		if strings.TrimSpace(spec.Command) == "" {
			return nil, fmt.Errorf("atp: instance %q has no command", spec.Name)
		}
		u, err := url.Parse(spec.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("atp: instance %q has invalid url %q", spec.Name, spec.URL)
		}
		if spec.Health != "" && !strings.HasPrefix(spec.Health, "/") {
			return nil, fmt.Errorf("atp: instance %q health path must start with /", spec.Name)
		}
		if spec.Dir == "" {
			spec.Dir = root
		} else if !filepath.IsAbs(spec.Dir) {
			spec.Dir = filepath.Join(root, spec.Dir)
		}
		s.specs = append(s.specs, spec)
		s.byName[spec.Name] = spec
		s.order = append(s.order, spec.Name)
	}
	if logDir != "" {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return nil, fmt.Errorf("atp: create log dir: %w", err)
		}
	}
	return s, nil
}

// expandSpec applies $VAR / ${VAR} expansion to every string a manifest can
// hold. It lets a manifest say "SHEPHERD_SECRET": "${SHEPHERD_SECRET}" without
// committing a secret to disk.
func expandSpec(spec *InstanceSpec) {
	spec.Dir = os.ExpandEnv(spec.Dir)
	spec.Command = os.ExpandEnv(spec.Command)
	spec.URL = os.ExpandEnv(spec.URL)
	spec.Health = os.ExpandEnv(spec.Health)
	for i := range spec.Args {
		spec.Args[i] = os.ExpandEnv(spec.Args[i])
	}
	if spec.Env != nil {
		env := make(map[string]string, len(spec.Env))
		for k, v := range spec.Env {
			env[k] = os.ExpandEnv(v)
		}
		spec.Env = env
	}
	if spec.Capture != nil {
		cap := make(map[string]string, len(spec.Capture))
		for k, v := range spec.Capture {
			cap[k] = os.ExpandEnv(v)
		}
		spec.Capture = cap
	}
}

// Specs returns the configured instances in start order.
func (s *Supervisor) Specs() []InstanceSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InstanceSpec, 0, len(s.specs))
	for _, name := range s.order {
		out = append(out, s.byName[name])
	}
	return out
}

// LogDir returns the directory that receives the instance logs.
func (s *Supervisor) LogDir() string { return s.logDir }

// Captured returns a value the supervisor remembered from a child's log, such
// as shepherd's admin token. The second result reports whether it is known.
func (s *Supervisor) Captured(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captured[key]
}

// Start launches one instance and waits for its health endpoint. If something
// is already answering on the instance's URL, Start adopts it instead of
// starting a second copy; the returned status has State "external".
func (s *Supervisor) Start(ctx context.Context, name string) (InstanceStatus, error) {
	spec, ok := s.specByName(name)
	if !ok {
		return InstanceStatus{}, fmt.Errorf("atp: unknown instance %q", name)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return InstanceStatus{}, errors.New("atp: supervisor is closed")
	}
	s.halted[name] = false
	if _, running := s.procs[name]; running {
		s.mu.Unlock()
		return s.statusFor(ctx, spec), nil
	}
	s.mu.Unlock()

	if ok, _, _, _ := s.probe(ctx, spec); ok {
		s.mu.Lock()
		s.captureFromLogLocked(spec)
		s.mu.Unlock()
		st := s.statusFor(ctx, spec)
		st.State = InstanceStateExternal
		st.Managed = false
		return st, nil
	}

	p, err := s.spawn(spec)
	if err != nil {
		st := InstanceStatus{
			Name:  name,
			State: InstanceStateFailed,
			URL:   spec.URL,
			Error: err.Error(),
			Log:   filepath.Join(s.logDir, name+".log"),
		}
		return st, err
	}
	s.mu.Lock()
	s.procs[name] = p
	s.mu.Unlock()

	healthy, detail, healthErr := s.waitHealthy(ctx, spec)
	st := s.statusFor(ctx, spec)
	if !healthy {
		if healthErr == "" {
			healthErr = "health endpoint did not become ready"
		}
		st.OK = false
		st.Detail = detail
		st.Error = healthErr
		if st.State == InstanceStateUp || st.State == InstanceStateExternal {
			st.State = InstanceStateUnhealthy
		}
		return st, fmt.Errorf("atp: %s did not become healthy: %s", name, healthErr)
	}

	s.mu.Lock()
	s.captureFromLogLocked(spec)
	s.mu.Unlock()

	st = s.statusFor(ctx, spec)
	st.OK = true
	st.Detail = detail
	st.Error = ""
	st.State = InstanceStateUp
	return st, nil
}

// Stop terminates an instance atp started. An adopted (external) instance is
// left alone: atp does not kill a process it does not own.
func (s *Supervisor) Stop(ctx context.Context, name string) (InstanceStatus, error) {
	spec, ok := s.specByName(name)
	if !ok {
		return InstanceStatus{}, fmt.Errorf("atp: unknown instance %q", name)
	}

	s.mu.Lock()
	s.halted[name] = true
	p := s.procs[name]
	if p != nil {
		p.stopping = true
	}
	s.mu.Unlock()

	if p == nil {
		if ok, detail, _, _ := s.probe(ctx, spec); ok {
			return InstanceStatus{
				Name:    name,
				State:   InstanceStateExternal,
				OK:      true,
				Managed: false,
				URL:     spec.URL,
				Health:  spec.Health,
				Detail:  detail,
			}, nil
		}
		return InstanceStatus{Name: name, State: InstanceStateStopped, URL: spec.URL, Health: spec.Health}, nil
	}

	_ = killProcess(p.cmd.Process, false)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = killProcess(p.cmd.Process, true)
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
	s.mu.Lock()
	if cur, present := s.procs[name]; present && cur == p {
		delete(s.procs, name)
	}
	s.mu.Unlock()
	return InstanceStatus{Name: name, State: InstanceStateStopped, URL: spec.URL, Health: spec.Health}, nil
}

// Restart stops then starts one instance.
func (s *Supervisor) Restart(ctx context.Context, name string) (InstanceStatus, error) {
	if _, err := s.Stop(ctx, name); err != nil {
		return InstanceStatus{}, err
	}
	return s.Start(ctx, name)
}

// StartAll starts every configured instance in manifest order. It keeps going
// after a failure so one broken service does not hide the state of the rest.
func (s *Supervisor) StartAll(ctx context.Context) map[string]error {
	errs := map[string]error{}
	for _, spec := range s.Specs() {
		if ctx.Err() != nil {
			errs[spec.Name] = ctx.Err()
			continue
		}
		if _, err := s.Start(ctx, spec.Name); err != nil {
			errs[spec.Name] = err
		}
	}
	return errs
}

// StopAll stops managed instances in reverse manifest order.
func (s *Supervisor) StopAll(ctx context.Context) map[string]error {
	errs := map[string]error{}
	for i := len(s.order) - 1; i >= 0; i-- {
		name := s.order[i]
		if _, err := s.Stop(ctx, name); err != nil {
			errs[name] = err
		}
	}
	return errs
}

// Status probes every instance concurrently and returns one status per entry,
// in manifest order.
func (s *Supervisor) Status(ctx context.Context) []InstanceStatus {
	specs := s.Specs()
	out := make([]InstanceStatus, len(specs))
	var wg sync.WaitGroup
	for i, spec := range specs {
		wg.Add(1)
		go func(i int, spec InstanceSpec) {
			defer wg.Done()
			out[i] = s.statusFor(ctx, spec)
		}(i, spec)
	}
	wg.Wait()
	return out
}

// Tail returns the last n lines of an instance's log.
func (s *Supervisor) Tail(name string, n int) ([]string, error) {
	if _, ok := s.specByName(name); !ok {
		return nil, fmt.Errorf("atp: unknown instance %q", name)
	}
	if n <= 0 {
		n = 100
	}
	if n > 1000 {
		n = 1000
	}
	path := filepath.Join(s.logDir, name+".log")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	ring := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if len(ring) == n {
			copy(ring, ring[1:])
			ring[n-1] = sc.Text()
			continue
		}
		ring = append(ring, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}

// Close stops every instance atp started and refuses further starts. It is safe
// to call more than once.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stopCh)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s.StopAll(ctx)
	return nil
}

// --- internals ---

func (s *Supervisor) specByName(name string) (InstanceSpec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, ok := s.byName[name]
	return spec, ok
}

func (s *Supervisor) spawn(spec InstanceSpec) (*managedProc, error) {
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = mergeEnv(os.Environ(), spec.Env)
	configureProcess(cmd)

	logPath := filepath.Join(s.logDir, spec.Name+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("atp: open log for %s: %w", spec.Name, err)
	}
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("atp: start %s: %w", spec.Name, err)
	}
	p := &managedProc{
		cmd:     cmd,
		pid:     cmd.Process.Pid,
		started: time.Now(),
		done:    make(chan struct{}),
	}
	go s.reap(spec, p, f)
	return p, nil
}

// reap waits for a child, records its exit status and applies the restart
// policy. It must not hold s.mu while waiting.
func (s *Supervisor) reap(spec InstanceSpec, p *managedProc, logFile *os.File) {
	err := p.cmd.Wait()
	_ = logFile.Close()

	code := 0
	if err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}

	s.mu.Lock()
	p.exitCode = code
	p.exitError = err
	stopping := p.stopping
	if cur, present := s.procs[spec.Name]; present && cur == p {
		delete(s.procs, spec.Name)
	}
	closed := s.closed
	halted := s.halted[spec.Name]
	if !stopping && !closed && !halted {
		s.restarts[spec.Name]++
	}
	attempt := s.restarts[spec.Name]
	s.mu.Unlock()

	close(p.done)

	if stopping || closed || halted || !spec.Restart {
		return
	}
	go s.scheduleRestart(spec, attempt)
}

func (s *Supervisor) scheduleRestart(spec InstanceSpec, attempt int) {
	backoff := time.Duration(1<<min(attempt, 5)) * time.Second
	select {
	case <-time.After(backoff):
	case <-s.stopCh:
		return
	}
	s.mu.Lock()
	if s.closed || s.halted[spec.Name] {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout+5*time.Second)
	defer cancel()
	_, _ = s.Start(ctx, spec.Name)
}

func (s *Supervisor) statusFor(ctx context.Context, spec InstanceSpec) InstanceStatus {
	ok, detail, errMsg, _ := s.probe(ctx, spec)

	s.mu.Lock()
	p := s.procs[spec.Name]
	restarts := s.restarts[spec.Name]
	s.mu.Unlock()

	st := InstanceStatus{
		Name:     spec.Name,
		URL:      spec.URL,
		Health:   spec.Health,
		OK:       ok,
		Detail:   detail,
		Restarts: restarts,
		Log:      filepath.Join(s.logDir, spec.Name+".log"),
	}
	if p == nil {
		if ok {
			st.State = InstanceStateExternal
		} else {
			st.State = InstanceStateStopped
		}
		return st
	}

	st.Managed = true
	st.PID = p.pid
	st.Uptime = time.Since(p.started).Round(time.Second).String()
	select {
	case <-p.done:
		st.State = InstanceStateFailed
		st.OK = false
		code := p.exitCode
		st.ExitCode = &code
		if p.exitError != nil {
			st.Error = p.exitError.Error()
		}
	default:
		if ok {
			st.State = InstanceStateUp
		} else {
			st.State = InstanceStateUnhealthy
			st.Error = errMsg
		}
	}
	return st
}

func (s *Supervisor) waitHealthy(ctx context.Context, spec InstanceSpec) (bool, string, string) {
	deadline := time.Now().Add(s.timeout)
	lastErr := "health endpoint did not answer"
	for {
		if ok, detail, errMsg, _ := s.probe(ctx, spec); ok {
			return true, detail, ""
		} else if errMsg != "" {
			lastErr = errMsg
		}

		s.mu.Lock()
		_, alive := s.procs[spec.Name]
		s.mu.Unlock()
		if !alive {
			return false, "", "process exited before becoming healthy"
		}

		select {
		case <-ctx.Done():
			return false, "", ctx.Err().Error()
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return false, "", fmt.Sprintf("%s after %s", lastErr, s.timeout)
		}
	}
}

// probe asks an instance's health endpoint for a 2xx.
func (s *Supervisor) probe(ctx context.Context, spec InstanceSpec) (ok bool, detail, errMsg, elapsed string) {
	target := strings.TrimRight(spec.URL, "/") + spec.Health
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return false, "", err.Error(), "0s"
	}
	resp, err := s.client.Do(req)
	elapsed = time.Since(start).Round(time.Millisecond).String()
	if err != nil {
		return false, "", err.Error(), elapsed
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detail = strings.TrimSpace(string(body))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, detail, fmt.Sprintf("health returned HTTP %d", resp.StatusCode), elapsed
	}
	return true, detail, "", elapsed
}

// captureFromLogLocked remembers the values named by spec.Capture. The caller
// must hold s.mu.
func (s *Supervisor) captureFromLogLocked(spec InstanceSpec) {
	for key, prefix := range spec.Capture {
		if v := s.scanLog(spec.Name, prefix); v != "" {
			s.captured[key] = v
		}
	}
}

func (s *Supervisor) scanLog(name, prefix string) string {
	path := filepath.Join(s.logDir, name+".log")
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	found := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		i := strings.Index(line, prefix)
		if i < 0 {
			continue
		}
		if v := strings.TrimSpace(line[i+len(prefix):]); v != "" {
			found = v
		}
	}
	return found
}

// mergeEnv overlays extra on base, keeping the last value for each key.
func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return append([]string(nil), base...)
	}
	index := make(map[string]int, len(base))
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		index[k] = len(out)
		out = append(out, kv)
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kv := k + "=" + extra[k]
		if i, ok := index[k]; ok {
			out[i] = kv
			continue
		}
		out = append(out, kv)
	}
	return out
}
