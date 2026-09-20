package main

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type rssDoc struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		Items       []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			Author      string `xml:"author"`
			PubDate     string `xml:"pubDate"`
			GUID        string `xml:"guid"`
		} `xml:"item"`
	} `xml:"channel"`
}

func assertWellFormed(t *testing.T, doc string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatalf("malformed XML: %v\n%s", err, doc)
		}
	}
}

func TestMakeTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"trims", "  hello  ", "hello"},
		{"collapses whitespace", "a\n\nb\t c   d", "a b c d"},
		{"whitespace only", " \n\t ", ""},
		{"leaves short text", "short title", "short title"},
		{"leaves exactly the limit", strings.Repeat("x", 120), strings.Repeat("x", 120)},
		{"truncates over the limit", strings.Repeat("x", 121), strings.Repeat("x", 117) + "..."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := makeTitle(c.in); got != c.want {
				t.Errorf("makeTitle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMakeTitleCountsRunes(t *testing.T) {
	in := strings.Repeat("é", 200)
	got := makeTitle(in)
	runes := []rune(got)
	if len(runes) != 120 {
		t.Fatalf("got %d runes, want 120", len(runes))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("got %q, want an ellipsis suffix", got)
	}
	if strings.Contains(got, "�") {
		t.Error("truncation split a multi-byte rune")
	}
}

func TestBuildFeedMetadata(t *testing.T) {
	feed := buildFeed(nil)
	if feed.Title != "Telegram" {
		t.Errorf("Title = %q, want %q", feed.Title, "Telegram")
	}
	if feed.Link == nil || feed.Link.Href != "https://t.me/" {
		t.Errorf("Link = %+v, want https://t.me/", feed.Link)
	}
	if feed.Description == "" {
		t.Error("Description is empty")
	}
	if len(feed.Items) != 0 {
		t.Errorf("got %d items, want 0", len(feed.Items))
	}
	if !feed.Updated.IsZero() {
		t.Errorf("Updated = %v, want zero for an empty feed", feed.Updated)
	}

	out, err := feed.ToRss()
	if err != nil {
		t.Fatalf("ToRss: %v", err)
	}
	assertWellFormed(t, out)
}

func TestBuildFeedItems(t *testing.T) {
	entries := []feedEntry{
		{
			Channel: "examplechan",
			Title:   "Example Channel",
			Msgs: []Message{
				{
					ID:        "examplechan/101",
					PostID:    "101",
					PlainText: "Hello world",
					HTML:      "Hello <b>world</b>",
					Date:      baseTime,
					Link:      "https://t.me/examplechan/101",
				},
				{
					ID:     "examplechan/102",
					PostID: "102",
					Date:   baseTime.Add(time.Hour),
					Link:   "https://t.me/examplechan/102",
					Photos: []string{"https://cdn.example.test/a.jpg"},
				},
				{
					ID:     "examplechan/103",
					PostID: "103",
					Date:   baseTime.Add(2 * time.Hour),
					Link:   "https://t.me/examplechan/103",
				},
			},
		},
		{
			Channel: "otherchan",
			Msgs: []Message{
				{
					ID:        "otherchan/7",
					PostID:    "7",
					PlainText: "Newest of all",
					HTML:      "Newest of all",
					Date:      baseTime.Add(3 * time.Hour),
					Link:      "https://t.me/otherchan/7",
				},
			},
		},
	}

	feed := buildFeed(entries)
	if len(feed.Items) != 4 {
		t.Fatalf("got %d items, want 4", len(feed.Items))
	}

	wantOrder := []string{"otherchan/7", "examplechan/103", "examplechan/102", "examplechan/101"}
	for i, want := range wantOrder {
		if feed.Items[i].Id != want {
			t.Errorf("Items[%d].Id = %q, want %q", i, feed.Items[i].Id, want)
		}
	}
	if !feed.Updated.Equal(baseTime.Add(3 * time.Hour)) {
		t.Errorf("Updated = %v, want %v", feed.Updated, baseTime.Add(3*time.Hour))
	}

	byID := make(map[string]int, len(feed.Items))
	for i, it := range feed.Items {
		byID[it.Id] = i
	}

	text := feed.Items[byID["examplechan/101"]]
	if text.Title != "Hello world" {
		t.Errorf("Title = %q, want %q", text.Title, "Hello world")
	}
	if text.Author == nil || text.Author.Name != "Example Channel" {
		t.Errorf("Author = %+v, want Example Channel", text.Author)
	}
	if text.Description != "Hello <b>world</b>" {
		t.Errorf("Description = %q, want %q", text.Description, "Hello <b>world</b>")
	}
	if text.Link == nil || text.Link.Href != "https://t.me/examplechan/101" {
		t.Errorf("Link = %+v", text.Link)
	}

	photo := feed.Items[byID["examplechan/102"]]
	if photo.Title != "(photo) #102" {
		t.Errorf("Title = %q, want %q", photo.Title, "(photo) #102")
	}
	if photo.Description != `<p><img src="https://cdn.example.test/a.jpg" alt=""/></p>` {
		t.Errorf("Description = %q", photo.Description)
	}

	bare := feed.Items[byID["examplechan/103"]]
	if bare.Title != "#103" {
		t.Errorf("Title = %q, want %q", bare.Title, "#103")
	}

	fallbackAuthor := feed.Items[byID["otherchan/7"]]
	if fallbackAuthor.Author == nil || fallbackAuthor.Author.Name != "otherchan" {
		t.Errorf("Author = %+v, want the channel name as fallback", fallbackAuthor.Author)
	}
}

func TestBuildFeedRendersWellFormedXML(t *testing.T) {
	entries := []feedEntry{
		{
			Channel: "examplechan",
			Title:   "Example & Channel <test>",
			Msgs: []Message{
				{
					ID:        "examplechan/101",
					PostID:    "101",
					PlainText: "5 < 6 & \"quoted\"",
					HTML:      `raw <b>html</b> & <a href="https://t.me/x?a=1&b=2">link</a>`,
					Date:      baseTime,
					Link:      "https://t.me/examplechan/101?a=1&b=2",
					Photos:    []string{"https://cdn.example.test/a.jpg?x=1&y=2"},
				},
			},
		},
	}

	feed := buildFeed(entries)

	rss, err := feed.ToRss()
	if err != nil {
		t.Fatalf("ToRss: %v", err)
	}
	assertWellFormed(t, rss)

	atom, err := feed.ToAtom()
	if err != nil {
		t.Fatalf("ToAtom: %v", err)
	}
	assertWellFormed(t, atom)

	var doc rssDoc
	if err := xml.Unmarshal([]byte(rss), &doc); err != nil {
		t.Fatalf("unmarshal rss: %v", err)
	}
	if doc.Version != "2.0" {
		t.Errorf("rss version = %q, want 2.0", doc.Version)
	}
	if doc.Channel.Title != "Telegram" {
		t.Errorf("channel title = %q, want Telegram", doc.Channel.Title)
	}
	if len(doc.Channel.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(doc.Channel.Items))
	}

	item := doc.Channel.Items[0]
	if item.GUID != "examplechan/101" {
		t.Errorf("guid = %q, want %q", item.GUID, "examplechan/101")
	}
	if item.Link != "https://t.me/examplechan/101?a=1&b=2" {
		t.Errorf("link = %q", item.Link)
	}
	if item.Title != `5 < 6 & "quoted"` {
		t.Errorf("title = %q", item.Title)
	}
	if !strings.Contains(item.Description, `<a href="https://t.me/x?a=1&b=2">link</a>`) {
		t.Errorf("description lost its markup after a round trip: %q", item.Description)
	}
	if !strings.Contains(item.Description, `<img src="https://cdn.example.test/a.jpg?x=1&y=2" alt=""/>`) {
		t.Errorf("description lost its photo markup: %q", item.Description)
	}
	if item.Author != "Example & Channel <test>" {
		t.Errorf("author = %q", item.Author)
	}
	if item.PubDate == "" {
		t.Error("pubDate is empty")
	}
}
