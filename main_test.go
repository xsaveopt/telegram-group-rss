package main

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ADDR", "INTERVAL", "MAX_MESSAGES", "BASE_PATH", "CHANNELS"} {
		t.Setenv(k, "")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

func TestGetenv(t *testing.T) {
	const key = "TELEGRAM_GROUP_RSS_TEST_KEY"

	if got := getenv(key, "fallback"); got != "fallback" {
		t.Errorf("unset: got %q, want %q", got, "fallback")
	}

	t.Setenv(key, "")
	if got := getenv(key, "fallback"); got != "fallback" {
		t.Errorf("empty: got %q, want %q", got, "fallback")
	}

	t.Setenv(key, "value")
	if got := getenv(key, "fallback"); got != "value" {
		t.Errorf("set: got %q, want %q", got, "value")
	}
}

func TestNormalizeBasePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"/", ""},
		{"  /  ", ""},
		{"///", ""},
		{"tg", "/tg"},
		{"/tg", "/tg"},
		{"/tg/", "/tg"},
		{"  /tg/  ", "/tg"},
		{"tg/rss//", "/tg/rss"},
		{"/a/b/c", "/a/b/c"},
	}
	for _, c := range cases {
		if got := normalizeBasePath(c.in); got != c.want {
			t.Errorf("normalizeBasePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitChannels(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"separators only", " ,;\n\t ", nil},
		{"single", "durov", []string{"durov"}},
		{"commas", "one,two,three", []string{"one", "two", "three"}},
		{"mixed separators", " one, two;three\nfour\tfive ", []string{"one", "two", "three", "four", "five"}},
		{"repeated separators", "one,,two,  ,three", []string{"one", "two", "three"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitChannels(c.in)
			if !equalStrings(got, c.want) {
				t.Errorf("splitChannels(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.addr != ":8080" {
		t.Errorf("addr = %q, want %q", cfg.addr, ":8080")
	}
	if cfg.interval != 5*time.Minute {
		t.Errorf("interval = %v, want 5m", cfg.interval)
	}
	if cfg.max != 100 {
		t.Errorf("max = %d, want 100", cfg.max)
	}
	if cfg.basePath != "" {
		t.Errorf("basePath = %q, want empty", cfg.basePath)
	}
	if len(cfg.channels) != 0 {
		t.Errorf("channels = %v, want empty", cfg.channels)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADDR", "127.0.0.1:9000")
	t.Setenv("INTERVAL", "90s")
	t.Setenv("MAX_MESSAGES", "7")
	t.Setenv("BASE_PATH", "tg/")
	t.Setenv("CHANNELS", "one, two;three")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.addr != "127.0.0.1:9000" {
		t.Errorf("addr = %q", cfg.addr)
	}
	if cfg.interval != 90*time.Second {
		t.Errorf("interval = %v, want 90s", cfg.interval)
	}
	if cfg.max != 7 {
		t.Errorf("max = %d, want 7", cfg.max)
	}
	if cfg.basePath != "/tg" {
		t.Errorf("basePath = %q, want /tg", cfg.basePath)
	}
	if !equalStrings(cfg.channels, []string{"one", "two", "three"}) {
		t.Errorf("channels = %v", cfg.channels)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
		want string
	}{
		{"unparsable interval", "INTERVAL", "soon", "INTERVAL"},
		{"interval below one second", "INTERVAL", "999ms", "INTERVAL must be >= 1s"},
		{"zero interval", "INTERVAL", "0s", "INTERVAL must be >= 1s"},
		{"unparsable max", "MAX_MESSAGES", "many", "MAX_MESSAGES"},
		{"zero max", "MAX_MESSAGES", "0", "MAX_MESSAGES"},
		{"negative max", "MAX_MESSAGES", "-3", "MAX_MESSAGES"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(c.key, c.val)

			_, err := loadConfig()
			if err == nil {
				t.Fatalf("%s=%q: got nil error", c.key, c.val)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestMountAt(t *testing.T) {
	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		w.WriteHeader(http.StatusTeapot)
	})
	h := mountAt("/tg", inner)

	cases := []struct {
		name  string
		path  string
		code  int
		inner string
	}{
		{"exact prefix", "/tg", http.StatusTeapot, "/"},
		{"prefix root", "/tg/", http.StatusTeapot, "/"},
		{"nested path", "/tg/feed/durov", http.StatusTeapot, "/feed/durov"},
		{"outside the prefix", "/other", http.StatusNotFound, ""},
		{"root", "/", http.StatusNotFound, ""},
		{"prefix without a separator", "/tgx", http.StatusNotFound, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seen = ""
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
			if rec.Code != c.code {
				t.Errorf("status = %d, want %d", rec.Code, c.code)
			}
			if seen != c.inner {
				t.Errorf("inner path = %q, want %q", seen, c.inner)
			}
		})
	}
}

func TestMountAtLeavesTheRequestAlone(t *testing.T) {
	h := mountAt("/tg", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/tg/feed", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if req.URL.Path != "/tg/feed" {
		t.Errorf("original request path = %q, want it untouched", req.URL.Path)
	}
}

func TestWatcherTitle(t *testing.T) {
	w := &watcher{name: "examplechan", store: NewStore(10)}
	if got := w.Title(); got != "examplechan" {
		t.Errorf("Title = %q, want the channel name", got)
	}

	w.setTitle("")
	if got := w.Title(); got != "examplechan" {
		t.Errorf("Title after setTitle(\"\") = %q, want the channel name", got)
	}

	w.setTitle("Example Channel")
	if got := w.Title(); got != "Example Channel" {
		t.Errorf("Title = %q, want %q", got, "Example Channel")
	}
}

func TestWatcherLoopFetchesAndStops(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))

	w := &watcher{name: "examplechan", store: NewStore(10), interval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		w.loop(ctx)
		close(done)
	}()

	waitFor(t, "the first scrape", func() bool { return len(w.store.List()) == 5 })

	if got := w.Title(); got != "Example Channel" {
		t.Errorf("Title = %q, want %q", got, "Example Channel")
	}

	time.Sleep(30 * time.Millisecond)
	if got := len(w.store.List()); got != 5 {
		t.Errorf("after repeated scrapes the store holds %d messages, want 5", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not return after the context was canceled")
	}
}

func TestWatcherLoopSurvivesFetchErrors(t *testing.T) {
	var calls atomic.Int64
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	w := &watcher{name: "examplechan", store: NewStore(10), interval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		w.loop(ctx)
		close(done)
	}()

	waitFor(t, "repeated failing scrapes", func() bool { return calls.Load() >= 3 })

	if got := len(w.store.List()); got != 0 {
		t.Errorf("store holds %d messages after failures, want 0", got)
	}
	if got := w.Title(); got != "examplechan" {
		t.Errorf("Title = %q, want the channel name", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not return after the context was canceled")
	}
}

func TestManagerGetRegistersAndCaches(t *testing.T) {
	var calls atomic.Int64
	handler := fixtureHandler(t, "channel.html")
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler.ServeHTTP(w, r)
	}))

	m := newManager(t.Context(), time.Hour, 10)

	w, err := m.get("ExampleChan")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if w.name != "examplechan" {
		t.Errorf("name = %q, want it lowercased", w.name)
	}
	if w.Title() != "Example Channel" {
		t.Errorf("Title = %q, want %q", w.Title(), "Example Channel")
	}
	if got := len(w.store.List()); got != 5 {
		t.Errorf("store holds %d messages, want 5", got)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}

	again, err := m.get("examplechan")
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if again != w {
		t.Error("second get returned a different watcher")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls after a cached get = %d, want 1", got)
	}
	if got := len(m.list()); got != 1 {
		t.Errorf("manager holds %d watchers, want 1", got)
	}
}

func TestManagerGetRejectsInvalidNames(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))
	m := newManager(t.Context(), time.Hour, 10)

	for _, name := range []string{"", "abc", strings.Repeat("a", 33), "has-dash", "has.dot", "has space", "../etc"} {
		if _, err := m.get(name); err == nil {
			t.Errorf("get(%q) accepted an invalid name", name)
		}
	}
	if got := len(m.list()); got != 0 {
		t.Errorf("manager holds %d watchers, want 0", got)
	}
}

func TestManagerGetDropsFailedWatcher(t *testing.T) {
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	m := newManager(t.Context(), time.Hour, 10)

	_, err := m.get("examplechan")
	if err == nil {
		t.Fatal("got nil error from a failing initial fetch")
	}
	if !strings.Contains(err.Error(), "initial fetch") {
		t.Errorf("error = %v, want it to mention the initial fetch", err)
	}
	if got := len(m.list()); got != 0 {
		t.Errorf("manager kept %d watchers after a failed fetch, want 0", got)
	}
}

func TestManagerListIsSortedByName(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))
	m := newManager(t.Context(), time.Hour, 10)

	for _, name := range []string{"charlie", "alpha1", "bravo1"} {
		if _, err := m.get(name); err != nil {
			t.Fatalf("get(%q): %v", name, err)
		}
	}

	got := make([]string, 0, 3)
	for _, w := range m.list() {
		got = append(got, w.name)
	}
	if !equalStrings(got, []string{"alpha1", "bravo1", "charlie"}) {
		t.Errorf("list = %v, want it sorted by name", got)
	}
}

func TestRunReturnsConfigErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("INTERVAL", "not-a-duration")

	if err := run(); err == nil {
		t.Fatal("got nil error from a bad INTERVAL")
	}
}

func TestRunReturnsListenErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADDR", "127.0.0.1:99999")

	if err := run(); err == nil {
		t.Fatal("got nil error from an unusable ADDR")
	}
}

func TestRunServesTheFeedAndShutsDown(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))
	addr := freeAddr(t)

	clearEnv(t)
	t.Setenv("ADDR", addr)
	t.Setenv("BASE_PATH", "/tg")
	t.Setenv("CHANNELS", "examplechan")
	t.Setenv("INTERVAL", "1h")
	t.Setenv("MAX_MESSAGES", "50")

	errc := make(chan error, 1)
	go func() { errc <- run() }()

	client := &http.Client{Timeout: 2 * time.Second}
	get := func(path string) (int, string, string) {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			return 0, "", ""
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, "", ""
		}
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
	}

	var body, contentType string
	waitFor(t, "the preloaded channel to appear in the feed", func() bool {
		code, ct, b := get("/tg/")
		if code != http.StatusOK || !strings.Contains(b, "examplechan/101") {
			return false
		}
		contentType, body = ct, b
		return true
	})

	if contentType != "application/rss+xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", contentType)
	}
	assertWellFormed(t, body)
	for _, want := range []string{"examplechan/105", "Example Channel", "Hello world"} {
		if !strings.Contains(body, want) {
			t.Errorf("feed is missing %q", want)
		}
	}

	if code, _, _ := get("/tg/nope"); code != http.StatusNotFound {
		t.Errorf("unknown path under the base path returned %d, want 404", code)
	}
	if code, _, _ := get("/"); code != http.StatusNotFound {
		t.Errorf("path outside the base path returned %d, want 404", code)
	}

	if code, ct, b := get("/tg/health"); code != http.StatusOK || b != "up" {
		t.Errorf("GET /tg/health = %d %q, want 200 \"up\"", code, b)
	} else if ct != "text/plain; charset=utf-8" {
		t.Errorf("GET /tg/health Content-Type = %q", ct)
	}
	if code, _, _ := get("/health"); code != http.StatusNotFound {
		t.Errorf("GET /health outside the base path returned %d, want 404 when BASE_PATH is set", code)
	}
	if err := checkHealth(); err != nil {
		t.Errorf("checkHealth() = %v, want nil while healthy under BASE_PATH", err)
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after SIGTERM")
	}

	if resp, err := client.Get("http://" + addr + "/tg/"); err == nil {
		_ = resp.Body.Close()
		t.Error("the server is still accepting requests after shutdown")
	}
}

