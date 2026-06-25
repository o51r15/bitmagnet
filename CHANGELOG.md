# CHANGELOG

All changes to this fork are documented here. Each entry links to the relevant upstream issue or PR
and the commit that implements the fix.

---

## [0.2.0] — 2026-06-25

All v1 stability milestones complete. This release addresses the remaining crash and lockup
patterns not covered by v0.1.0 — focusing on UDP connection safety, network recovery, queue
management, disk protection, and deployment reliability.

### Fixed

- **Global UDP query semaphore** (`e62c755`) — M0.3
  Added `MaxConcurrentQueries int64` (default 512) to the DHT server and a global weighted
  semaphore acquired before every `Query()` send and released on return. Without this, at
  `scaling_factor: 10` up to 1,010 goroutines could simultaneously block on UDP operations.
  The semaphore adds hard backpressure without touching channel sizing or pipeline architecture.

- **DHT network recovery — reseed interval wired from config** (`9757191`) — M0.4 part 1
  `ReseedBootstrapNodesInterval` was hardcoded to 10 minutes in `factory.go`, silently ignoring
  the config field. Now reads from `params.Config.ReseedBootstrapNodesInterval`. Config default
  is 1 minute, so the effective recovery window dropped from 10 minutes to 1 minute immediately.

- **DHT network recovery — ktable health monitor** (`9757191`) — M0.4 part 2
  Added `runKtableHealthMonitor` goroutine to `bootstrap.go`. Checks `GetNodesForSampleInfoHashes`
  every 10 seconds. Three consecutive empty results (30 seconds with no routing table nodes)
  triggers an emergency bootstrap reseed directly, without waiting for the interval timer.
  Addresses Issue [#359](https://github.com/bitmagnet-io/bitmagnet/issues/359).

- **Queue backpressure valve** (`b06a191`) — M0.5
  Added `MaxQueueDepth: 50000` config default and `runQueueDepthMonitor` goroutine that caches
  the pending+retry job count every 30 seconds. When depth exceeds the threshold,
  `flushHashesToClassify` skips adding new classification jobs until the queue drains. Torrents
  are still persisted to the DB — only queue insertion is held back. Prevents unbounded
  `queue_jobs` table growth under TMDB rate limits.

- **Padding file threshold counting** (`932fa52`)
  The file persistence loop used the raw torrent file index against `saveFilesThreshold`, causing
  BEP-47 padding files to consume threshold slots even when skipped. Now uses a separate
  `savedCount` counter. Original file index preserved for the `Index` field as it is
  spec-meaningful in BitTorrent.

### Added

- **Build date in UI version display** (`1db85f8`, `d8b8bb8`)
  Added `BuildTime` variable to `internal/version/version.go`, injected via `-ldflags` at CI
  build time. The GraphQL version resolver now returns `built YYYY-MM-DD` for fork builds instead
  of `v-unknown`. Falls back to `dev` for local builds without ldflags. GitTag is only returned
  if it looks like a real semver tag (starts with `v`) — branch names like `main` are ignored.

- **Disk usage guardian script** (`3048dd5`) — M0.6
  Added `scripts/disk-check.sh`. Checks disk usage of the Postgres data volume and stops the
  bitmagnet container at a configurable threshold (default 85%). Designed to run as a host cron
  job. Stops only the crawler — Postgres keeps running to avoid WAL corruption.
  Addresses Issue [#187](https://github.com/bitmagnet-io/bitmagnet/issues/187) /
  [#495](https://github.com/bitmagnet-io/bitmagnet/issues/495).

- **Portainer-compatible deployment** (`f16d921`)
  `docker-compose.yml` uses absolute volume paths and inline credential placeholders for
  straightforward Portainer stack deployment. No `.env` file required.

- **`config/config.yml`** with safe runtime defaults
  Documents and sets `scaling_factor: 1`, `save_files_threshold: 50`,
  `processor.concurrency: 2`, `log.level: warn`.

- **V2 enrichment roadmap** (`2bdb3ce`)
  Scoped Prowlarr integration (reactive hash lookup + proactive indexer crawling) and public
  tracker RSS polling as future V2 milestones. Work deferred until all v1 stability work complete.

### Infrastructure

- `README.md` expanded with Portainer deployment guide, Postgres tuning explanation,
  per-parameter rationale table, and config reference.
- `ROADMAP.md` updated with completed milestone status and full V2 feature scope.

---

## [0.1.0] — 2026-06-23

Initial stability release. Five fixes applied addressing the primary root causes of the 24-hour
crash/lockup cycle.

### Fixed

- **`queue_jobs` expression index** (`c4291d7`)
  The queue server's job-fetch query was forcing a full table scan and 54MB in-memory sort of all
  pending rows on every single worker poll due to an unindexable `ORDER BY (status = 'retry') DESC`
  expression. Added a partial expression index reducing query time from ~447ms to ~0.23ms (~2000x).
  Dropped the 678MB GIN index on `(queue, payload)` that had accumulated with zero reads.
  *Upstream: Issue [#496](https://github.com/bitmagnet-io/bitmagnet/issues/496)*

- **`StableBloomFilter` pointer embed** (`b67e253`)
  The `internal/bloom` wrapper embedded `boom.StableBloomFilter` by value, making it unsafe to copy.
  Database `Scan` operations could trigger a nil pointer panic. Changed to pointer embed.
  *Upstream: PR [#446](https://github.com/bitmagnet-io/bitmagnet/pull/446)*

- **DHT bootstrap node list** (`2699390`)
  Removed stale and intermittently unreliable bootstrap nodes (`dht.anacrolix.link`,
  `router.silotis.us`) that were causing frequent DNS resolution failures and slow cold-start
  routing table population. Added `router.bittorrent.cloud:42069` as a current replacement.
  *Upstream: PR [#454](https://github.com/bitmagnet-io/bitmagnet/pull/454)*

- **Exclude BEP-47 padding files** (`eae031e`)
  Synthetic `.pad/<size>` entries inserted by torrent clients for piece alignment were being saved
  as `torrent_files` rows. These carry no content metadata and inflated file counts against the
  `save_files_threshold` cap. Now skipped during persistence.
  *Upstream: PR [#458](https://github.com/bitmagnet-io/bitmagnet/pull/458)*

### Added

- **GitHub Actions CI workflow** (`9dc16bf`)
  Builds and pushes a multi-arch Docker image (`linux/amd64` + `linux/arm64`) to
  `ghcr.io/o51r15/bitmagnet:latest` on every push to `main`. Each commit also tagged with its
  short SHA for pinning.

- **Hardened `docker-compose.yml`** (`561797d`)
  Tuned for 8GB host: `1GB` bitmagnet limit, `2GB` Postgres limit with `shared_buffers=512MB`,
  `work_mem=8MB`, `max_connections=50`, aggressive autovacuum, SSD-optimised `random_page_cost`.

- **`ROADMAP.md`** — full diagnostic and attack plan for all known remaining issues

- **`CHANGELOG.md`** — this file

### Infrastructure

- `README.md` updated to document fork goals, all changes, and known remaining issues
