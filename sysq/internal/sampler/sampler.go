package sampler

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ainigh/headroom-workspace/sysq/internal/history"
	"github.com/ainigh/headroom-workspace/sysq/internal/script"
	"github.com/ainigh/headroom-workspace/sysq/internal/table"
)

// RunFunc runs a recipe or primitive for the sampler.
type RunFunc func(ctx context.Context, name string, params map[string]any) ([]script.Item, error)

// Update is one sampled result, as delivered to subscribers.
type Update struct {
	Key   string        `json:"key"`
	Name  string        `json:"name"`
	TS    time.Time     `json:"ts"`
	Items []script.Item `json:"items"`
	Error string        `json:"error,omitempty"`
}

// Sampler schedules recipe runs.
type Sampler struct {
	Store *history.Store
	Run   RunFunc
	Now   func() time.Time
	// PowerProbe reports whether the Mac is on battery (nil: never).
	PowerProbe func(ctx context.Context) (bool, error)

	cfg       atomic.Pointer[Config]
	viewOpen  atomic.Bool
	onBattery atomic.Bool

	mu      sync.Mutex
	last    map[string]time.Time
	running map[string]bool
	prev    map[string]map[string]string // schedule -> column -> last value (events)
	subs    map[string]map[int]chan Update
	watches map[string]*watch
	nextID  int
	sem     chan struct{}
}

type watch struct {
	name   string
	params map[string]any
	every  time.Duration
	refs   map[int]time.Duration
}

// New returns a sampler with a config.
func New(store *history.Store, run RunFunc, cfg *Config) *Sampler {
	s := &Sampler{Store: store, Run: run, Now: time.Now,
		last: map[string]time.Time{}, running: map[string]bool{}, prev: map[string]map[string]string{},
		subs: map[string]map[int]chan Update{}, watches: map[string]*watch{}, sem: make(chan struct{}, 2)}
	s.cfg.Store(cfg)
	return s
}

// SetConfig swaps schedules (hot reload).
func (s *Sampler) SetConfig(c *Config) { s.cfg.Store(c) }

// Config returns the active schedules.
func (s *Sampler) Config() *Config { return s.cfg.Load() }

// SetViewOpen tells the sampler whether a host view is visible.
func (s *Sampler) SetViewOpen(v bool) { s.viewOpen.Store(v) }

// SetOnBattery overrides the power state.
func (s *Sampler) SetOnBattery(v bool) { s.onBattery.Store(v) }

// State reports view and power state.
func (s *Sampler) State() (viewOpen, onBattery bool) { return s.viewOpen.Load(), s.onBattery.Load() }

// Key identifies a recipe+params combination.
func Key(name string, params map[string]any) string {
	if len(params) == 0 {
		return name
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		v, _ := json.Marshal(params[k])
		b.WriteString("|" + k + "=" + string(v))
	}
	return b.String()
}

// Start runs until ctx is done.
func (s *Sampler) Start(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastDown := s.Now()
	lastPower := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := s.Now()
		if s.PowerProbe != nil && now.Sub(lastPower) >= time.Minute {
			lastPower = now
			go func() {
				if b, err := s.PowerProbe(ctx); err == nil {
					s.onBattery.Store(b)
				}
			}()
		}
		s.Tick(ctx, now)
		if now.Sub(lastDown) >= 5*time.Minute && s.Store != nil {
			lastDown = now
			if err := s.Store.Downsample(ctx, s.Config().MaxRetention()); err != nil {
				log.Printf("sysqd: downsample: %v", err)
			}
		}
	}
}

// Tick starts every schedule and watch that is due (exported for tests).
func (s *Sampler) Tick(ctx context.Context, now time.Time) {
	cfg := s.Config()
	view, batt := s.State()
	type job struct {
		key    string
		name   string
		recipe string
		params map[string]any
		sched  *Schedule
	}
	var jobs []job
	s.mu.Lock()
	for i := range cfg.Schedules {
		sc := &cfg.Schedules[i]
		if sc.Disabled {
			continue
		}
		key := Key(sc.Recipe, sc.Params)
		iv := sc.Interval(view, batt)
		// A live subscriber at a faster rate speeds the schedule up.
		if w, ok := s.watches[key]; ok && w.every < iv {
			iv = w.every
		}
		if !s.running[key] && now.Sub(s.last[key]) >= iv {
			jobs = append(jobs, job{key, sc.Name, sc.Recipe, sc.Params, sc})
		}
	}
	scheduled := map[string]bool{}
	for _, j := range jobs {
		scheduled[j.key] = true
	}
	for key, w := range s.watches {
		if scheduled[key] || s.running[key] || s.isScheduled(cfg, key) {
			continue
		}
		if now.Sub(s.last[key]) >= w.every {
			jobs = append(jobs, job{key: key, name: w.name, recipe: w.name, params: w.params})
		}
	}
	for _, j := range jobs {
		s.running[j.key] = true
		s.last[j.key] = now
	}
	s.mu.Unlock()
	for _, j := range jobs {
		j := j
		go func() {
			s.sem <- struct{}{}
			defer func() { <-s.sem }()
			s.sample(ctx, j.key, j.name, j.recipe, j.params, j.sched)
		}()
	}
}

