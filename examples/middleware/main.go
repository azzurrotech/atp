// Command middleware demonstrates atp's middleware mode: atp is embedded in
// an unrelated Go server and serves the paths it owns (/api/..., /clients,
// /c/..., /gw/..., /login, /health), while every other path continues to be
// handled by the host application.
//
//	# start the host server (atp lives at :8090, admin dashboard at /clients)
//	go run ./examples/middleware --root ./data --secret '<32+ byte secret>'
//
//	curl localhost:8090/                 -> handled by the host app
//	curl localhost:8090/health           -> handled by atp
//	curl localhost:8090/clients          -> 302 to /login (atp admin)
//	curl localhost:8090/api/song/silos   -> 401 (atp API, admin only)
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"azzurrotech/atp/web"
)

func main() {
	port := flag.String("port", "8090", "host port")
	root := flag.String("root", "./data", "atp data root")
	secret := flag.String("secret", "", "atp master secret (≥ 32 chars)")
	flag.Parse()
	if len(*secret) < 32 {
		log.Fatal("example: -secret must be at least 32 characters (it encrypts the secrets vault)")
	}

	srv, err := web.NewATPService(web.Options{
		Root:          *root,
		Secret:        *secret,
		AdminUser:     "admin",
		AdminPassword: "admin",
	})
	if err != nil {
		log.Fatalf("atp: %v", err)
	}

	// The host application handles everything atp does not own. Note that atp
	// deliberately does not claim bare "/" in middleware mode.
	host := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "host app handled %s — atp is only the middleware here.\n", r.URL.Path)
	})

	addr := ":" + *port
	log.Printf("example host listening on %s (atp owns /api /clients /c /gw /login /health)", addr)
	if err := http.ListenAndServe(addr, srv.Middleware(host)); err != nil {
		log.Fatalf("host: %v", err)
	}
}