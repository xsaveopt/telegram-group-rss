package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/feeds"
)

func makeTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const limit = 120
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit-3]) + "..."
	}
	return s
}

func buildFeed(channel string, msgs []Message) *feeds.Feed {
	feed := &feeds.Feed{
		Title:       "Telegram: " + channel,
		Link:        &feeds.Link{Href: "https://t.me/s/" + channel},
		Description: "Recent messages from t.me/" + channel,
		Created:     time.Now(),
	}

	for _, m := range msgs {
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

		author := m.Channel
		if author == "" {
			author = channel
		}
		item := &feeds.Item{
			Id:          m.ID,
			Title:       title,
			Link:        &feeds.Link{Href: m.Link},
			Description: desc,
			Created:     m.Date,
			Author:      &feeds.Author{Name: author},
		}
		feed.Items = append(feed.Items, item)
	}

	if len(feed.Items) > 0 {
		feed.Updated = feed.Items[0].Created
	}
	return feed
}
