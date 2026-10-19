// Package daemon assembles sysqd: environment, history store, sampler,
// loopback HTTP API, admin socket and hot reload.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/engine"
	"github.com/ainigh/headroom-workspace/sysq/internal/history"
	"github.com/ainigh/headroom-workspace/sysq/internal/httpapi"
	"github.com/ainigh/headroom-workspace/sysq/internal/sampler"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/setup"
)

// Options configure the daemon.
type Options struct {
	Dir       string
	Fixtures  string
	Port      int // 0: clients.yaml api.port
	NoSampler bool
	Version   string
	Now       func() time.Time
}

// Daemon is a running sysqd.
type Daemon struct {
	Opts    Options
	Env     *setup.Env
	Store   *history.Store
	Sampler *sampler.Sampler
	Server  *httpapi.Server
	Admin   *httpapi.Admin
	API     net.Listener
	AdminL  net.Listener

	mu     sync.Mutex
	mtimes map[string]time.Time
	wg     sync.WaitGroup
}

// Paths under the data directory.
func (d *Daemon) path(name string) string { return filepath.Join(d.Env.Dir, name) }

// Start opens everything and begins serving. Stop by cancelling ctx, then Wait.
func Start(ctx context.Context, o Options) (*Daemon, error) {
	env, err := setup.Open(setup.Options{Dir: o.Dir, Fixtures: o.Fixtures})
	if err != nil {
		return nil, err
	}
	if o.Now != nil {
		env.Engine.Now = o.Now
	}
	d := &Daemon{Opts: o, Env: env, mtimes: map[string]time.Time{}}
	if err := os.MkdirAll(env.Dir, 0o700); err != nil {
		return nil, err
	}
	d.Store, err = history.Open(d.path("history.db"), false)
	if err != nil {
		return nil, err
	}
	if o.Now != nil {
		d.Store.SetClock(o.Now)
	}
	schedFile := d.path("schedules.yaml")
	if _, err := os.Stat(schedFile); os.IsNotExist(err) {
		_ = os.WriteFile(schedFile, sampler.DefaultYAML(), 0o644)
	}
	cfg, err := sampler.Load(schedFile)
	var warnings []string
	if err != nil {
		warnings = append(warnings, "schedules.yaml: "+err.Error()+" (using defaults)")
		cfg = sampler.Default()
	}
	clients, err := httpapi.LoadClients(d.path("clients.yaml"))
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	d.Server = httpapi.New(nil, clients)
	d.Sampler = sampler.New(d.Store, func(ctx context.Context, name string, params map[string]any) ([]script.Item, error) {
		b := d.Server.B()
		return httpapi.RunName(ctx, b, name, params, engine.CallOpts{MaxSensitivity: "personal", Ledger: &engine.Ledger{Budget: 4 * b.Engine.Policy.CostBudget}})
	}, cfg)
	if o.Now != nil {
		d.Sampler.Now = o.Now
	}
	d.Sampler.PowerProbe = func(ctx context.Context) (bool, error) {
		b := d.Server.B()
		t, err := b.Engine.Primitive(ctx, "sys.power", nil, engine.CallOpts{})
		if err != nil || t.Len() == 0 {
			return false, err
		}
		src, _ := t.Get(0, "source").(string)
		return strings.EqualFold(src, "battery"), nil
	}
	d.Server.SetBackend(d.backend(env, warnings))
	d.Admin = &httpapi.Admin{
		S: d.Server,
		OnState: func(view bool, batt *bool) {
			d.Sampler.SetViewOpen(view)
			if batt != nil {
				d.Sampler.SetOnBattery(*batt)
			}
		},
		SaveSchedules: func(data []byte) error {
			c, err := sampler.Parse(data)
			if err != nil {
				return err
			}
			if err := os.WriteFile(schedFile, data, 0o644); err != nil {
				return err
			}
			d.Sampler.SetConfig(c)
			d.touch(schedFile)
			return nil
		},
		Reload: d.Reload,
	}

	port := o.Port
	if port == 0 {
		port = clients.API().Port
	}
	d.API, err = httpapi.ListenLoopback(port)
	if err != nil {
		return nil, fmt.Errorf("listen 127.0.0.1:%d: %w", port, err)
	}
	d.AdminL, err = httpapi.ListenAdmin(d.path("admin.sock"))
	if err != nil {
		d.API.Close()
		return nil, fmt.Errorf("admin socket: %w", err)
	}
	for _, f := range d.watched() {
		d.touch(f)
	}
	d.wg.Add(2)
	go func() { defer d.wg.Done(); _ = httpapi.Serve(ctx, d.API, d.Server.Handler()) }()
	go func() { defer d.wg.Done(); _ = httpapi.Serve(ctx, d.AdminL, d.Admin.Handler()) }()
	if !o.NoSampler {
		d.wg.Add(1)
		go func() { defer d.wg.Done(); d.Sampler.Start(ctx) }()
	}
	d.wg.Add(1)
	go func() { defer d.wg.Done(); d.watch(ctx) }()
	go func() {
		<-ctx.Done()
		_ = os.Remove(d.path("admin.sock"))
	}()
	return d, nil
}

