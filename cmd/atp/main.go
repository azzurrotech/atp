// Command atp runs the AzzurroTech Platform as one server: the modified
// fileserver, the function registry, the secrets vault, the head and header
// managers, hosted applications and generated client-app capabilities.
//
// pod, song and shepherd are separate processes. atp reaches them over HTTP at
// the URLs given by --pod-url, --song-url and --shepherd-url (or the matching
// ATP_POD_URL, ATP_SONG_URL and ATP_SHEPHERD_URL environment variables).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"azzurrotech/atp"
)

func main() {
	var (
		addr      = flag.String("addr", env("ATP_ADDR", ":8080"), "listen address (host:port)")
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
		version   = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = usage
	flag.Parse()

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
	if err := http.ListenAndServe(srv.Addr(), srv); err != nil {
		log.Fatal(err)
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
