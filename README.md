# telegram-group-rss

Scrapes the public web preview of a Telegram channel (`https://t.me/s/<channel>`) and serves it as an RSS feed. No API key, no bot, no auth — only works for **public channels** (Telegram groups don't have a `/s/` preview).

## Quick start

```bash
docker run --rm -p 8080:8080 \
  -e CHANNELS="durov,telegram" \
  -e INTERVAL=2m \
  ghcr.io/sratabix/telegram-group-rss:latest

curl http://localhost:8080/feed/durov
```

## Environment variables

| Var | Default | Description |
|---|---|---|
| `ADDR` | `:8080` | HTTP listen address |
| `INTERVAL` | `5m` | Poll interval per channel (Go duration: `30s`, `2m`, `1h`) |
| `MAX_MESSAGES` | `100` | Max messages kept in memory per channel |
| `CHANNELS` | *(empty)* | Comma / space / semicolon separated list of channels to preload at startup. Channels are also created lazily on first request. |
| `BASE_PATH` | *(empty)* | Mount the app under a subpath (e.g. `/tg`) for reverse-proxying. No trailing slash. |

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

## Notes

- Each channel has its own feed and its own watcher; many can be served concurrently — just hit `/feed/<channel>` for each, or list them in `CHANNELS` to preload.
- The RSS `<author>` on every item is set to the channel name/id (not the post's display name), so aggregating multiple feeds in one reader keeps the source identifiable.
- Storage is **in-memory only**. A restart loses history beyond what the next scrape returns (`t.me/s/` shows roughly the last 20 messages).
- Be polite with `INTERVAL`. Anything below ~`1m` is rarely useful and risks rate-limiting.
- Only public channels work. Public groups, private channels, and message threads are out of scope here — they'd need the Bot API or MTProto.

## Building from source

```bash
go build ./...
go test ./...
go run . # respects the same env vars
```