func (d *Daemon) backend(env *setup.Env, warnings []string) *httpapi.Backend {
	hist := history.Register(env.Engine, d.Store, func() []history.Series {
		if c := d.Sampler.Config(); c != nil && len(c.Narrative) > 0 {
			return c.Narrative
		}
		return nil
	})
	if env.PolicyError != nil {
		warnings = append(warnings, "policy.yaml: "+env.PolicyError.Error())
	}
	if env.CatalogErr != nil {
		warnings = append(warnings, env.CatalogErr.Error())
	}
	if env.RecipesErr != nil {
		warnings = append(warnings, env.RecipesErr.Error())
	}
	renv := env.RecipeEnv()
	renv.History = hist
	return &httpapi.Backend{
		Engine: env.Engine, Recipes: env.Recipes, RecipeEnv: renv, Store: d.Store, Sampler: d.Sampler,
		Audit: env.Audit, FixtureEngine: env.FixtureEngine, UserRecipeDir: env.UserRecipeDir(),
		Version: d.Opts.Version, Warnings: warnings,
		Narrative: func() []history.Series { return d.Sampler.Config().Narrative },
	}
}

// Reload rebuilds the environment from disk (catalog, recipes, policy) and
// re-reads schedules and clients. Bad files keep the last good state.
func (d *Daemon) Reload() error {
	env, err := setup.Open(setup.Options{Dir: d.Opts.Dir, Fixtures: d.Opts.Fixtures})
	if err != nil {
		return err
	}
	if d.Opts.Now != nil {
		env.Engine.Now = d.Opts.Now
	}
	var warnings []string
	if c, err := sampler.Load(d.path("schedules.yaml")); err != nil {
		warnings = append(warnings, "schedules.yaml: "+err.Error()+" (kept the previous schedules)")
	} else {
		d.Sampler.SetConfig(c)
	}
	if fresh, err := httpapi.LoadClients(d.path("clients.yaml")); err != nil {
		warnings = append(warnings, err.Error())
	} else {
		d.Server.Clients.Replace(fresh)
	}
	d.Env = env
	d.Server.SetBackend(d.backend(env, warnings))
	if len(warnings) > 0 {
		return errors.New(strings.Join(warnings, "; "))
	}
	return nil
}

func (d *Daemon) watched() []string {
	files := []string{d.path("policy.yaml"), d.path("schedules.yaml"), d.path("clients.yaml")}
	for _, dir := range []string{d.path("catalog"), d.path("recipes")} {
		_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				files = append(files, p)
			}
			return nil
		})
	}
	return files
}

func (d *Daemon) touch(f string) {
	st, err := os.Stat(f)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.mtimes[f] = time.Time{}
		return
	}
	d.mtimes[f] = st.ModTime()
}

// watch polls config files every 2 s and reloads on change (cheap, and
// works without FSEvents bindings).
func (d *Daemon) watch(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		changed := false
		files := d.watched()
		d.mu.Lock()
		if len(files) != len(d.mtimes) {
			changed = true
		}
		for _, f := range files {
			st, err := os.Stat(f)
			var m time.Time
			if err == nil {
				m = st.ModTime()
			}
			if old, ok := d.mtimes[f]; !ok || !old.Equal(m) {
				changed = true
			}
		}
		d.mu.Unlock()
		if changed {
			d.mu.Lock()
			d.mtimes = map[string]time.Time{}
			d.mu.Unlock()
			for _, f := range files {
				d.touch(f)
			}
			if err := d.Reload(); err != nil {
				log.Printf("sysqd: reload: %v", err)
			} else {
				log.Printf("sysqd: reloaded configuration")
			}
		}
	}
}

// Wait blocks until all servers stop.
func (d *Daemon) Wait() {
	d.wg.Wait()
	if d.Store != nil {
		d.Store.Close()
	}
}

// Addr is the API address.
func (d *Daemon) Addr() string { return d.API.Addr().String() }
