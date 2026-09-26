package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rewriteTransport struct {
	mu   sync.RWMutex
	base string
}

func (t *rewriteTransport) target() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.base
}

func (t *rewriteTransport) setTarget(s string) {
	t.mu.Lock()
	t.base = s
	t.mu.Unlock()
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.target()
	if base == "" {
		return nil, errors.New("no test target configured")
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.URL.Scheme = u.Scheme
	r.URL.Host = u.Host
	r.Host = u.Host
	return http.DefaultTransport.RoundTrip(r)
}

var testTransport = &rewriteTransport{}

func TestMain(m *testing.M) {
	httpClient.Transport = testTransport
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func startUpstream(t *testing.T, h http.Handler) {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := testTransport.target()
	testTransport.setTarget(srv.URL)
	t.Cleanup(func() {
		testTransport.setTarget(prev)
		srv.Close()
	})
}

func fixtureHandler(t *testing.T, name string) http.Handler {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	})
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %s: %v", s, err)
	}
	return v
}

func TestFetchChannelParsesFixture(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))

	title, msgs, err := fetchChannel(t.Context(), "examplechan")
	if err != nil {
		t.Fatalf("fetchChannel: %v", err)
	}
	if title != "Example Channel" {
		t.Errorf("title = %q, want %q", title, "Example Channel")
	}
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5: %+v", len(msgs), msgs)
	}

	wantIDs := []string{"examplechan/101", "examplechan/102", "examplechan/103", "examplechan/104", "examplechan/105"}
	for i, want := range wantIDs {
		if msgs[i].ID != want {
			t.Errorf("msgs[%d].ID = %q, want %q", i, msgs[i].ID, want)
		}
		if msgs[i].Channel != "examplechan" {
			t.Errorf("msgs[%d].Channel = %q, want %q", i, msgs[i].Channel, "examplechan")
		}
		if msgs[i].Link != "https://t.me/"+want {
			t.Errorf("msgs[%d].Link = %q, want %q", i, msgs[i].Link, "https://t.me/"+want)
		}
	}

	first := msgs[0]
	if first.PostID != "101" {
		t.Errorf("PostID = %q, want %q", first.PostID, "101")
	}
	if first.Author != "Alice" {
		t.Errorf("Author = %q, want %q", first.Author, "Alice")
	}
	if first.HTML != "Hello <b>world</b>" {
		t.Errorf("HTML = %q, want %q", first.HTML, "Hello <b>world</b>")
	}
	if first.PlainText != "Hello world" {
		t.Errorf("PlainText = %q, want %q", first.PlainText, "Hello world")
	}
	if want := mustTime(t, "2024-05-01T12:00:00+00:00"); !first.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", first.Date, want)
	}
	if len(first.Photos) != 0 {
		t.Errorf("Photos = %v, want none", first.Photos)
	}

	photo := msgs[1]
	wantPhotos := []string{"https://cdn.example.test/a.jpg", "https://cdn.example.test/b.jpg"}
	if len(photo.Photos) != len(wantPhotos) {
		t.Fatalf("Photos = %v, want %v", photo.Photos, wantPhotos)
	}
	for i, want := range wantPhotos {
		if photo.Photos[i] != want {
			t.Errorf("Photos[%d] = %q, want %q", i, photo.Photos[i], want)
		}
	}
	if photo.PlainText != "" || photo.HTML != "" {
		t.Errorf("photo message text = %q / %q, want empty", photo.PlainText, photo.HTML)
	}
	if photo.Author != "Bob" {
		t.Errorf("Author = %q, want %q", photo.Author, "Bob")
	}

	fallback := msgs[2]
	if fallback.PlainText != "Fallback text" {
		t.Errorf("PlainText = %q, want %q", fallback.PlainText, "Fallback text")
	}
	if fallback.HTML != "Fallback <i>text</i>" {
		t.Errorf("HTML = %q, want %q", fallback.HTML, "Fallback <i>text</i>")
	}
	if fallback.Author != "" {
		t.Errorf("Author = %q, want empty", fallback.Author)
	}

	if !msgs[3].Date.IsZero() {
		t.Errorf("missing date parsed as %v, want zero", msgs[3].Date)
	}
	if !msgs[4].Date.IsZero() {
		t.Errorf("unparsable date gave %v, want zero", msgs[4].Date)
	}
}

func TestFetchChannelRequestShape(t *testing.T) {
	var (
		mu     sync.Mutex
		path   string
		agent  string
		accept string
	)
	startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		agent = r.Header.Get("User-Agent")
		accept = r.Header.Get("Accept")
		mu.Unlock()
		_, _ = w.Write([]byte("<html></html>"))
	}))

	if _, _, err := fetchChannel(t.Context(), "examplechan"); err != nil {
		t.Fatalf("fetchChannel: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if path != "/s/examplechan" {
		t.Errorf("path = %q, want %q", path, "/s/examplechan")
	}
	if agent != userAgent {
		t.Errorf("User-Agent = %q, want %q", agent, userAgent)
	}
	if accept != "text/html" {
		t.Errorf("Accept = %q, want %q", accept, "text/html")
	}
}

