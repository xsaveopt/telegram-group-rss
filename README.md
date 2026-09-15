# telegram-group-rss

A small Go service that turns public Telegram channels into one RSS feed by scraping their web preview at t.me/s/{channel}.
It works for public channels only, since Telegram groups have no such preview page.

Each channel listed in CHANNELS is fetched at startup and then polled again every INTERVAL, keeping the newest MAX_MESSAGES posts per channel in memory.
The feed at / merges every channel's posts newest first, with photos inlined and each item's author set to the channel's display title so you can tell the sources apart.
History lives in memory, so after a restart the feed holds whatever the preview pages show on the first fetch.

## Running it

Images are published to ghcr.io/xsaveopt/telegram-group-rss for linux/amd64.
The latest tag follows the newest stable release, tags like 1, 1.2 and 1.2.3 pin a major, minor or patch line, and dev is rebuilt from every commit to main.
Pre-releases such as 1.2.3-rc1 get their version tag and never latest.

```sh
docker run --rm -p 8080:8080 \
  -e CHANNELS="{channel},{channel}" \
  ghcr.io/xsaveopt/telegram-group-rss:latest
```

With compose the same thing looks like this:

```yaml
services:
  telegram-group-rss:
    image: ghcr.io/xsaveopt/telegram-group-rss:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      CHANNELS: "{channel},{channel}"
```

To run it from a checkout, go run . reads the same environment variables, and .env.example lists them with sample values.

## Environment variables

| Var | Default | Purpose |
|---|---|---|
| `CHANNELS` | empty | Channels to watch, separated by commas, spaces or semicolons. Names are 4 to 32 letters, digits or underscores, and an invalid or unreachable one is logged and skipped. |
| `INTERVAL` | `5m` | How often each channel is polled, as a Go duration like `30s`, `2m` or `1h`. The minimum is `1s`. |
| `MAX_MESSAGES` | `100` | Posts kept in memory per channel. |
| `ADDR` | `:8080` | HTTP listen address. |
| `BASE_PATH` | empty | Subpath the app is served under, like `/tg`. Leading and trailing slashes are normalized. |

## Reverse proxy

With BASE_PATH set to /tg the app answers on /tg/ itself, so the proxy passes the path through unchanged:

```nginx
location /tg/ {
    proxy_pass http://{host}:8080;
}
```

## License

GPL-2.0, see LICENSE.
