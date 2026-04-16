# SyncedApp

SyncedApp is a lightweight Go reverse proxy in front of Prometheus.

It rewrites and splits selected query requests using job metadata from Couchbase, overwrites series timestamps for easier overlap ability, and can cache split results in Memcached.

## What It Does

- Proxies requests from a configured prefix (default: `/prometheus/`) to Prometheus.
- Applies special handling only for:
  - `/api/v1/query`
  - `/api/v1/query_range`
- Extracts job IDs from PromQL (`job="..."` or `job=~"id1|id2|..."`).
- Looks up `ts_start` and `ts_end` per job in Couchbase.
- Rewrites query timeframe (`start`, `end`) to match job metadata.
- Normalizes returned timestamps so each job timeline starts at `0`, making series easy to overlay and compare.
- Splits multi-job requests into per-job upstream calls and merges the results.
- Optionally caches split results in Memcached.

## Request Behavior (High Level)

- Non-GET requests: pass through.
- GET requests not targeting `/api/v1/query` or `/api/v1/query_range`: pass through.
- `/api/v1/query` with `time=...`: converted to `/api/v1/query_range` passthrough mode.
- `/api/v1/query_range` and converted instant queries:
  - If no job filter is found: pass through.
  - If one job is found: timeframe is rewritten from Couchbase.
  - If multiple jobs are found: request is split, fan-out executed, then merged.
  - For non-passthrough range handling, timestamps are shifted by each job's `ts_start` so results begin at `0`.

## Requirements

- Go 1.24+
- Reachable Prometheus endpoint
- Reachable Couchbase cluster/bucket containing metadata documents
- Optional Memcached for query result caching

## Configuration

SyncedApp reads configuration from `.env` in the working directory.

Required:

- `COUCHBASE_CONNECTION_STRING`
- `USERNAME`
- `PASSWORD`
- `METADATA_BUCKET_NAME`
- `PROMETHEUS_CONNECTION_STRING`

Optional (with defaults):

- `LISTEN_ADDR` (default `:5000`)
- `PROMETHEUS_PROXY_PREFIX` (default `/prometheus/`)
- `MEMCACHED_ENABLED` (default `false`)
- `MEMCACHED_ADDRS` (default `localhost:11211`)
- `MEMCACHED_TTL_SECONDS` (default `300`)
- `MEMCACHED_TIMEOUT_MS` (default `100`)
- `MEMCACHED_MAX_ITEM_BYTES` (default `900000`)
- `LOG_CACHE` (default `true`)
- `LOG_TIMESERIES` (default `false`)
- `LOG_SPLITTING` (default `false`)

## Run Locally

1. Create or update `.env` with required values.
2. Start SyncedApp:

```bash
go run .
```

3. Send requests through the proxy prefix, for example:

```bash
curl "http://localhost:5000/prometheus/api/v1/query_range?query=up"
```

## Run With Docker Compose

1. Put the required Couchbase and Prometheus settings in `.env` at the project root.
2. Start the stack from the project root:

```bash
docker compose -f deployment/compose.yml up --build
```

3. SyncedApp will start after the memcached container is started.

Notes:

- The compose file lives under `deployment/`, but builds from the project root.
- The compose file forces `MEMCACHED_ENABLED=true`.
- Inside Docker, SyncedApp uses `syncedapp-memcached:11211` instead of `localhost:11211`.
- The app is exposed on `http://localhost:5000`.

## Notes on Caching and Concurrency

- Split result caching is key-based per job and query params.
- Cross-request singleflight deduplicates concurrent misses for the same cache key.
- Shared upstream fetches run with a detached, bounded context to avoid one canceled caller canceling all waiters.

## Notes on Grafana Scenes Integration
- Can be used as a Grafana Prometheus Datasource inside Dashboards built with Grafana Scenes
- Panels' time field should be overriden to start from 0 and end at the longest duration between the overlapped series 
- Doesn't require a decoupling between QueryRunner time range and panel time range since the proxy overwrites query time range with metadata correct timeframe