func TestFetchChannelEmptyHistory(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "empty.html"))

	title, msgs, err := fetchChannel(t.Context(), "quietchan")
	if err != nil {
		t.Fatalf("fetchChannel: %v", err)
	}
	if title != "Quiet Channel" {
		t.Errorf("title = %q, want %q", title, "Quiet Channel")
	}
	if len(msgs) != 0 {
		t.Errorf("got %d messages, want 0", len(msgs))
	}
}

func TestFetchChannelUpstreamStatus(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		startUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		_, _, err := fetchChannel(t.Context(), "examplechan")
		if err == nil {
			t.Fatalf("status %d: got nil error", code)
		}
		if !strings.Contains(err.Error(), "upstream status") {
			t.Errorf("status %d: error = %v", code, err)
		}
	}
}

func TestFetchChannelTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	prev := testTransport.target()
	testTransport.setTarget(srv.URL)
	t.Cleanup(func() { testTransport.setTarget(prev) })
	srv.Close()

	if _, _, err := fetchChannel(t.Context(), "examplechan"); err == nil {
		t.Fatal("got nil error from a dead upstream")
	}
}

func TestFetchChannelCanceledContext(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "channel.html"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, _, err := fetchChannel(ctx, "examplechan")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFetchChannelParsesMediaFixture(t *testing.T) {
	startUpstream(t, fixtureHandler(t, "media.html"))

	_, msgs, err := fetchChannel(t.Context(), "examplechan")
	if err != nil {
		t.Fatalf("fetchChannel: %v", err)
	}
	byID := make(map[string]Message, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	if len(msgs) != 5 || len(byID) != 5 {
		t.Fatalf("got %d messages, want 5: %+v", len(msgs), msgs)
	}

	t.Run("reply", func(t *testing.T) {
		m := byID["examplechan/201"]
		if m.PlainText != "Reply body" {
			t.Errorf("PlainText = %q, want the reply's own text", m.PlainText)
		}
		if m.HTML != "Reply body" {
			t.Errorf("HTML = %q, want the reply's own text", m.HTML)
		}
		if m.Author != "" {
			t.Errorf("Author = %q, want empty for an unsigned post, not the quoted message's author", m.Author)
		}
		if want := mustTime(t, "2024-06-01T08:00:00+00:00"); !m.Date.Equal(want) {
			t.Errorf("Date = %v, want %v", m.Date, want)
		}
	})

	t.Run("signed reply with a quoted photo", func(t *testing.T) {
		m := byID["examplechan/202"]
		if m.Author != "Sample Poster" {
			t.Errorf("Author = %q, want %q", m.Author, "Sample Poster")
		}
		if m.PlainText != "Signed reply" {
			t.Errorf("PlainText = %q, want %q", m.PlainText, "Signed reply")
		}
		if len(m.Photos) != 0 {
			t.Errorf("Photos = %v, want none from the quoted message's thumbnail", m.Photos)
		}
	})

	t.Run("forward", func(t *testing.T) {
		m := byID["examplechan/203"]
		if m.HTML != "Forwarded <b>body</b>" {
			t.Errorf("HTML = %q, want %q", m.HTML, "Forwarded <b>body</b>")
		}
		if m.PlainText != "Forwarded body" {
			t.Errorf("PlainText = %q, want %q", m.PlainText, "Forwarded body")
		}
		if m.Author != "" {
			t.Errorf("Author = %q, want empty, the forward source is not the post author", m.Author)
		}
		if m.Channel != "examplechan" || m.PostID != "203" {
			t.Errorf("Channel/PostID = %q/%q, want examplechan/203", m.Channel, m.PostID)
		}
	})

	t.Run("video", func(t *testing.T) {
		m := byID["examplechan/204"]
		if m.PlainText != "Video caption" {
			t.Errorf("PlainText = %q, want %q", m.PlainText, "Video caption")
		}
		if want := mustTime(t, "2024-06-01T11:00:00+00:00"); !m.Date.Equal(want) {
			t.Errorf("Date = %v, want %v from the message footer, not the video duration", m.Date, want)
		}
		if len(m.Photos) != 0 {
			t.Errorf("Photos = %v, want none for a video", m.Photos)
		}
	})

	t.Run("album", func(t *testing.T) {
		m := byID["examplechan/207"]
		want := []string{
			"https://cdn.example.test/album-1.jpg",
			"https://cdn.example.test/album-2.jpg",
			"https://cdn.example.test/album-3.jpg",
		}
		if !equalStrings(m.Photos, want) {
			t.Errorf("Photos = %v, want %v", m.Photos, want)
		}
		if m.PlainText != "Album caption" {
			t.Errorf("PlainText = %q, want %q", m.PlainText, "Album caption")
		}
		if m.Link != "https://t.me/examplechan/207" {
			t.Errorf("Link = %q", m.Link)
		}
	})
}
