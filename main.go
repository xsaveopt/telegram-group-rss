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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var channelRe = regexp.MustCompile(`^[A-Za-z0-9_]{4,32}$`)

type config struct {
	addr     string
	interval time.Duration
	max      int
	basePath string
	channels []string
}

func loadConfig() (config, error) {
	c := config{
		addr:     getenv("ADDR", ":8080"),
		interval: 5 * time.Minute,
		max:      100,
	}
	if v := os.Getenv("INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("INTERVAL: %w", err)
		}
		if d < time.Second {
			return c, errors.New("INTERVAL must be >= 1s")
		}
		c.interval = d
	}
	if v := os.Getenv("MAX_MESSAGES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return c, errors.New("MAX_MESSAGES must be a positive int")
		}
		c.max = n
	}
	c.basePath = normalizeBasePath(os.Getenv("BASE_PATH"))
	c.channels = splitChannels(os.Getenv("CHANNELS"))
	return c, nil
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func normalizeBasePath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "/" {
		return ""
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return strings.TrimRight(s, "/")
}

func splitChannels(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == ';'
	})
}

const staleFactor = 3

type watcher struct {
	name     string
	store    *Store
	interval time.Duration

	mu        sync.RWMutex
	title     string
	lastFetch time.Time
}

func (w *watcher) Title() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.title != "" {
		return w.title
	}
	return w.name
}

func (w *watcher) setTitle(t string) {
	if t == "" {
		return
	}
	w.mu.Lock()
	w.title = t
	w.mu.Unlock()
}

func (w *watcher) recordFetch() {
	w.mu.Lock()
	w.lastFetch = time.Now()
	w.mu.Unlock()
}

func (w *watcher) stale(now time.Time) bool {
	w.mu.RLock()
	last := w.lastFetch
	w.mu.RUnlock()
	return now.Sub(last) > staleFactor*w.interval
}

func (w *watcher) loop(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			title, msgs, err := fetchChannel(ctx, w.name)
			if err != nil {
				log.Printf("fetch %s: %v", w.name, err)
				continue
			}
			w.setTitle(title)
			w.recordFetch()
			if n := w.store.Add(msgs); n > 0 {
				log.Printf("%s: %d new messages", w.name, n)
			}
		}
	}
}

type manager struct {
	mu       sync.Mutex
	wch      map[string]*watcher
	interval time.Duration
	max      int
	ctx      context.Context
}

func newManager(ctx context.Context, interval time.Duration, max int) *manager {
	return &manager{
		wch:      make(map[string]*watcher),
		interval: interval,
		max:      max,
		ctx:      ctx,
	}
}

func (m *manager) get(name string) (*watcher, error) {
	name = strings.ToLower(name)
	if !channelRe.MatchString(name) {
		return nil, errors.New("invalid channel name")
	}

	m.mu.Lock()
	if w, ok := m.wch[name]; ok {
		m.mu.Unlock()
		return w, nil
	}
	w := &watcher{
		name:     name,
		store:    NewStore(m.max),
		interval: m.interval,
	}
	m.wch[name] = w
	m.mu.Unlock()

	title, msgs, err := fetchChannel(m.ctx, name)
	if err != nil {
		m.mu.Lock()
		delete(m.wch, name)
		m.mu.Unlock()
		return nil, fmt.Errorf("initial fetch: %w", err)
	}
	w.setTitle(title)
	w.recordFetch()
	w.store.Add(msgs)
	go w.loop(m.ctx)
	log.Printf("watching %s every %s", name, m.interval)
	return w, nil
}

func (m *manager) list() []*watcher {
	m.mu.Lock()
	out := make([]*watcher, 0, len(m.wch))
	for _, w := range m.wch {
		out = append(out, w)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *manager) healthy() bool {
	ws := m.list()
	if len(ws) == 0 {
		return true
	}
	now := time.Now()
	for _, w := range ws {
		if !w.stale(now) {
			return true
		}
	}
	return false
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "GET /health on ADDR's port and exit 0 if healthy, 1 otherwise")
	flag.Parse()

	if *healthcheck {
		if err := checkHealth(); err != nil {
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func checkHealth() error {
	_, port, err := net.SplitHostPort(getenv("ADDR", ":8080"))
	if err != nil {
		port = "8080"
	}
	basePath := normalizeBasePath(os.Getenv("BASE_PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%s%s/health", port, basePath), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	mgr := newManager(ctx, cfg.interval, cfg.max)

	for _, name := range cfg.channels {
		go func(n string) {
			if _, err := mgr.get(n); err != nil {
				log.Printf("preload %s failed: %v", n, err)
			}
		}(name)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		entries := make([]feedEntry, 0)
		for _, wc := range mgr.list() {
			entries = append(entries, feedEntry{
				Channel: wc.name,
				Title:   wc.Title(),
				Msgs:    wc.store.List(),
			})
		}
		feed := buildFeed(entries)
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		if err := feed.WriteRss(w); err != nil {
			log.Printf("rss write: %v", err)
		}
	})
	mux.HandleFunc("/health", healthHandler(mgr))

	var handler http.Handler = mux
	if cfg.basePath != "" {
		handler = mountAt(cfg.basePath, mux)
	}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s (base path %q, %d preloaded channels, interval %s)",
		cfg.addr, cfg.basePath, len(cfg.channels), cfg.interval)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func healthHandler(mgr *manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		status := http.StatusOK
		body := "up"
		if !mgr.healthy() {
			status = http.StatusServiceUnavailable
			body = "degraded"
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func mountAt(prefix string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, prefix)
		if p == "" {
			p = "/"
		} else if p[0] != '/' {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = p
		h.ServeHTTP(w, r2)
	})
}
