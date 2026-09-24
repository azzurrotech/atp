// Command atp runs the AzzurroTech Platform orchestrator in server mode: a
// standalone, standard-library-only Go webserver that embeds song, pod and
// shepherd and adds client management, an encrypted secrets vault, per-client
// usage logging and hourly-size billing.
//
// atp also operates in middleware mode for host Go servers: see
// web.ATPService.Middleware and examples/middleware.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"azzurrotech/atp/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("atp: %v", err)
	}
}

func run() error {
	var (
		port  = flag.String("port", envOr("ATP_PORT", "8080"), "HTTP listen port")
		root  = flag.String("root", envOr("ATP_ROOT", "./data"), "data root (clients, secrets, logs, song + pod stores)")
		secret = flag.String("secret", envOr("ATP_SECRET", ""), "master secret, >= 32 chars (encrypts secrets, signs tokens)")
		user   = flag.String("admin-user", envOr("ATP_ADMIN_USER", "admin"), "admin username")
		pass   = flag.String("admin-password", envOr("ATP_ADMIN_PASSWORD", ""), "admin password (defaults to \"admin\" — set this!)")
		price  = flag.Float64("price", web.DefaultPrice, "default price per GB per hour (USD)")
		ret    = flag.Int("retention", 24, "default request-log retention in hours")
		rate   = flag.Float64("rate", 120, "rate limit per minute per client (0 disables)")
		burst  = flag.Float64("burst", 30, "rate limiter burst")
		noUI   = flag.Bool("no-ui", false, "disable the management web UI")
		seed   = flag.Bool("seed", false, "provision a demo client")
		ver    = flag.Bool("version", false, "print version and exit")
		help   = flag.Bool("help", false, "show usage and exit")
	)
	flag.Parse()

	if *help {
		fmt.Println(`atp — AzzurroTech Platform orchestrator

Embedded services:
  song      siloed static hosting + SSR + magic-link auth     /api/song
  pod       filesystem XML database (HTML-form driven)        /api/pod
  shepherd  identity-less IAM, firewall, rate limiter, gateway /api/shepherd /gw/
  atp       client & secret mgmt, usage logging, billing      / (admin UI)

Server mode (this binary):
  atp --port 8080 --root ./data --secret '<32+ byte secret>' --admin-password '...'

Middleware mode (embed in another Go server):
  import "azzurrotech/atp/web"
  srv, _ := web.NewATPService(web.Options{Root: "./data", Secret: "..."})
  http.Handle("/", srv.Middleware(myHostHandler))

Options:
  --port <n>             HTTP port                    (env ATP_PORT, default 8080)
  --root <dir>           data root                    (env ATP_ROOT, default ./data)
  --secret <key>         master secret >= 32 chars    (env ATP_SECRET)
  --admin-user <u>       admin username               (env ATP_ADMIN_USER, default admin)
  --admin-password <p>   admin password               (env ATP_ADMIN_PASSWORD, default "admin")
  --price <usd>          default $/GB per hour        (default 0.05)
  --retention <hours>    default log retention        (default 24)
  --rate <rpm>           rate limit per minute        (default 120)
  --burst <n>            rate limiter burst           (default 30)
  --no-ui                disable the web UI
  --seed                 create a demo client exercising every feature
  --version / --help`)
		return nil
	}

	if *ver {
		fmt.Printf("atp v%s\n", web.Version)
		return nil
	}

	if *secret == "" {
		return fmt.Errorf("a master --secret of at least 32 characters is required (set ATP_SECRET); " +
			"it encrypts the secrets vault and signs capability tokens")
	}

	svc, err := web.NewATPService(web.Options{
		Port:                 *port,
		Root:                 *root,
		Secret:               *secret,
		AdminUser:            *user,
		AdminPassword:        *pass,
		DisableUI:            *noUI,
		Seed:                 *seed,
		DefaultRatePerMinute: *rate,
		Burst:                *burst,
	})
	if err != nil {
		return err
	}

	// Apply explicit pricing/retention overrides after defaults are loaded.
	settings := svc.Clients().Settings()
	cur := settings
	changed := false
	if *price > 0 && *price != web.DefaultPrice {
		cur.DefaultPricePerGBHour = *price
		changed = true
	}
	if *ret > 0 && *ret != 24 {
		cur.DefaultRetentionHours = *ret
		changed = true
	}
	if changed {
		if err := svc.Clients().SetSettings(cur); err != nil {
			return err
		}
	}

	return svc.Start()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}