# telegram-group-rss

**Scrapes the public web preview of a Telegram channel (`https://t.me/s/<channel>`) and serves each as an RSS feed. No API key, no bot, no auth.**

> ⚠️ Only works for **public channels**. Telegram groups don't have a `/s/` preview and would need the Bot API or MTProto instead.

## Contents

- [How it works](#how-it-works)
- [Quick start](#quick-start)
- [docker-compose](#docker-compose)
- [Endpoints](#endpoints)
- [Reverse proxy](#reverse-proxy)
- [Image tags](#image-tags)
- [Environment variables](#environment-variables)
- [Building from source](#building-from-source)

## How it works

A small Go service. The first request to `/feed/<channel>` spawns a per-channel watcher that does a synchronous fetch and then re-polls `t.me/s/<channel>` in the background every `INTERVAL`. Each scrape parses the `.tgme_widget_message` blocks, dedupes by `data-post` id, and keeps the last `MAX_MESSAGES` in memory. Subsequent feed requests just render the in-memory store as RSS. Channels listed in `CHANNELS` are warmed at startup in the background so the HTTP server is up immediately.

Storage is in-memory only — a restart loses history beyond what the next scrape returns (roughly the last 20 messages that `t.me/s/` exposes). The RSS `<author>` on every item is set to the channel id, so aggregating multiple feeds in one reader keeps the source identifiable.

## Quick start

```bash
docker run --rm -p 8080:8080 \
  -e CHANNELS="durov,telegram" \
  -e INTERVAL=2m \
  ghcr.io/sratabix/telegram-group-rss:latest

curl http://localhost:8080/feed/durov
```

## docker-compose

```yaml
services:
  telegram-group-rss:
    image: ghcr.io/sratabix/telegram-group-rss:latest
    container_name: telegram-group-rss
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      CHANNELS: "durov,telegram"
      INTERVAL: 5m
```

## Endpoints

| Path | Description |
|---|---|
| `GET /` | Plain-text usage hint |
| `GET /healthz` | Liveness probe (`ok`) |
| `GET /feed/<channel>` | RSS feed. `.xml` suffix is also accepted. |

When `BASE_PATH=/tg`, all paths are prefixed: `/tg/feed/<channel>`, `/tg/healthz`, etc.

## Reverse proxy

`BASE_PATH` makes paths match upstream, so no rewriting is needed:

```nginx
location /tg/ {
    proxy_pass http://tgrss:8080;
}
```

## Image tags

`latest` for the latest stable release. `1`, `1.2`, `1.2.3` to pin to a major, minor, or patch line. Pre-releases like `1.2.3-rc1` are never tagged `latest`. `dev` tracks the tip of the `main` branch (rebuilt on every commit) and is the easiest tag to use for testing without waiting for a release. Per-commit immutable tags are also published as `dev-<sha>`. Images are published to `ghcr.io/sratabix/telegram-group-rss` and built for `linux/amd64`.

## Environment variables

| Var | Default | Purpose |
|---|---|---|
| `ADDR` | `:8080` | HTTP listen address. |
| `INTERVAL` | `5m` | Poll interval per channel (Go duration: `30s`, `2m`, `1h`). Anything below ~`1m` risks rate-limiting. |
| `MAX_MESSAGES` | `100` | Max messages kept in memory per channel. |
| `CHANNELS` | *(empty)* | Comma / space / semicolon separated list of channels to preload at startup. Channels are also created lazily on first request. |
| `BASE_PATH` | *(empty)* | Mount the app under a subpath (e.g. `/tg`) for reverse-proxying. No trailing slash. |

## Building from source

```bash
go build ./...
go test ./...
go run . # respects the same env vars
```