func (s *Sampler) isScheduled(cfg *Config, key string) bool {
	for _, sc := range cfg.Schedules {
		if !sc.Disabled && Key(sc.Recipe, sc.Params) == key {
			return true
		}
	}
	return false
}

// SampleNow runs one schedule synchronously (tests, warm-up).
func (s *Sampler) SampleNow(ctx context.Context, name string) {
	for i := range s.Config().Schedules {
		sc := &s.Config().Schedules[i]
		if sc.Name == name {
			key := Key(sc.Recipe, sc.Params)
			s.mu.Lock()
			s.running[key] = true
			s.last[key] = s.Now()
			s.mu.Unlock()
			s.sample(ctx, key, sc.Name, sc.Recipe, sc.Params, sc)
		}
	}
}

func (s *Sampler) sample(ctx context.Context, key, name, recipe string, params map[string]any, sc *Schedule) {
	defer func() {
		s.mu.Lock()
		s.running[key] = false
		s.mu.Unlock()
	}()
	ts := s.Now()
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	items, err := s.Run(rctx, recipe, params)
	cancel()
	up := Update{Key: key, Name: name, TS: ts, Items: items}
	if err != nil {
		up.Error = err.Error()
	}
	if sc != nil && err == nil && s.Store != nil && len(items) > 0 {
		t := items[0].Table
		if err := s.Store.Record(ctx, history.Sample{Recipe: sc.Name, Label: items[0].Label, TS: ts, Table: t, KeepTable: sc.KeepTable, Columns: sc.Columns, Key: sc.Key}); err != nil {
			log.Printf("sysqd: record %s: %v", sc.Name, err)
		}
		s.events(ctx, sc.Name, t, ts)
	}
	s.publish(key, up)
}

// events records transitions configured in schedules.yaml.
func (s *Sampler) events(ctx context.Context, name string, t *table.Table, ts time.Time) {
	if t.Len() == 0 {
		return
	}
	for _, r := range s.Config().Events {
		if r.Name != name {
			continue
		}
		i := t.ColIndex(r.Column)
		if i < 0 {
			continue
		}
		cur := table.Format(t.Rows[0][i])
		s.mu.Lock()
		if s.prev[name] == nil {
			s.prev[name] = map[string]string{}
		}
		before, had := s.prev[name][r.Column]
		s.prev[name][r.Column] = cur
		s.mu.Unlock()
		if !had {
			before = s.Store.GetMeta(ctx, "last:"+name+":"+r.Column)
			if before == "" {
				_ = s.Store.SetMeta(ctx, "last:"+name+":"+r.Column, cur)
				continue
			}
		}
		if before == cur {
			continue
		}
		_ = s.Store.SetMeta(ctx, "last:"+name+":"+r.Column, cur)
		msg := strings.NewReplacer("{from}", before, "{to}", cur, "{name}", name).Replace(r.Message)
		_ = s.Store.AddEvent(ctx, history.Event{TS: ts, Kind: r.Kind, Recipe: name, Subject: r.Column, Message: msg, Severity: r.Severity})
	}
}

func (s *Sampler) publish(key string, up Update) {
	s.mu.Lock()
	var chans []chan Update
	for _, c := range s.subs[key] {
		chans = append(chans, c)
	}
	s.mu.Unlock()
	for _, c := range chans {
		select {
		case c <- up:
		default: // slow subscriber: drop this update
		}
	}
}

// Subscribe streams updates for name+params at roughly every. The returned
// cancel must be called.
func (s *Sampler) Subscribe(name string, params map[string]any, every time.Duration) (<-chan Update, func()) {
	if every < time.Second {
		every = time.Second
	}
	key := Key(name, params)
	ch := make(chan Update, 4)
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	if s.subs[key] == nil {
		s.subs[key] = map[int]chan Update{}
	}
	s.subs[key][id] = ch
	w, ok := s.watches[key]
	if !ok {
		w = &watch{name: name, params: params, refs: map[int]time.Duration{}}
		s.watches[key] = w
	}
	w.refs[id] = every
	w.every = minEvery(w.refs)
	s.last[key] = time.Time{} // first update promptly
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subs[key], id)
		if w, ok := s.watches[key]; ok {
			delete(w.refs, id)
			if len(w.refs) == 0 {
				delete(s.watches, key)
			} else {
				w.every = minEvery(w.refs)
			}
		}
	}
}

func minEvery(m map[int]time.Duration) time.Duration {
	var best time.Duration
	for _, d := range m {
		if best == 0 || d < best {
			best = d
		}
	}
	return best
}
