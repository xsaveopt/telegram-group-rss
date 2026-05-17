package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type Message struct {
	ID        string
	Channel   string
	PostID    string
	Author    string
	HTML      string
	PlainText string
	Date      time.Time
	Link      string
	Photos    []string
}

const userAgent = "telegram-group-rss/0.1 (+https://github.com/sratabix/telegram-group-rss)"

var (
	httpClient = &http.Client{Timeout: 30 * time.Second}
	bgURLRe    = regexp.MustCompile(`url\(['"]?([^'")]+)['"]?\)`)
)

func fetchChannel(ctx context.Context, channel string) (string, []Message, error) {
	url := fmt.Sprintf("https://t.me/s/%s", channel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", nil, err
	}

	title := strings.TrimSpace(doc.Find(".tgme_channel_info_header_title").First().Text())

	var msgs []Message
	doc.Find(".tgme_widget_message").Each(func(_ int, s *goquery.Selection) {
		post, _ := s.Attr("data-post")
		if post == "" {
			return
		}
		parts := strings.SplitN(post, "/", 2)
		if len(parts) != 2 {
			return
		}

		textSel := s.Find(".tgme_widget_message_text.js-message_text").First()
		if textSel.Length() == 0 {
			textSel = s.Find(".tgme_widget_message_text").First()
		}
		htmlText, _ := textSel.Html()
		plain := strings.TrimSpace(textSel.Text())

		var date time.Time
		if t := s.Find(".tgme_widget_message_date time").AttrOr("datetime", ""); t != "" {
			date, _ = time.Parse(time.RFC3339, t)
		}

		author := strings.TrimSpace(s.Find(".tgme_widget_message_author_name").First().Text())

		var photos []string
		s.Find(".tgme_widget_message_photo_wrap").Each(func(_ int, p *goquery.Selection) {
			if style, ok := p.Attr("style"); ok {
				if m := bgURLRe.FindStringSubmatch(style); len(m) == 2 {
					photos = append(photos, m[1])
				}
			}
		})

		msgs = append(msgs, Message{
			ID:        post,
			Channel:   parts[0],
			PostID:    parts[1],
			Author:    author,
			HTML:      htmlText,
			PlainText: plain,
			Date:      date,
			Link:      "https://t.me/" + post,
			Photos:    photos,
		})
	})

	return title, msgs, nil
}
