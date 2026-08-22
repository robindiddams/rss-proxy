# rss-proxy

A standalone Go HTTP server that lets legacy podcast clients (iTunes 10.4 on
Mac OS X Snow Leopard) consume modern HTTPS podcast feeds over plain HTTP.
Runs on a trusted home network; not for the public internet.

## Development

Built and tested on Arch Linux with Go 1.26. Target deployment is a home LAN
serving an iMac running iTunes 10.4.

```sh
go build ./... && go vet ./... && go test ./...
go run ./cmd/rss-proxy -public-base-url http://localhost:8080 -listen :8080
```

The feed URL shape is `http://<host>/rss/untls?url=<encoded-https-feed>`.
Media URLs carry the upstream path as a suffix
(`http://<host>/media/untls/<path>?url=<encoded>`) so they end in the real
file extension — iTunes 10.4 determines file type from the URL extension.

`testdata/feed.xml` is a synthetic Megaphone-style fixture; tests do not
require internet.

## Usage

### Install / update (systemd service)

`install.sh` is idempotent and expects sudo. It builds the binary, installs
it to `/usr/local/bin/rss-proxy`, creates a dedicated service user, writes
`/etc/rss-proxy.env` and a hardened systemd unit, then starts/enables the
service. Re-run it after code changes or to change config — it only restarts
when something actually changed.

```sh
sudo ./install.sh                          # auto-detects LAN IP, listens on :80
sudo ./install.sh --public-base-url http://192.168.8.230
sudo ./install.sh --allow-hosts feeds.megaphone.fm,megaphone.imgix.net,traffic.megaphone.fm,pdst.fm
```

The service runs as user `rss-proxy` with `CAP_NET_BIND_SERVICE` (binds `:80`
without root). It survives reboots (`enable`d).

### Flags

All available as install.sh flags or `RSS_PROXY_*` env vars in
`/etc/rss-proxy.env`:

| flag | env | default | notes |
|------|-----|---------|-------|
| `--public-base-url` | `RSS_PROXY_PUBLIC_BASE_URL` | `http://<lan-ip>` | required in effect; must be `http` scheme |
| `--listen` | `RSS_PROXY_LISTEN` | `:80` | |
| `--allow-hosts` | `RSS_PROXY_ALLOW_HOSTS` | (none) | comma-separated; empty = any policy-compliant public host |
| `--feed-timeout` | `RSS_PROXY_FEED_TIMEOUT` | `15s` | |
| `--max-feed-bytes` | `RSS_PROXY_MAX_FEED_BYTES` | `4194304` | |
| `--binary` | — | | use a prebuilt binary instead of building |

Invalid env values fail at startup rather than silently defaulting.

### Endpoints

- `GET /` — web form to generate subscribe URLs from a browser.
- `GET /rss/untls?url=<https-feed>` — rewritten HTTP feed (HEAD == GET headers).
- `GET /media/untls/<path>?url=<https-url>` — streaming media proxy (Range, 304, 206, 416).
- `GET /healthz` — health check.

### Debugging

```sh
sudo journalctl -u rss-proxy -f          # live logs (one line per request)
sudo systemctl status rss-proxy
sudo cat /etc/rss-proxy.env               # current config
```

To test locally without installing, run directly and point a client at it:

```sh
go run ./cmd/rss-proxy -public-base-url http://localhost:8080 -listen :8080
curl 'http://localhost:8080/healthz'
curl 'http://localhost:8080/rss/untls?url=https%3A%2F%2Ffeeds.megaphone.fm%2FTBIEA9794787572'
```

For local testing against loopback/HTTP upstreams, the binary supports
`--allow-private-ips` and `--allow-http-upstream` test switches (not exposed
by install.sh; pass them via env or run the binary directly).
