// Command atp runs the AzzurroTech Platform as one server: the modified
// fileserver, the function registry, the secrets vault, the head and header
// managers, hosted applications and generated client-app capabilities.
//
// pod, song and shepherd are separate processes. atp reaches them over HTTP at
// the URLs given by --pod-url, --song-url and --shepherd-url (or the matching
// ATP_POD_URL, ATP_SONG_URL and ATP_SHEPHERD_URL environment variables).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"azzurrotech/atp"
)

func main() {
	var (
		addr      = flag.String("addr", env("ATP_ADDR", ":8080"), "listen address (host:port)")
		portsStr  = flag.String("ports", env("ATP_PORTS", ""), "comma-separated list of ports to scan for auto-discovery (overrides defaults)")
		root      = flag.String("root", env("ATP_ROOT", "./data"), "root for hosted apps and the vault")
		secret    = flag.String("secret", env("ATP_SECRET", ""), "master secret, at least 32 bytes (vault key)")
		encrypt   = flag.Bool("encrypt", envBool("ATP_ENCRYPT", false), "encrypt dynamic content at rest")
		headless  = flag.Bool("headless", envBool("ATP_HEADLESS", false), "serve apps/functions without the management view")
		ipHdr     = flag.String("client-ip-header", env("ATP_CLIENT_IP_HEADER", ""), "trusted proxy client-IP header, e.g. X-Forwarded-For")
		title     = flag.String("title", env("ATP_TITLE", ""), "default document title")
		podURL    = flag.String("pod-url", env("ATP_POD_URL", atp.DefaultPodURL), "base URL of the pod service")
		songURL   = flag.String("song-url", env("ATP_SONG_URL", atp.DefaultSongURL), "base URL of the song service")
		shepURL   = flag.String("shepherd-url", env("ATP_SHEPHERD_URL", atp.DefaultShepherdURL), "base URL of the shepherd service")
		shepAdmin = flag.String("shepherd-admin", env("ATP_SHEPHERD_ADMIN", ""), "shepherd admin token (sent as X-Shepherd-Admin)")
		discover  = flag.Bool("discover", envBool("ATP_DISCOVER", false), "auto-discover ATP instances on localhost and register their apps")
		manifest  = flag.String("manifest", env("ATP_MANIFEST", ""), "instance manifest (JSON); empty uses ./platform.json when present")
		logDir    = flag.String("log-dir", env("ATP_LOG_DIR", ""), "override the manifest's instance log directory")
		supervise = flag.Bool("supervise", envBool("ATP_SUPERVISE", false), "start every manifest instance at startup and stop it on shutdown")
		version   = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	// Parse custom ports from flag or environment variable. A malformed entry is
	// fatal: silently scanning a different set than the operator asked for is
	// worse than refusing to start.
	var customPorts []int
	if *portsStr != "" {
		for _, p := range strings.Split(*portsStr, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 65535 {
				log.Fatalf("atp: invalid port %q in -ports/%s (want 1-65535)", p, "ATP_PORTS")
			}
			customPorts = append(customPorts, n)
		}
		if len(customPorts) == 0 {
			log.Fatalf("atp: -ports/ATP_PORTS contained no usable port")
		}
	}

	if *version {
		fmt.Printf("atp %s\n", atp.Version)
		return
	}

	srv, err := atp.New(atp.Options{
		Root:           *root,
		Addr:           *addr,
		Secret:         *secret,
		Encrypt:        *encrypt,
		Headless:       *headless,
		ClientIPHeader: *ipHdr,
		Title:          *title,
		PodURL:         *podURL,
		SongURL:        *songURL,
		ShepherdURL:    *shepURL,
		ShepherdAdmin:  *shepAdmin,
		Manifest:       *manifest,
		LogDir:         *logDir,
		Supervise:      *supervise,
	})
	if err != nil {
		log.Fatalf("atp: %v", err)
	}
	defer srv.Close()

	log.Printf("atp %s listening on %s (root %s)", atp.Version, srv.Addr(), srv.Root())
	log.Printf("  pod      : %s", *podURL)
	log.Printf("  song     : %s", *songURL)
	log.Printf("  shepherd : %s", *shepURL)
	for _, w := range srv.Warnings() {
		log.Printf("  warning  : %s", w)
	}

	if *discover {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := srv.AutoDiscovery(ctx, atp.AutoDiscoveryConfig{
			RegisterApps: true,
			Ports:        customPorts,
		})
		if err != nil {
			log.Printf("atp: auto-discovery error: %v", err)
		} else {
			log.Printf("atp: auto-discovery scanned %d port(s), registered %d app(s)",
				len(result.Probes), result.TotalApps)
			for _, probe := range result.Probes {
				if !probe.Healthy {
					log.Printf("  port %d: %v", probe.Port, probe.Error)
					continue
				}
				if probe.Service != "" {
					log.Printf("  port %d: healthy, service %s %s", probe.Port, probe.Service, probe.Version)
				} else {
					log.Printf("  port %d: healthy", probe.Port)
				}
			}
			for _, w := range result.Warnings {
				log.Printf("  warning: %s", w)
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := net.Listen("tcp", srv.Addr())
	if err != nil {
		log.Fatalf("atp: listen %s: %v", srv.Addr(), err)
	}

	httpSrv := &http.Server{
		Addr:              srv.Addr(),
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	// Supervised instances start only after the listener accepts connections:
	// their health probes and upstream registration both go through atp.
	if *supervise {
		if srv.Instances() == nil {
			log.Printf("atp: -supervise requested but no manifest is loaded")
		} else if specs := srv.Instances().Specs(); len(specs) == 0 {
			log.Printf("atp: manifest has no instances")
		} else {
			errs := srv.StartAll(ctx)
			for _, spec := range specs {
				if err := errs[spec.Name]; err != nil {
					log.Printf("atp: instance %s did not start: %v", spec.Name, err)
					continue
				}
				log.Printf("atp: instance %s started", spec.Name)
			}
			if len(errs) > 0 {
				log.Printf("atp: %d instance(s) did not start", len(errs))
			}
		}
	}

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("atp: serve: %v", err)
		}
	case <-ctx.Done():
		log.Printf("atp: shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for name, err := range srv.StopAll(shutCtx) {
			if err != nil {
				log.Printf("atp: instance %s did not stop cleanly: %v", name, err)
			}
		}
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			log.Printf("atp: shutdown: %v", err)
		}
	}
}

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), `atp %s — the AzzurroTech Platform

usage: atp [flags]

`, atp.Version)
	flag.PrintDefaults()
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
