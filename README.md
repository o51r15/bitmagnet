# bitmagnet — Stability Fork

> **This is a community stability fork of [bitmagnet-io/bitmagnet](https://github.com/bitmagnet-io/bitmagnet).**
> It is not affiliated with the upstream project. The goal is to apply well-documented but unmerged fixes
> that address real-world crash and lockup behavior while the upstream maintainer works toward larger architectural changes.

---

## What is bitmagnet?

A self-hosted BitTorrent indexer, DHT crawler, content classifier and torrent search engine with web UI, GraphQL API and Servarr stack integration.

Visit the upstream website at [bitmagnet.io](https://bitmagnet.io).

---

## Why this fork?

The upstream project is alpha software under active development. Several users — including contributors on
this fork — have documented a pattern of full system lockups when running bitmagnet continuously for 24+ hours.
The symptoms are consistent across different hardware:

- The process appears healthy but stops indexing new torrents
- Memory usage climbs steadily until the host OOM-kills the container or the whole machine locks up
- Restarting the container recovers crawl activity temporarily, then the cycle repeats

After a detailed review of open GitHub issues and the codebase, four root causes were identified
that have documented fixes available but not yet merged upstream.

---

## Goals of this fork

1. **Stability first** — apply all well-evidenced, low-risk fixes that address the crash/lockup cycle before adding any new features
2. **Stay close to upstream** — track `bitmagnet-io/bitmagnet:main` and rebase as upstream progresses
3. **Automated builds** — publish a Docker image to GHCR on every push to `main` so the fork is always deployable without building locally
4. **Document every change** — each fix is a separate commit with a clear reference to the issue or PR it addresses

---

## Deployment

### Pre-flight — create directories and config on the host

Before deploying, create the required directories and drop in the runtime config:

```bash
mkdir -p /home/o51r15/docker/bitmagnet/config
mkdir -p /home/o51r15/docker/bitmagnet/data/postgres

cat > /home/o51r15/docker/bitmagnet/config/config.yml << 'EOF'
dht_crawler:
  scaling_factor: 1
  save_files_threshold: 50
  save_pieces: false
  rescrape_threshold: 720h

processor:
  concurrency: 2

tmdb:
  enabled: true

log:
  level: warn
  file_rotator:
    enabled: false
EOF
```

---

### Deploying via Portainer

The compose file uses `${VAR}` substitution for secrets. Portainer supports this natively
through its **Environment variables** section — no `.env` file needed on disk.

**Steps:**

1. In Portainer, go to **Stacks → Add stack**
2. Paste the contents of `docker-compose.yml` into the compose editor
3. Scroll down to the **Environment variables** section
4. Add the following two variables:

| Name | Value |
|---|---|
| `TMDB_API_KEY` | your TMDB API key |
| `POSTGRES_PASSWORD` | your chosen Postgres password |

5. Click **Deploy the stack**

The volume paths in the compose are absolute (`/home/o51r15/docker/bitmagnet/...`) so Portainer
resolves them correctly to your host filesystem rather than its own internal working directory.

---

### Deploying via Docker Compose CLI

```bash
git clone https://github.com/o51r15/bitmagnet.git
cd bitmagnet
cp .env.example .env
# Edit .env and fill in TMDB_API_KEY and POSTGRES_PASSWORD
docker compose up -d
```

`.env` is listed in `.gitignore` and will never be committed.

---

### Postgres tuning — no custom image required

The `docker-compose.yml` includes a full set of Postgres performance tunings targeted
at a write-heavy DHT crawl workload on SSD. These are applied via Postgres's standard
`-c key=value` command-line flags in the compose `command:` block — equivalent to editing
`postgresql.conf` but without touching the image. The image remains unmodified vanilla
`postgres:16-alpine`. No fork, no custom build, no extra maintenance surface.

Any user pulling this compose file gets all tunings automatically on deploy.

The current settings are calibrated for an **8GB host**:

| Parameter | Value | Reason |
|---|---|---|
| `shared_buffers` | 512MB | ~25% of the 2GB container limit — the recommended ratio |
| `effective_cache_size` | 1536MB | ~75% of container limit; guides the query planner |
| `work_mem` | 8MB | 50 connections × 8MB = 400MB worst case |
| `maintenance_work_mem` | 64MB | Used by VACUUM and index builds |
| `max_connections` | 50 | Prevents connection storm memory spikes |
| `wal_buffers` | 16MB | Smooths write flushing under bursty crawl load |
| `checkpoint_completion_target` | 0.9 | Spreads checkpoint I/O over more time |
| `max_wal_size` | 1GB | Limits WAL growth between checkpoints |
| `random_page_cost` | 1.1 | Tells the planner random I/O is cheap — correct for SSD |
| `autovacuum_vacuum_scale_factor` | 0.05 | Runs VACUUM earlier; critical for tables with millions of daily inserts |
| `autovacuum_analyze_scale_factor` | 0.025 | Keeps statistics fresh for the query planner |
| `autovacuum_vacuum_cost_delay` | 2ms | More aggressive autovacuum I/O budget |
| `autovacuum_naptime` | 20s | Checks tables for vacuuming more frequently |
| `autovacuum_max_workers` | 2 | Two background vacuum workers |

If your host has more RAM, raise `shared_buffers`, `effective_cache_size`, and `work_mem`
proportionally. The 25%/75% ratio rules stay the same regardless of total RAM.

---

### bitmagnet runtime config

`config/config.yml` is mounted into the container and sets safe runtime defaults:

| Setting | Value | Reason |
|---|---|---|
| `dht_crawler.scaling_factor` | 1 | Keeps goroutine count at ~101; raise after M0.3 lands |
| `dht_crawler.save_files_threshold` | 50 | Limits per-torrent DB write storms |
| `processor.concurrency` | 2 | Safe post queue index fix; doubles queue drain throughput |
| `log.level` | warn | Reduces log I/O under sustained crawl load |

---

## Changes from upstream

All changes are applied as individual commits for easy review and potential upstream submission.

### `fix(db)`: queue_jobs expression index + drop unused GIN index
**Commit:** `c4291d7` | **Upstream issue:** [#496](https://github.com/bitmagnet-io/bitmagnet/issues/496)

The queue server's job-fetch query orders results by `(status = 'retry') DESC` — a computed boolean expression
that Postgres cannot satisfy with a standard B-tree index. At any meaningful queue depth this forces a full
table scan and an in-memory sort of every pending row on every single poll across every worker.
At 60k pending jobs this measured at ~447ms and ~54MB RAM per query in the upstream issue benchmark.
Multiplied across concurrent workers, this is the primary source of DB memory pressure that drives the crash cycle.

Additionally, migration `00012` created a GIN index on `(queue, payload)` that has accumulated hundreds of
megabytes while recording zero index scans. Every torrent insert, update, and delete pays maintenance cost
for this index with no read benefit.

**Fix:** New migration `00021` adds a partial expression index that reduces the query from ~447ms to ~0.23ms
(~2000x improvement per the upstream benchmark), and drops the dead GIN index entirely.

---

### `fix(bloom)`: StableBloomFilter pointer embed
**Commit:** `b67e253` | **Upstream PR:** [#446](https://github.com/bitmagnet-io/bitmagnet/pull/446)

The `internal/bloom` package wraps the upstream `boom.StableBloomFilter` type with an embedded value rather
than a pointer. The upstream type is not safe to copy by value — doing so can produce nil pointer panics
during database `Scan` operations when deserialising bloom filter state. The PR author confirmed this resolved
a nil pointer exception they had been experiencing.

**Fix:** Changed the struct embed from `boom.StableBloomFilter` to `*boom.StableBloomFilter` and updated
`NewDefaultStableBloomFilter` and `Scan` accordingly.

---

### `fix(dhtcrawler)`: update default bootstrap nodes
**Commit:** `2699390` | **Upstream PR:** [#454](https://github.com/bitmagnet-io/bitmagnet/pull/454)

Several of the hardcoded DHT bootstrap nodes in `config.go` are stale or intermittently unreliable.
`dht.anacrolix.link` in particular resolves to a CNAME that frequently returns NXDOMAIN, producing
a flood of `failed to resolve bootstrap node address` warnings in logs and causing the crawler's routing
table to start empty on cold boot. A poorly seeded routing table means the crawler takes much longer
to start discovering peers and may never fully populate under low-resource conditions.

**Fix:** Removed stale/unreliable nodes, added `router.bittorrent.cloud:42069` as a current reliable replacement.

---

### `fix(dhtcrawler)`: exclude BEP-47 padding files
**Commit:** `eae031e` | **Upstream PR:** [#458](https://github.com/bitmagnet-io/bitmagnet/pull/458)

Torrent clients insert synthetic padding files (`.pad/<size>`) into multi-file torrents to align file data
on piece boundaries per BEP-47. These entries carry no useful content metadata but are saved to `torrent_files`
rows along with real files. On torrents with many padding entries this inflates file counts against the
`save_files_threshold` cap and wastes database rows. Over millions of torrents this adds measurable table bloat.

**Fix:** Added a path prefix check in `persist.go` to skip any file whose display path begins with `.pad/`.

---

### `ci`: GitHub Actions build workflow
**Commit:** `9dc16bf`

Added `.github/workflows/stability-build.yml` which builds a multi-arch Docker image
(`linux/amd64` + `linux/arm64`) and pushes it to GHCR on every push to `main`.

The image is published as:
```
ghcr.io/o51r15/bitmagnet:latest
```

Each push also produces a short-SHA tag (e.g. `ghcr.io/o51r15/bitmagnet:sha-9dc16bf`) for pinning
to a specific build if needed.

---

## Known remaining issues

The following upstream issues are tracked but not yet addressed in this fork:

- **[#359](https://github.com/bitmagnet-io/bitmagnet/issues/359)** — DHT crawler enters an unrecoverable
  state after network interruption. No self-healing mechanism exists. Workaround: `restart: unless-stopped`
  in your compose file with a container-level memory limit so Docker restarts the container cleanly rather
  than the host OOM-killing it.
- **[#462](https://github.com/bitmagnet-io/bitmagnet/issues/462)** — `failed to hydrateHasOne` errors
  under concurrent search load. Architectural limitation acknowledged upstream; OpenSearch integration
  planned for a future release.
- **No backpressure between DHT crawler and queue processor** — the crawler pushes hashes into `queue_jobs`
  regardless of queue depth. If the processor is slower than the crawler (which it always will be under TMDB
  load), the table grows unboundedly. Mitigated but not eliminated by the index fix above.

---

## Relationship to upstream

This fork rebases against `bitmagnet-io/bitmagnet:main` periodically. All changes are intended to be
upstreamable. If any of these fixes are merged upstream, the corresponding commits will be dropped from this
fork on the next rebase to avoid drift.

Upstream repository: https://github.com/bitmagnet-io/bitmagnet
Upstream website: https://bitmagnet.io
