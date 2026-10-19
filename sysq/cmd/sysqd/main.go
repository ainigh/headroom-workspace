// Command sysqd is sysq's daemon: the sampler, the history store, and the
// read-only HTTP API on 127.0.0.1 (design §25–26).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ainigh/headroom-workspace/sysq/internal/daemon"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	var o daemon.Options
	flag.StringVar(&o.Dir, "dir", "", "data directory (default ~/Library/Application Support/sysq)")
	flag.StringVar(&o.Fixtures, "fixtures", os.Getenv("SYSQ_FIXTURES"), `serve recorded output ("builtin" or a directory) instead of running commands`)
	flag.IntVar(&o.Port, "port", 0, "API port on 127.0.0.1 (default from clients.yaml, 11435)")
	flag.BoolVar(&o.NoSampler, "no-sampler", false, "serve the API without sampling")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	o.Version = version
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	d, err := daemon.Start(ctx, o)
	if err != nil {
		log.Fatalf("sysqd: %v", err)
	}
	log.Printf("sysqd %s listening on http://%s (admin socket %s/admin.sock)", version, d.Addr(), d.Env.Dir)
	d.Wait()
}
