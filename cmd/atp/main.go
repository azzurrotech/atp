// Command atp runs the AzzurroTech Platform as one self-contained server: the
// modified fileserver, the function registry, the secrets vault, the head and
// header managers, hosted applications, generated client-app capabilities and
// the in-process song, pod and shepherd modules.
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
		addr     = flag.String("addr", env("ATP_ADDR", ":8080"), "listen address (host:port)")
		root     = flag.String("root", env("ATP_ROOT", "./data"), "root for hosted apps, stores and the vault")
		secret   = flag.String("secret", env("ATP_SECRET", ""), "master secret, at least 32 bytes (vault key + token signing)")
		encrypt  = flag.Bool("encrypt", envBool("ATP_ENCRYPT", false), "encrypt dynamic content at rest")
		headless = flag.Bool("headless", envBool("ATP_HEADLESS", false), "serve apps/functions without the management view")
		ipHdr    = flag.String("client-ip-header", env("ATP_CLIENT_IP_HEADER", ""), "trusted proxy client-IP header, e.g. X-Forwarded-For")
		title    = flag.String("title", env("ATP_TITLE", ""), "default document title")
		version  = flag.Bool("version", false, "print version and exit")
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
	})
	if err != nil {
		log.Fatalf("atp: %v", err)
	}
	defer srv.Close()

	log.Printf("atp %s listening on %s (root %s)", atp.Version, srv.Addr(), srv.Root())
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