func TestRunServesHealthWithoutBasePath(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))
	addr := freeAddr(t)

	clearEnv(t)
	t.Setenv("ADDR", addr)
	t.Setenv("CHANNELS", "examplechan")
	t.Setenv("INTERVAL", "1h")
	t.Setenv("MAX_MESSAGES", "10")

	errc := make(chan error, 1)
	go func() { errc <- run() }()

	client := &http.Client{Timeout: 2 * time.Second}
	get := func(path string) (int, string, string) {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			return 0, "", ""
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, "", ""
		}
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
	}

	waitFor(t, "the server to answer /health", func() bool {
		code, _, _ := get("/health")
		return code == http.StatusOK
	})

	code, ct, body := get("/health")
	if code != http.StatusOK || body != "up" {
		t.Errorf("GET /health = %d %q, want 200 \"up\"", code, body)
	}
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if err := checkHealth(); err != nil {
		t.Errorf("checkHealth() = %v, want nil while healthy without BASE_PATH", err)
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run returned %v, want nil after a clean shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after SIGTERM")
	}
}

func TestWatcherStale(t *testing.T) {
	w := &watcher{interval: 10 * time.Millisecond}
	now := time.Now()

	if !w.stale(now) {
		t.Error("a watcher with no recorded fetch should be stale")
	}

	w.recordFetch()
	if w.stale(time.Now()) {
		t.Error("a watcher that just fetched should not be stale")
	}

	w.mu.Lock()
	w.lastFetch = now.Add(-31 * time.Millisecond)
	w.mu.Unlock()
	if !w.stale(now) {
		t.Error("a fetch older than staleFactor*interval should be stale")
	}

	w.mu.Lock()
	w.lastFetch = now.Add(-5 * time.Millisecond)
	w.mu.Unlock()
	if w.stale(now) {
		t.Error("a fetch within staleFactor*interval should not be stale")
	}
}

