// Command sysact is the confirmed mutation runner. It serves prepare and
// execute on a unix socket for a host app; execute needs a host-signed
// confirmation of the exact plan. This command has no way to execute
// anything itself.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ainigh/headroom-workspace/sysact"
	"github.com/ainigh/headroom-workspace/sysact/internal/actions"
	"github.com/ainigh/headroom-workspace/sysact/internal/confirm"
	"github.com/ainigh/headroom-workspace/sysact/internal/protect"
	"github.com/ainigh/headroom-workspace/sysact/internal/server"
	"github.com/ainigh/headroom-workspace/sysq/pkg/audit"
)

var version = "dev"

func dataDir(flagDir string) string {
	if flagDir != "" {
		return flagDir
	}
	if d := os.Getenv("SYSACT_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "sysact")
}

func setup(dir string, dry bool) (*server.Server, error) {
	shipped, _ := fs.Sub(sysact.Actions, "actions")
	roots := []fs.FS{shipped}
	if st, err := os.Stat(filepath.Join(dir, "actions")); err == nil && st.IsDir() {
		roots = append(roots, os.DirFS(filepath.Join(dir, "actions")))
	}
	cat, err := actions.Load(roots...)
	if err != nil {
		log.Printf("sysact: %v", err)
		if cat == nil {
			return nil, err
		}
	}
	home, _ := os.UserHomeDir()
	rules, err := protect.Load(filepath.Join(dir, "protected_paths.yaml"), home)
	if err != nil {
		log.Printf("sysact: %v", err)
	}
	key, err := confirm.LoadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}
	return server.New(server.Options{Catalog: cat, Protect: rules, Key: key, DryRun: dry, Audit: &audit.Log{Dir: filepath.Join(dir, "audit")}, Home: home}), nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	fsFlags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fsFlags.String("dir", "", "data directory (default ~/Library/Application Support/sysact)")
	dry := fsFlags.Bool("dry-run", false, "return plans but refuse every execute (for UI development)")
	_ = fsFlags.Parse(os.Args[2:])
	d := dataDir(*dir)
	switch os.Args[1] {
	case "serve":
		s, err := setup(d, *dry)
		if err != nil {
			log.Fatalf("sysact: %v", err)
		}
		sock := filepath.Join(d, "sysact.sock")
		l, err := server.Listen(sock)
		if err != nil {
			log.Fatalf("sysact: %v", err)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		mode := ""
		if *dry {
			mode = " (dry-run: execution refused)"
		}
		log.Printf("sysact %s on %s%s", version, sock, mode)
		if err := s.Serve(ctx, l); err != nil {
			log.Fatalf("sysact: %v", err)
		}
		_ = os.Remove(sock)
	case "catalog":
		s, err := setup(d, true)
		if err != nil {
			log.Fatal(err)
		}
		for _, a := range s.Catalog() {
			admin := ""
			if a.RequiresAdmin {
				admin = " (admin)"
			}
			fmt.Printf("%-20s %-8s %s%s\n", a.Name, a.Tier, a.Doc, admin)
		}
	case "prepare":
		s, err := setup(d, true)
		if err != nil {
			log.Fatal(err)
		}
		args := fsFlags.Args()
		if len(args) < 1 {
			log.Fatal("usage: sysact prepare <action> [param=value ...]")
		}
		params := map[string]any{}
		for _, kv := range args[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				log.Fatalf("expected param=value, got %q", kv)
			}
			params[k] = v
		}
		p, err := s.Prepare(context.Background(), args[0], params)
		if err != nil {
			log.Fatal(err)
		}
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Println(string(b))
		fmt.Println("(a plan from this command can't be executed: only the running service executes, and only with a host confirmation)")
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sysact serve [--dry-run] [--dir DIR] | sysact catalog | sysact prepare <action> [param=value ...] | sysact version")
	os.Exit(2)
}
