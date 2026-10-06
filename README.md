# V3X Exporter

A Go-based Prometheus exporter that scrapes user metrics from the [v3x.club](https://v3x.club) API and exposes them via the Prometheus HTTP interface.

It's entirely vibe coded: no elaborate build system, just straight Go code running as a simple HTTP service that:
- Authenticates with the V3X API using your credentials (cookie-based session)
- Scrapes `/auth/me` for upload/download bytes
- Exposes everything at `/metrics` for Prometheus to scrape

## Architecture

```
┌──────────────────┐     ┌──────────────┐     ┌─────────────┐
│ api.v3x.club     │────▶│ Exporter     │◀────│ Prometheus  │
│ (user data)      │     │ (scrapes &   │     │ (collects)  │
└──────────────────┘     │ exposes /    │     └─────────────┘
                         │ metrics      │
                         └──────────────┘
```

## Requirements

- Go 1.22+
- Docker (optional)

## Configuration

Create a `.env` file in the project root (see `.env.example`):

| Variable | Description | Default |
|----------|-------------|---------|
| `V3X_BASE_URL` | V3X API host | `https://api.v3x.club` |
| `V3X_USERNAME` | Username (or use `V3X_EMAIL`; the login field accepts either) | *required* |
| `V3X_EMAIL` | Email, used if `V3X_USERNAME` is empty | |
| `V3X_PASSWORD` | Password | *required* |
| `PORT` | Server listen port | `9090` |
| `METRICS_PATH` | Metrics endpoint path | `/metrics` |
| `SCRAPE_INTERVAL` | Informational (metrics are refreshed on each Prometheus scrape) | `5m` |

Two-factor authentication is not supported (disable it on the account used).

## Quick Start

```bash
# Local
export V3X_USERNAME=<user> V3X_PASSWORD=<pass>
go build -o v3x_exporter . && ./v3x_exporter

# Docker (dev, host port 9101 by default)
docker compose -f docker-compose.dev.yml up --build
```

## Endpoints

- `GET /metrics` - Prometheus metrics
- `GET /health` - Health check (`{"status":"healthy","authenticated":true}`)

```text
# TYPE v3x_total_uploaded_bytes gauge
v3x_total_uploaded_bytes 5.58255848392e+11
# TYPE v3x_total_downloaded_bytes gauge
v3x_total_downloaded_bytes 1.073741824e+09
```

## Prometheus Configuration

```yaml
scrape_configs:
  - job_name: 'v3x_exporter'
    static_configs:
      - targets: ['localhost:9090']
    metrics_path: /metrics
```

## Authentication Flow

1. `POST https://api.v3x.club/auth/login` with JSON `{"login": "...", "password": "...", "remember": true}` (no CSRF token)
2. The API sets a session cookie, kept in a cookie jar
3. `GET https://api.v3x.club/auth/me` returns `uploaded` and `downloaded` in bytes
4. On HTTP 401 the exporter logs in again automatically

No Cloudflare bypass needed: plain HTTP requests work.

## License

[MIT](./LICENSE)