func TestManagerHealthy(t *testing.T) {
	m := newManager(t.Context(), time.Hour, 10)

	if !m.healthy() {
		t.Error("a manager with no watchers should be healthy")
	}

	fresh := &watcher{name: "fresh", interval: time.Hour}
	fresh.recordFetch()
	m.wch["fresh"] = fresh
	if !m.healthy() {
		t.Error("a manager with one fresh watcher should be healthy")
	}

	stale := &watcher{name: "stale", interval: time.Millisecond}
	stale.mu.Lock()
	stale.lastFetch = time.Now().Add(-time.Hour)
	stale.mu.Unlock()
	m.wch["stale"] = stale
	if !m.healthy() {
		t.Error("a manager with one fresh and one stale watcher should stay healthy")
	}

	delete(m.wch, "fresh")
	if m.healthy() {
		t.Error("a manager whose only watcher is stale should be degraded")
	}
}

func TestHealthHandler(t *testing.T) {
	m := newManager(t.Context(), time.Hour, 10)
	h := healthHandler(m)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "up" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "up")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	stale := &watcher{name: "stale", interval: time.Millisecond}
	stale.mu.Lock()
	stale.lastFetch = time.Now().Add(-time.Hour)
	stale.mu.Unlock()
	m.wch["stale"] = stale

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec.Body.String() != "degraded" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "degraded")
	}
}

type getResult struct {
	w   *watcher
	n   int
	err error
}

func concurrentGets(m *manager, results chan<- getResult) {
	w, err := m.get("examplechan")
	n := -1
	if w != nil {
		n = len(w.store.List())
	}
	results <- getResult{w: w, n: n, err: err}
}

func blockingUpstream(t *testing.T, next http.Handler) (*atomic.Int64, chan struct{}, chan struct{}) {
	t.Helper()
	var calls atomic.Int64
	entered := make(chan struct{}, 64)
	release := make(chan struct{})
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		next.ServeHTTP(w, r)
	}))
	return &calls, entered, release
}

func TestManagerGetConcurrentCallersWaitForTheInitialFetch(t *testing.T) {
	const callers = 8
	calls, entered, release := blockingUpstream(t, fixtureHandler(t, "channel.html"))
	m := newManager(t.Context(), time.Hour, 10)

	results := make(chan getResult, callers)
	go concurrentGets(m, results)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the first caller never reached upstream")
	}
	for range callers - 1 {
		go concurrentGets(m, results)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)

	var first *watcher
	for i := range callers {
		var r getResult
		select {
		case r = <-results:
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d never returned", i)
		}
		if r.err != nil {
			t.Errorf("caller %d: get = %v", i, r.err)
			continue
		}
		if r.n != 5 {
			t.Errorf("caller %d: got a watcher holding %d messages, want 5 once the initial fetch finished", i, r.n)
		}
		if first == nil {
			first = r.w
		} else if r.w != first {
			t.Errorf("caller %d: got a different watcher", i)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
	if got := len(m.list()); got != 1 {
		t.Errorf("manager holds %d watchers, want 1", got)
	}
}

func TestManagerGetConcurrentCallersSeeTheInitialFetchFail(t *testing.T) {
	const callers = 8
	_, entered, release := blockingUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	m := newManager(t.Context(), time.Hour, 10)

	results := make(chan getResult, callers)
	go concurrentGets(m, results)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the first caller never reached upstream")
	}
	for range callers - 1 {
		go concurrentGets(m, results)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)

	for i := range callers {
		var r getResult
		select {
		case r = <-results:
		case <-time.After(5 * time.Second):
			t.Fatalf("caller %d never returned", i)
		}
		if r.err == nil {
			t.Errorf("caller %d: got watcher %p with %d messages and no error while the initial fetch failed", i, r.w, r.n)
		}
		if r.w != nil {
			t.Errorf("caller %d: got a watcher the manager has already dropped", i)
		}
	}
	if got := len(m.list()); got != 0 {
		t.Errorf("manager holds %d watchers, want 0", got)
	}
}

