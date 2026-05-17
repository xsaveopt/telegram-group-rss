package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/feeds"
)

type feedEntry struct {
	Channel string
	Title   string
	Msgs    []Message
}

func makeTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 120
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit-3]) + "..."
	}
	return s
}

func buildFeed(entries []feedEntry) *feeds.Feed {
	feed := &feeds.Feed{
		Title:       "Telegram",
		Link:        &feeds.Link{Href: "https://t.me/"},
		Description: "Merged feed of monitored Telegram channels",
		Created:     time.Now(),
	}

	for _, e := range entries {
		author := e.Title
		if author == "" {
			author = e.Channel
		}
		for _, m := range e.Msgs {
			title := makeTitle(m.PlainText)
			if title == "" {
				if len(m.Photos) > 0 {
					title = "(photo) #" + m.PostID
				} else {
					title = "#" + m.PostID
				}
			}

			desc := m.HTML
			for _, p := range m.Photos {
				desc += fmt.Sprintf(`<p><img src=%q alt=""/></p>`, p)
			}

			feed.Items = append(feed.Items, &feeds.Item{
				Id:          m.ID,
				Title:       title,
				Link:        &feeds.Link{Href: m.Link},
				Description: desc,
				Created:     m.Date,
				Author:      &feeds.Author{Name: author},
			})
		}
	}

	sort.Slice(feed.Items, func(i, j int) bool {
		return feed.Items[i].Created.After(feed.Items[j].Created)
	})
	if len(feed.Items) > 0 {
		feed.Updated = feed.Items[0].Created
	}
	return feed
}
