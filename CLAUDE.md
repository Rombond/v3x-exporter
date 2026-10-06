# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## High-Level Summary
**Go-based Prometheus exporter** scraping user traffic from the v3x.club API (`https://api.v3x.club`, the site is a Next.js front end). Stack: Gin, Prometheus client, cookie jar. **No Cloudflare challenge** for the API: plain HTTP works (no FlareSolverr).

## Authentication
- `POST /auth/login` JSON `{"login": "<username or email>", "password": "...", "remember": true}`
- No CSRF token. The API sets a session cookie (HttpOnly), stored in an `http.CookieJar`.
- 2FA accounts answer `{twoFactorRequired, challenge}`: unsupported, returns an error.
- Auto-login at startup; on 401 from `/auth/me`, `refresh()` logs in again and retries once.

## Metrics
`GET /auth/me` returns `uploaded` and `downloaded` in **bytes** (no conversion). `downloaded` is the displayed (possibly credited) value; `downloadedReal` is ignored.

| Metric | Type |
|--------|------|
| `v3x_total_uploaded_bytes` | Gauge |
| `v3x_total_downloaded_bytes` | Gauge |

Gauges are refreshed on every `/metrics` request (like the sibling exporters).

## Configuration (`.env`)
```
V3X_BASE_URL    # default https://api.v3x.club
V3X_USERNAME    # or V3X_EMAIL
V3X_PASSWORD
PORT            # default 9090
METRICS_PATH    # default /metrics
SCRAPE_INTERVAL # default 5m
EXPORTER_PORT   # dev compose host port (9101)
```

## Commands
```bash
docker compose -f docker-compose.dev.yml up --build
curl http://localhost:9101/metrics
curl http://localhost:9101/health
docker compose -f docker-compose.dev.yml logs -f app
```

## Structure
```
v3x_exporter/
├── main.go  go.mod  go.sum
├── Dockerfile  docker-compose.yml  docker-compose.dev.yml
├── .env (gitignored)  .env.example  .gitignore  .dockerignore
└── .github/workflows/docker-build.yml
```