type healthProbe struct {
	port string
	mu   sync.Mutex
	path string
}

func (p *healthProbe) seen() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.path
}

func startHealthServer(t *testing.T, l net.Listener, code int) *healthProbe {
	t.Helper()
	p := &healthProbe{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.path = r.URL.Path
		p.mu.Unlock()
		w.WriteHeader(code)
	}))
	if l != nil {
		_ = srv.Listener.Close()
		srv.Listener = l
	}
	srv.Start()
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	p.port = port
	return p
}

func TestCheckHealthStatus(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		wantErr string
	}{
		{"ok", http.StatusOK, ""},
		{"degraded", http.StatusServiceUnavailable, "503"},
		{"server error", http.StatusInternalServerError, "500"},
		{"no content is not healthy", http.StatusNoContent, "204"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			p := startHealthServer(t, nil, c.code)
			t.Setenv("ADDR", ":"+p.port)

			err := checkHealth()
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("checkHealth() = %v, want nil", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("checkHealth() = %v, want an error mentioning %s", err, c.wantErr)
			}
			if got := p.seen(); got != "/health" {
				t.Errorf("probed path = %q, want /health", got)
			}
		})
	}
}

func TestCheckHealthUsesOnlyThePortAndTheBasePath(t *testing.T) {
	clearEnv(t)
	p := startHealthServer(t, nil, http.StatusOK)
	t.Setenv("ADDR", "0.0.0.0:"+p.port)
	t.Setenv("BASE_PATH", "tg/rss/")

	if err := checkHealth(); err != nil {
		t.Errorf("checkHealth() = %v, want nil", err)
	}
	if got := p.seen(); got != "/tg/rss/health" {
		t.Errorf("probed path = %q, want /tg/rss/health", got)
	}
}

func TestCheckHealthUnreachable(t *testing.T) {
	clearEnv(t)
	t.Setenv("ADDR", freeAddr(t))

	if err := checkHealth(); err == nil {
		t.Error("checkHealth() = nil with nothing listening, want an error")
	}
}

func TestCheckHealthFallsBackToTheDefaultPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:8080")
	if err != nil {
		t.Skipf("port 8080 is busy: %v", err)
	}
	p := startHealthServer(t, l, http.StatusOK)

	for _, addr := range []string{"not-an-address", "8080"} {
		clearEnv(t)
		t.Setenv("ADDR", addr)
		if err := checkHealth(); err != nil {
			t.Errorf("ADDR=%q: checkHealth() = %v, want nil via the default port", addr, err)
		}
	}
	if got := p.seen(); got != "/health" {
		t.Errorf("probed path = %q, want /health", got)
	}
}

func TestHealthcheckProcess(t *testing.T) {
	if os.Getenv("TGRSS_HEALTHCHECK_PROCESS") != "1" {
		t.Skip("runs only as the healthcheck subprocess")
	}
	os.Args = []string{os.Args[0], "-healthcheck"}
	main()
}

func TestHealthcheckFlagExitCode(t *testing.T) {
	healthy := startHealthServer(t, nil, http.StatusOK)
	degraded := startHealthServer(t, nil, http.StatusServiceUnavailable)
	_, deadPort, err := net.SplitHostPort(freeAddr(t))
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	cases := []struct {
		name string
		port string
		want int
	}{
		{"healthy", healthy.port, 0},
		{"degraded", degraded.port, 1},
		{"unreachable", deadPort, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHealthcheckProcess$")
			cmd.Env = append(os.Environ(), "TGRSS_HEALTHCHECK_PROCESS=1", "ADDR=:"+c.port, "BASE_PATH=")
			err := cmd.Run()
			got := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run: %v", err)
				}
				got = exitErr.ExitCode()
			}
			if got != c.want {
				t.Errorf("exit code = %d, want %d", got, c.want)
			}
		})
	}
}

