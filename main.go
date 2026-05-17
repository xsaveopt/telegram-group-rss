package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
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

type watcher struct {
	name     string
	store    *Store
	interval time.Duration
}

func (w *watcher) loop(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			msgs, err := fetchChannel(ctx, w.name)
			if err != nil {
				log.Printf("fetch %s: %v", w.name, err)
				continue
			}
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

	msgs, err := fetchChannel(m.ctx, name)
	if err != nil {
		m.mu.Lock()
		delete(m.wch, name)
		m.mu.Unlock()
		return nil, fmt.Errorf("initial fetch: %w", err)
	}
	w.store.Add(msgs)
	go w.loop(m.ctx)
	log.Printf("watching %s every %s", name, m.interval)
	return w, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
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
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "telegram-group-rss")
		_, _ = fmt.Fprintf(w, "GET %s/feed/<channel>\n", cfg.basePath)
		_, _ = fmt.Fprintf(w, "GET %s/healthz\n", cfg.basePath)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/feed/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/feed/")
		name = strings.TrimSuffix(name, ".xml")
		if name == "" || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		watcher, err := mgr.get(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		feed := buildFeed(name, watcher.store.List())
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		if err := feed.WriteRss(w); err != nil {
			log.Printf("rss write: %v", err)
		}
	})

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
		log.Fatal(err)
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
