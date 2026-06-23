# CHANGELOG

All changes to this fork are documented here. Each entry links to the relevant upstream issue or PR
and the commit that implements the fix.

---

## [Unreleased] — Milestone 0.2 in progress

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