func startRun(t *testing.T, addr string) func(string) (int, string, string) {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- run() }()

	client := &http.Client{Timeout: 2 * time.Second}
	get := func(path string) (int, string, string) {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			return 0, "", ""
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, "", ""
		}
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
	}

	t.Cleanup(func() {
		select {
		case err := <-errc:
			t.Errorf("run returned early: %v", err)
			return
		default:
		}
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
			t.Errorf("signal: %v", err)
			return
		}
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("run returned %v, want nil after a clean shutdown", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("run did not return after SIGTERM")
		}
	})

	waitFor(t, "the server to answer", func() bool {
		code, _, _ := get("/health")
		return code != 0
	})
	return get
}

func parseFeed(t *testing.T, body string) rssDoc {
	t.Helper()
	assertWellFormed(t, body)
	var doc rssDoc
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("unmarshal rss: %v", err)
	}
	return doc
}

func TestRunRootServesAnEmptyFeedWithoutChannels(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))
	addr := freeAddr(t)
	clearEnv(t)
	t.Setenv("ADDR", addr)

	get := startRun(t, addr)

	code, ct, body := get("/")
	if code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", code)
	}
	if ct != "application/rss+xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	doc := parseFeed(t, body)
	if doc.Channel.Title != "Telegram" {
		t.Errorf("channel title = %q, want Telegram", doc.Channel.Title)
	}
	if len(doc.Channel.Items) != 0 {
		t.Errorf("got %d items, want 0", len(doc.Channel.Items))
	}
	if code, _, b := get("/health"); code != http.StatusOK || b != "up" {
		t.Errorf("GET /health = %d %q, want 200 \"up\"", code, b)
	}
}

func TestRunRootServesAnEmptyFeedForAQuietChannel(t *testing.T) {
	var calls atomic.Int64
	quiet := fixtureHandler(t, "empty.html")
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		quiet.ServeHTTP(w, r)
	}))
	addr := freeAddr(t)
	clearEnv(t)
	t.Setenv("ADDR", addr)
	t.Setenv("CHANNELS", "quietchan")
	t.Setenv("INTERVAL", "1h")

	get := startRun(t, addr)
	waitFor(t, "the preload fetch", func() bool { return calls.Load() >= 1 })

	var body string
	waitFor(t, "the feed to answer", func() bool {
		code, _, b := get("/")
		body = b
		return code == http.StatusOK
	})
	doc := parseFeed(t, body)
	if len(doc.Channel.Items) != 0 {
		t.Errorf("got %d items, want 0", len(doc.Channel.Items))
	}
}

func TestRunKeepsServingWhenAPreloadFails(t *testing.T) {
	good := fixtureHandler(t, "channel.html")
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/s/brokenchan" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		good.ServeHTTP(w, r)
	}))
	addr := freeAddr(t)
	clearEnv(t)
	t.Setenv("ADDR", addr)
	t.Setenv("CHANNELS", "brokenchan, examplechan, bad-name")
	t.Setenv("INTERVAL", "1h")

	get := startRun(t, addr)

	var body string
	waitFor(t, "the working channel to appear in the feed", func() bool {
		code, _, b := get("/")
		body = b
		return code == http.StatusOK && strings.Contains(b, "examplechan/101")
	})
	doc := parseFeed(t, body)
	if len(doc.Channel.Items) != 5 {
		t.Errorf("got %d items, want the 5 from the working channel", len(doc.Channel.Items))
	}
	if code, _, b := get("/health"); code != http.StatusOK || b != "up" {
		t.Errorf("GET /health = %d %q, want 200 \"up\"", code, b)
	}
}

func TestRunRetriesAFailedPreload(t *testing.T) {
	var calls atomic.Int64
	good := fixtureHandler(t, "channel.html")
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		good.ServeHTTP(w, r)
	}))
	addr := freeAddr(t)
	clearEnv(t)
	t.Setenv("ADDR", addr)
	t.Setenv("CHANNELS", "flakychan")
	t.Setenv("INTERVAL", "1s")

	get := startRun(t, addr)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if code, _, b := get("/"); code == http.StatusOK && strings.Contains(b, "examplechan/101") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("a configured channel whose first fetch failed never reached the feed, upstream saw %d calls", calls.Load())
}
