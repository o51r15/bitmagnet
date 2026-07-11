# ROADMAP

This document tracks all known stability issues, planned fixes, and their current status.
Items are grouped by milestone. Each entry references the upstream issue or PR where applicable.

---

## Milestone 0.1 — Baseline Stability ✅ COMPLETE

Foundation fixes applied to the fork. All commits are on `main`.

| # | Fix | Source | Commit |
|---|---|---|---|
| ✅ | `queue_jobs` expression index + drop 678MB dead GIN index | Issue [#496](https://github.com/bitmagnet-io/bitmagnet/issues/496) | `c4291d7` |
| ✅ | `StableBloomFilter` pointer embed — nil panic prevention | PR [#446](https://github.com/bitmagnet-io/bitmagnet/pull/446) | `b67e253` |
| ✅ | Updated DHT bootstrap nodes, removed stale/unreliable entries | PR [#454](https://github.com/bitmagnet-io/bitmagnet/pull/454) | `2699390` |
| ✅ | Exclude BEP-47 `.pad/` padding files from DB persistence | PR [#458](https://github.com/bitmagnet-io/bitmagnet/pull/458) | `eae031e` |
| ✅ | GitHub Actions CI — builds and pushes `ghcr.io/o51r15/bitmagnet:latest` on push to main | New | `9dc16bf` |
| ✅ | Docker Compose tuned for 8GB host with memory limits and Postgres optimisation | New | `561797d` |

---

## Milestone 0.2 — Quick Wins (Config / Compose Level) — COMPLETE (6c6016d)

Low-risk, no Go code required. Estimated effort: 1 session.

| # | Fix | Source | Status |
|---|---|---|---|
| ✅ | Add `TZ=UTC` to compose — fixes misleading "last torrent found N hours ago" display | Issue [#485](https://github.com/bitmagnet-io/bitmagnet/issues/485) | Done (6c6016d) |
| ✅ | Add `ulimits: nofile: 65535` to bitmagnet container — raises fd limit to prevent silent UDP socket exhaustion | Issue [#348](https://github.com/bitmagnet-io/bitmagnet/issues/348) | Done (6c6016d) |
| ✅ | Add `processor.concurrency: 2` to `config/config.yml` — now safe post index-fix; improves queue drain rate | Upstream FAQ + our fix | Done (6c6016d) |
| ✅ | Add `config/config.yml` to the repo with documented safe defaults | Deployment hygiene | Done (6c6016d) |

---

## Milestone 0.3 — UDP Connection Cap (Go Code) — COMPLETE (e62c755)

**Priority: High. Root cause of network saturation and the most widely reported community complaint.**

### Diagnosis

The DHT server uses a **single raw UDP socket** (one file descriptor) that sends to many remote addresses
via `unix.Sendto`. This is *not* the fd exhaustion path — the socket itself is one fd.

The actual exhaustion mechanism is different and more subtle:

The `BufferedConcurrentChannel` pipeline feeds work to goroutines via a semaphore
(`semaphore.NewWeighted(concurrency)`). At `scaling_factor: 10`, the channels are sized like this in `factory.go`:

```
nodesForPing:             capacity=10,  concurrency=10
nodesForFindNode:         capacity=100, concurrency=100
nodesForSampleInfoHashes: capacity=100, concurrency=100
getPeers:                 capacity=100, concurrency=200
scrape:                   capacity=100, concurrency=200
requestMetaInfo:          capacity=100, concurrency=400
```

Each concurrent goroutine calls `client.Query()` which blocks on a UDP send + timeout wait (4 seconds
default). **At scaling_factor 10 there can be up to 1,010 goroutines simultaneously blocked on UDP
operations.** Each goroutine holds OS stack memory (~8KB initial, grows), and each pending query holds
a `chan dht.RecvMsg` entry in the `queries` map.

The `queryLimiter` in `factory.go` does apply a per-IP rate limit:
```go
concurrency.NewKeyedLimiter(rate.Every(time.Second), 4, 1000, time.Second*20)
```
This limits to 4 requests/second per unique IP with a burst of 1, and a key TTL of 20s. But the key space
is the **remote IP**, not a global cap — so 1,000 different IPs can each be queried 4 times/second
simultaneously with no global ceiling.

**The missing piece is a global concurrent query cap.** There is no semaphore or token bucket across
the whole server that bounds total simultaneous in-flight UDP operations. At high scaling factors this
produces thousands of simultaneous kernel-level `sendto` + timer goroutines.

### Attack Plan

1. Add `MaxConcurrentQueries int` to `server.Config` (default 512, configurable via YAML)
2. Add a `semaphore.NewWeighted(MaxConcurrentQueries)` to `server.server`
3. Acquire the semaphore before `s.send()` in `Query()`, release on return
4. Expose as `dht_server.max_concurrent_queries` in config
5. Document the relationship between `scaling_factor` and this value

This is a **targeted, non-breaking change** — it adds backpressure without touching channel sizing
or the pipeline architecture. A safe default of 512 reduces peak goroutine count by ~50% at
scaling_factor 10, and by ~98% at scaling_factor 1 (where we only need ~10 concurrent queries).

---

## Milestone 0.4 — DHT Crawler Network Recovery (Go Code) — COMPLETE (9757191)

**Priority: High. Root cause of the 24-hour unrecoverable hang (Issue #359).**

### Diagnosis

When the network drops, all in-flight `client.Query()` calls return `context deadline exceeded`.
The pipeline workers correctly drop nodes from the ktable on failure. The problem is what happens
**after** the network recovers.

The crawler has a bootstrap reseeding loop (`bootstrap.go`) that runs every 10 minutes
(`reseedBootstrapNodesInterval`). Bootstrap nodes are added to `nodesForPing`. If a ping
succeeds, the node is added to the ktable. `getNodesForFindNode` pulls nodes from the ktable
every second.

The failure mode is:

1. Network drops. All queries timeout. Ktable empties as nodes are dropped.
2. Bootstrap reseeder runs. Pings the bootstrap nodes.
3. **If the network recovers while pings are in flight, they succeed and the ktable repopulates.** ✅
4. **If the network is still down when the bootstrap reseeder runs, pings fail.** The ktable stays
   empty. The reseeder then waits 10 minutes before trying again.
5. During those 10 minutes, `getNodesForFindNode` finds no nodes. `getNodesForSampleInfoHashes`
   finds no nodes. **All pipeline channels go idle.** No new work enters the system.
6. After 10 minutes the reseeder fires again. If the network is up, it recovers. If not, another
   10-minute wait. Each outage lasting just past a reseeder window doubles the recovery time.

The **unrecoverable** case (issue #359) occurs when the network outage coincides with a reseeder
interval boundary and the process state becomes internally inconsistent — likely a goroutine
blocking forever on a channel send with no receiver. The `stopped` channel is the only graceful
exit path; if a goroutine misses context cancellation it will leak.

### Attack Plan

1. **Reduce `reseedBootstrapNodesInterval` from 10 minutes to 60 seconds** — reduces maximum
   recovery window from 10 minutes to 1 minute. Low risk, one config change.
2. **Add an active health check** — a separate goroutine that monitors the ktable node count.
   If it drops below a threshold (e.g. 10 nodes) for more than 30 seconds, it proactively triggers
   a bootstrap reseed regardless of the interval timer. This is the real fix.
3. **Add ktable node count to Prometheus metrics** — makes the recovery cycle observable so we
   can verify the fix is working.

The health check goroutine pattern is already used elsewhere in the codebase (see
`server/health_collector.go`). This is a well-understood pattern here.

---

## Milestone 0.5 — Queue Backpressure (Go Code) — COMPLETE (b06a191)

**Priority: Medium. Prevents the queue from growing unboundedly under TMDB load.**

### Diagnosis

The DHT crawler (`persist.go`) writes hashes to `queue_jobs` via the database directly, inside
`runPersistTorrents`. There is no check on the current queue depth before inserting. The queue
processor runs at `processor.concurrency: 1` (default) and is rate-limited to 1 TMDB request/second
(without a personal key). The crawler can discover and persist hundreds of new hashes per minute.

Over 24 hours this creates a `queue_jobs` table with potentially millions of pending rows. Even
with our index fix (milestone 0.1), very large queue depths create several secondary problems:

- The expression index is a partial index (`WHERE status IN ('pending','retry')`). Its selectivity
  drops as the pending set grows. At ~500k rows it's still fast, but the table scan cost of index
  maintenance on every insert increases linearly.
- The `purge_jobs` background task (in `queue/manager/purge_jobs.go`) runs periodically to clean
  processed jobs. With millions of processed rows it holds a long-running DELETE that contends
  with the crawler's inserts.
- VACUUM cannot reclaim space from dead rows fast enough if the insertion rate exceeds the
  autovacuum budget, causing table bloat that eventually degrades all queries.

### Attack Plan

1. **Add a queue depth check before persisting** — read the current pending job count from a
   cached counter (updated every 30 seconds). If depth exceeds a configurable threshold
   (e.g. `queue.max_pending: 50000`), skip the DB insert and discard the hash.
2. **This is effectively a backpressure valve** — when the queue is healthy the crawler runs at
   full speed; when the queue backs up the crawler throttles automatically.
3. **Alternative (simpler, less code)**: Add a `queue_server.max_pending_jobs` config key that
   the queue server checks before starting new workers. Already partially supported by the
   queue architecture.

---

## Milestone 0.6 — DB Size Guardrails (Config Level) — COMPLETE (3048dd5)

**Priority: Low-Medium. Prevents disk exhaustion killing Postgres non-gracefully.**

Issue [#187](https://github.com/bitmagnet-io/bitmagnet/issues/187) /
Issue [#495](https://github.com/bitmagnet-io/bitmagnet/issues/495)

No upstream fix exists. Adding a compose-level disk usage healthcheck that pauses the DHT crawler
container if the Postgres volume exceeds a configured threshold. This does not require Go code
changes — it can be implemented as a Docker healthcheck script and documented clearly.

---

## Deferred / Upstream Dependency

| Issue | Reason deferred |
|---|---|
| `failed to hydrateHasOne` search errors ([#462](https://github.com/bitmagnet-io/bitmagnet/issues/462)) | Architectural limitation — requires OpenSearch integration planned by upstream |
| IPv6 support ([#405](https://github.com/bitmagnet-io/bitmagnet/pull/405)) | Pending upstream design decision on dual-stack vs IPv6-only |
| Disk size limit config key ([#495](https://github.com/bitmagnet-io/bitmagnet/issues/495)) | Partially addressed by milestone 0.6 guardrails; full feature is upstream scope |


---

---

---

## V2 Milestone 1 — Prowlarr Crawler (Scoped)

> **Status: PARTIALLY COMPLETE** — backend crawler live on Pi and Optiplex.
> Angular UI page (Prowlarr nav link + indexer cards) deployed.
> GraphQL and Crawl Now button deferred to V2.2. Security/correctness fixes tracked in V2.1.
> Design: DEVLOG Sessions 5, 6, 9. Implementation: Sessions 10, 12. Code review: Session 13.

### What it does

Connects bitmagnet to a running Prowlarr instance. User selects which indexers to crawl
and which categories to pull from each. Crawled torrents are added to the main DB.
Metadata from the indexer only fills empty fields — existing data is never overwritten.
A new Prowlarr UI page shows per-indexer torrent lists and allows on-demand crawl triggers.

### Config

```yaml
prowlarr:
  url: http://prowlarr:9696
  api_key: your_key_here
  indexers:
    - id: 12
      name: "The Pirate Bay"
      enabled: true
      schedule: "0 */6 * * *"
      categories:
        - Movies
        - TV
```

### Scope (v1 of this feature)

- Prowlarr client package with indexer discovery and search API calls
- Per-indexer category selection using human-readable names (Movies, TV, Audio etc.)
- Scheduled crawl and on-demand crawl via UI button
- Upsert into main torrents table with source tracking (indexer name in torrents_torrent_source)
- Metadata merge: fill gaps only, never overwrite existing identified data
- ~~Skip TMDB classification for Prowlarr-sourced torrents~~ (removed — Prowlarr torrents now get full TMDB+OMDb classification, commit `a0dddc9`)
- Most recent page of results per run
- New Prowlarr UI page: indexer dropdown, filtered torrent list, crawl now button

### Deferred to future iteration

- Metadata priority config (who wins on conflict: DHT vs indexer vs TMDB)
- Pagination depth options and backfill on first run
- Sort by seeders / exclude dead torrents (0 seeders)
- ~~TMDB enrichment option for Prowlarr-sourced torrents~~ — DONE (commit `a0dddc9`)
- Per-indexer crawl stats in UI

### Implementation components

| Component | Location | Status |
|---|---|---|
| Prowlarr API client | `internal/prowlarr/client.go` | ✅ Done — hardening fixes in V2.1 |
| Config struct | `internal/prowlarr/config.go` | ✅ Done |
| Crawl worker | `internal/prowlarr/crawler.go` | ✅ Done — correctness fixes in V2.1 |
| fx wiring | `internal/prowlarr/factory.go` | ✅ Done |
| Worker key | `prowlarr_crawler` | ✅ Done (adec8a7) |
| ClassifierFlags in importer | `internal/importer/importer.go` | ✅ Done (fbae238) |
| Migration 00023 source index | `migrations/00023_prowlarr_source_index.sql` | ✅ Done (43d5067) |
| Migration 00024 indexer state | `migrations/00024_prowlarr_indexer_state.sql` | ✅ Done |
| UI route | `/prowlarr` | ✅ Done |
| UI component | `webui/.../prowlarr/` | ✅ Done (067bcbb) |
| GraphQL query + Crawl Now mutation | `graphql/schema/prowlarr.graphqls` | 🟥 Deferred → V2.2 |



---

## V2.1 — Prowlarr Security & Correctness Hardening

> **Status: PLANNED** — 4 commits, all self-contained. Findings from Session 13 code review.
> Do before adding more indexers to any deployment.

### Commit A — client.go hardening (RED — security)

| # | File | Fix |
|---|---|---|
| 1 | `prowlarr/client.go:67` | Move API key from URL query param to `X-Api-Key` header. Refactor `get()` to build `*http.Request`. Prevents key appearing in Prowlarr/proxy access logs. |
| 2 | `prowlarr/client.go:75` | Wrap `io.ReadAll` with `io.LimitReader(resp.Body, 32MB)`. Prevents memory exhaustion from large/adversarial responses. |
| 3 | `ci.Dockerfile:39` | Pin `FROM alpine:latest` → `FROM alpine:3.20`. Prevents non-reproducible builds. Matches local Dockerfile. |

### Commit B — crawler.go correctness (ORANGE)

| # | File | Fix |
|---|---|---|
| 4 | `prowlarr/crawler.go:32` | Add startup retry loop: if `getIndexers()` fails, retry every 60s. Currently crawler dies permanently if Prowlarr is down at startup. |
| 5 | `prowlarr/crawler.go:59` | On-demand trigger passes `name=""` and `categories=nil`. Add `indexerMeta` map on struct, populated at startup, looked up in trigger handler. |
| 6 | `prowlarr/crawler.go:130` | `uint(r.Size)` silently overflows on negative values. Add `if r.Size > 0` guard. |

### Commit C — misc housekeeping (ORANGE + YELLOW)

| # | File | Fix |
|---|---|---|
| 7 | `httpserver/config.go:59` | `CORS Debug: true` in default config logs every request to stderr. Change to `false`. |
| 8 | `scripts/disk-check.sh:36` | `docker stop` uses 10s grace period, returns 0 even when container wasn't running. Change to `docker stop -t 30` + `docker inspect` state confirm. |
| 9 | `.github/workflows/codeql.yml` | Deprecated `@v2` CodeQL actions → `@v3`. |
| 10 | `scripts/disk-check.sh:18` | Hardcoded deployment paths. Add `BITMAGNET_DATA_DIR` and `BITMAGNET_CONTAINER_NAME` env-var overrides. |
| lint | `prowlarr.component.html` | Fix prettier lint warning (CI noise, non-blocking). |

### Awareness items — no code change needed

| # | Note |
|---|---|
| 11 | GraphQL introspection + playground always on. Fine for home server; flag if port 3333 ever goes public. |
| 12 | `dhtcrawler/factory.go` — inherited `// todo: Fix! //nolint:contextcheck` upstream debt. |
| 13 | `internal/lazy/lazy.go` — error caching on failed lazy init is by-design. Document in deployment notes. |


---

## V2.2 — Prowlarr Completion

> **Status: PLANNED** — blocked on gqlgen execution problem. Unlocks Crawl Now button.

### GraphQL additions

gqlgen does not run in CI or via Desktop Commander (Go not in PATH).
Options:
- Run via Docker: `docker run --rm -v <repo>:/src -w /src golang:1.23.6 go run github.com/99designs/gqlgen generate`
- Manually edit `internal/gql/gql.gen.go` (24k lines — risky)

New schema file `graphql/schema/prowlarr.graphqls`:
```graphql
type ProwlarrQuery {
  indexers: [ProwlarrIndexer!]!
}
type ProwlarrIndexer {
  id:            Int!
  name:          String!
  enabled:       Boolean!
  lastCrawledAt: DateTime
  torrentCount:  Int!
}
type ProwlarrMutation {
  crawl(indexerIds: [Int!]!): Void
}
```
Root additions: `prowlarr: ProwlarrQuery!` and `prowlarr: ProwlarrMutation!`

`crawl` mutation sends indexer IDs to `CrawlNowFn` (already exported from prowlarr/factory.go).
`torrentCount` resolved by COUNT on `torrents_torrent_sources WHERE source = 'prowlarr-<id>'`.

### Per-indexer crawl interval

`IndexerConfig.Interval` was removed (e7a784f) because mapstructure has no
`StringToTimeDurationHookFunc` for fields inside a `[]struct` slice.

Fix: add hook to `internal/config/config.go` decoder:
```go
mapstructure.ComposeDecodeHookFunc(
    mapstructure.StringToTimeDurationHookFunc(),
    // ... existing hooks
)
```
Then restore `Interval time.Duration` to `IndexerConfig`. All indexers currently use `defaultCrawlInterval = 1h`.

### Startup hash compatibility check

At startup, fire a limit=1 test search per configured indexer. If no `infoHash` is returned,
log `WARN` and skip that indexer entirely. Prevents silent zero-import crawls.
Confirmed broken indexers from live testing (Session 10): Torrenting (id:70), LimeTorrents (id:15).

---

# V2 — Enrichment and Coverage Expansion

> Work begins only after all v1 stability milestones are complete.
> These are planned features, not fixes.

---

## V2 Overview

The v1 fork addresses crash and lockup stability. V2 addresses data quality and coverage.
Bitmagnet currently identifies content via TMDB alone, which produces poor results for
content outside mainstream movies and TV — software, games, scene releases, foreign content,
and anything TMDB doesn't index well. V2 introduces two new enrichment pipelines that
dramatically improve identification quality and index completeness.

---

## V2 Road 1 — Prowlarr-Backed Hash Lookup (Reactive Enrichment)

**Goal:** When the classifier cannot identify a torrent via TMDB, query Prowlarr as a
fallback to cross-reference the hash or parsed title against configured indexers.

### How it works

Prowlarr acts as a broker — it abstracts per-site scraping, authentication, rate limiting,
and result normalisation across hundreds of indexers. Bitmagnet already exposes itself as a
Torznab endpoint for Sonarr/Radarr. This runs in reverse: bitmagnet queries Prowlarr's
Torznab/Newznab API with a hash or title and receives enriched metadata back.

### Config design

```yaml
prowlarr:
  url: http://prowlarr:9696
  api_key: your_key_here

  # lookup_indexers: queried reactively when the classifier cannot identify
  # a torrent via TMDB or local search. Should be high-quality curated sources
  # where metadata is trustworthy. Rate limits apply — use sparingly.
  lookup_indexers:
    - 56   # e.g. BTN
    - 78   # e.g. PTP
```

### Key implementation details

- Lookup is triggered only after TMDB and local search have both failed
- Results are cached — a successful lookup is stored; an empty result is also stored
  so the same hash is never re-queried
- A configurable throttle prevents hammering rate-limited indexers
- Indexer IDs are passed directly to Prowlarr's API — no per-site logic in bitmagnet

---

## V2 Road 2 — Prowlarr-Backed Indexer Crawling (Proactive Ingestion)

**Goal:** Poll configured indexers on a schedule via Prowlarr to proactively pull new
magnets and their associated metadata, independent of what the DHT discovers.

### How it works

Instead of waiting for the DHT to surface a hash organically, bitmagnet polls Prowlarr
on a schedule and ingests new listings directly. The magnet contains the hash, which slots
into the existing `torrents` table. The metadata from the indexer (title, category,
description, file list) supplements or replaces what the DHT/TMDB pipeline would produce.

### Config design

```yaml
prowlarr:
  url: http://prowlarr:9696
  api_key: your_key_here

  # crawl_indexers: polled proactively on a schedule for new magnets and metadata.
  # Should be high-volume public trackers. Volume matters more than curation here.
  crawl_indexers:
    - id: 12    # e.g. TPB
      interval: 15m
    - id: 34    # e.g. 1337x
      interval: 30m
```

### Key implementation details

- Runs as a separate worker alongside the DHT crawler
- Feeds the same persistence pipeline — no duplicate DB logic
- Indexer-specific crawl intervals configurable per entry
- Deduplication: hashes already in the DB are skipped

---

## V2 Road 3 — Public Tracker RSS Polling

**Goal:** Poll public RSS feeds from major trackers to extract magnets and metadata in
near-real-time without HTML scraping or API dependencies.

### How it works

Most public trackers publish RSS feeds of new uploads. These are lightweight, structured,
and update continuously. A simple RSS worker polls configured feed URLs, extracts magnet
links and metadata, and feeds results into the persistence pipeline. No authentication,
no scraping, no per-site parser maintenance.

### Why RSS first

RSS is the lowest-friction starting point for Road 2 coverage. It requires no Prowlarr
dependency, no authentication, and the feeds are stable and well-documented. HTML scraping
is fragile and high-maintenance. RSS gives 80% of the coverage with 20% of the complexity.

### Config design

```yaml
rss_crawler:
  feeds:
    - url: https://thepiratebay.org/rss/top100/200  # video
      interval: 10m
    - url: https://1337x.to/rss.xml
      interval: 15m
```

---

## V2 Enrichment Tier Summary

```
Tier 1 — DHT crawler (v1, existing)
  Raw hash discovery from the network. Baseline coverage.

Tier 2 — RSS crawler (V2 Road 3)
  Proactive magnet + metadata ingestion from public tracker feeds.
  High quality data for new and popular content, near real-time.

Tier 3 — Prowlarr crawl (V2 Road 2)
  Scheduled polling of configured Prowlarr indexers for magnets + metadata.
  Covers content not on public RSS feeds, including private tracker content.

Tier 4 — Prowlarr lookup (V2 Road 1)
  Reactive fallback for hashes the classifier cannot identify via TMDB.
  Queries Prowlarr only when all other identification has failed.
```

---

## V2 Implementation Order

1. **V2 Road 3 — RSS crawler** — standalone worker, no external dependencies, highest ROI
2. **V2 Road 2 — Prowlarr crawl** — requires Prowlarr instance, builds on RSS patterns
3. **V2 Road 1 — Prowlarr lookup** — most complex (cache, throttle, classifier integration)

---

## V3 — DB Size Management (Planned)

> Addresses a common community concern about database growth under sustained DHT crawling.
> Not a blocker for most users but important for long-running deployments on limited storage.

### Background

The DHT crawler is indiscriminate by design — it indexes everything the network surfaces,
including torrents that died years ago with zero seeders. Over time this produces a DB that
grows without bound. Two categories of data are the main culprits:

1. **Old torrents** — content indexed months or years ago that no one is seeding
2. **Dead torrents** — torrents with few or zero seeders that have never recovered

The existing disk guardian script (M0.6) is a blunt instrument — it pauses the crawler
when disk is nearly full. This feature addresses DB growth proactively before it becomes
a crisis.

V3 has four components: configurable trim rules, seed data revalidation (to make trim
accurate), a manual seed refresh button in the UI, and Prowlarr source protection.

### Feature 1 — Configurable seed-based trim (per source)

Trim rules are defined per source. Each source can independently configure:
- `max_age_days` — torrents older than this are candidates for trim (-1 = disabled)
- `min_seeds` — torrents with fewer seeds than this are candidates for trim (-1 = disabled)
- `ignore_no_seed_data` — if true, torrents without any seed data are exempt from
  seed-based trim (prevents purging entries that simply lack seed counts)

Both conditions must be met for a torrent to be trimmed: it must be older than
`max_age_days` AND have fewer than `min_seeds` seeders. Setting either to -1
disables that dimension of the check.

```yaml
db_trim:
  enabled: false                       # master switch, default off
  dry_run: false                       # preview mode — log what would be removed
  protect_prowlarr_sources: true       # never trim if a Prowlarr source exists
  sources:
    - source: dht
      max_age_days: 180                # trim DHT torrents older than 6 months...
      min_seeds: 1                     # ...that have fewer than 1 seeder
      ignore_no_seed_data: true        # don't trim if seed count is unknown
    - source: prowlarr-20
      max_age_days: -1                 # never trim Prowlarr content by age
      min_seeds: -1                    # never trim Prowlarr content by seeds
    - source: default
      max_age_days: -1                 # default: never trim
      min_seeds: -1
      ignore_no_seed_data: true
```

Runs as a scheduled background worker. Only removes a torrent when ALL of its
sources' trim rules agree it should go — a torrent indexed by both DHT and
Prowlarr is only trimmed when both sources' rules apply.

### Feature 2 — Seed data revalidation service

Trim is only as accurate as the seed data it acts on. The DHT crawler already
updates seed/leech counts via BEP 33 bloom filters when it re-encounters known
hashes (`infohash_triage.go`, `RescrapeThreshold` default 30 days). This covers
DHT-sourced torrents passively.

For non-DHT sources (Prowlarr, RSS, future imports), seed data goes stale because
the DHT crawler may never re-encounter those hashes. The revalidation service
addresses this by re-querying the **discovery indexer** (the source that originally
found the torrent) for updated seed/leech counts.

Configuration:
- `enabled` — master toggle (default: false)
- `min_seeds` — only revalidate torrents with fewer than X seeds
- `max_age_days` — only revalidate torrents newer than X days (ignore long-term
  dead torrents — let trim handle those)
- `interval_days` — how often to re-check each qualifying torrent
- Rate-limited with oldest-checked-first priority to avoid hammering indexers

```yaml
seed_revalidation:
  enabled: false
  min_seeds: 5                   # revalidate torrents with < 5 seeds
  max_age_days: 30               # only recheck torrents newer than 30 days
  interval_days: 7               # recheck each torrent at most every 7 days
  rate_limit_per_minute: 10      # max indexer queries per minute
```

This creates a clean lifecycle: new torrents get actively monitored, torrents that
age out of the `max_age_days` window stop getting rechecked, and trim cleans up
the ones confirmed dead.

### Feature 3 — Manual seed/leech refresh (UI)

A button on the torrent detail page to manually trigger a seed/leech data refresh
for a specific torrent. Queries the discovery indexer on demand. Useful for
spot-checking individual torrents without waiting for the background revalidation
cycle, or for rescuing a specific torrent from the trim pipeline.

### Feature 4 — Prowlarr resilience preservation

When trimming, torrents that exist in a Prowlarr source should be exempt from
DHT age/seed-based trim rules. This preserves the key selling point of the Prowlarr
integration: content remains searchable in bitmagnet even when the original
indexer goes offline.

Controlled by `protect_prowlarr_sources: true` (default) in the trim config.

### Implementation notes

- Trim runs as a low-priority background worker on a configurable schedule
- Deletes cascade via existing FK constraints (torrent_files, torrent_contents,
  torrents_torrent_sources all have ON DELETE CASCADE)
- Worker logs rows removed per source per run for observability
- Dry-run mode for users to preview what would be removed before committing
- Migration needed: add index on `torrents_torrent_sources (seeders, updated_at)`
  for efficient trim queries
- Revalidation service runs as a separate worker, queries discovery indexer only
- Manual refresh button calls a new REST endpoint (same pattern as Prowlarr
  crawl-now: REST via existing fx wiring, no GraphQL)

---

## V4 — Bulk Import (Planned)

> Enables backfilling the database from external torrent database dumps (TPB, RARBG, etc.).
> Transforms bitmagnet from a pure crawler into a digital hoarding platform — import once,
> then let DHT crawling keep the data current going forward.

### Feature 1 — Source management

Each import is tagged to a source (new or existing). Users define the source on import
or match to an existing source. Sources track origin (TPB, RARBG, DHT, Prowlarr, etc.)
and are the organizing principle for trim rules (V3) and data lifecycle.

### Feature 2 — Format detection and parsing

User declares the input format (CSV, SQL dump, etc.). System validates the first ~1000
rows against the expected schema. If a mismatch is detected (e.g., user said CSV but
it's a SQL dump, or columns don't match), the import pauses and advises the user of the
detected format. Auto-detection of well-known dumps (TPB, RARBG) based on column
signatures, suggesting the correct parser automatically.

### Feature 3 — Deduplication

Info hash is the dedup key. If a hash already exists in the DB, the import applies the
user-selected conflict resolution strategy rather than inserting a duplicate.

### Feature 4 — Duplicate conflict resolution

Three merge strategies, selectable at import time:

1. **Overwrite** — replace existing metadata with imported data
2. **Add missing** (recommended default) — only fill in fields that are currently
   null/empty in the existing record, leave populated fields untouched
3. **Ignore** — skip the duplicate entirely, keep existing data as-is

### Feature 5 — TMDB classification

Two options only: **background** (throttled ~1 req/sec, chips away over days/weeks) or
**disabled**. No immediate bulk classification option — importing millions of entries
with TMDB enabled would get the user rate-limited or banned without understanding why.

### Feature 6 — Import summary

Two-phase display:

**Pre-import preview** (first ~1000 rows): detected format, column mapping, estimated
total records, sample entries for user sanity check before committing.

**Post-import summary**:
- Total records processed / imported / skipped (dupes) / failed (malformed)
- Records with complete metadata vs. hash-only
- Records with seed/leech data vs. without
- Category breakdown (Movies, TV, Music, Books, Software, XXX, Unknown)
- Date range of imported data (oldest to newest creation date)
- Source assignment confirmation

### Feature 7 — Interrupted import recovery

Large imports (multi-GB SQL dumps) can fail mid-way. The import system tracks the last
successfully imported row and supports resuming from that point. Full rollback of
millions of inserts is impractical at scale — resume is the correct approach.

### Feature 8 — Per-source trim rules for imports

Imported sources inherit the V3 trim system. Each imported source gets its own
configurable trim rules: age threshold, seed threshold, and the "ignore torrents
without seed data" flag. Default is -1 (disabled) for all thresholds — opt-in
trimming, not opt-out. Digital hoarders keep everything by default.

### Integration with V3

- Imported torrents with seed data are subject to trim rules like any other source
- Imported torrents without seed data are protected by `ignore_no_seed_data: true`
  until the DHT crawler or revalidation service updates their seed counts
- The V3 revalidation service can re-query discovery indexers for imported torrents
  that fall below the seed threshold, keeping imported data fresh over time

---

## Infrastructure — VPN/Gluetun DHT Sidecar (Deployed)

> **Status: DEPLOYED** — DHT crawler runs through gluetun VPN via sidecar container pattern.

### Problem

Running bitmagnet under `network_mode: service:gluetun` isolates it from the local
Docker network — it can't reach Prowlarr, Postgres, or the *arr stack. Gluetun's
`FIREWALL_OUTBOUND_SUBNETS` setting is unreliable for allowing local network access
across containers. This is a known issue across multiple services (same problem hit
Pulsarr/trackarr).

### Solution — Split workers across two containers

Bitmagnet's worker architecture already supports running individual workers in
separate processes. All workers communicate through the shared Postgres database
(queue tables, torrent tables). No direct inter-process communication needed.

**bitmagnet** (main) — normal Docker network:
- `--keys=http_server,queue_server,prowlarr_crawler,db_trim`
- Full access to Prowlarr, Postgres, *arr stack, WebUI on port 3333

**bitmagnet-dht** (sidecar) — gluetun network namespace:
- `--keys=dht_crawler`
- `network_mode: container:gluetun`
- DHT UDP traffic routed through VPN tunnel
- Reaches Postgres via shared external Docker network (`bitmagnet-net`)

### Network topology

```
┌─────────────────────────────────────────────────┐
│  gluetun compose (separate stack)               │
│  ┌───────────┐                                  │
│  │  gluetun  │◄── VPN tunnel ──► Internet       │
│  │  :3334/udp│    (WireGuard)                   │
│  └─────┬─────┘                                  │
│        │ network_mode: container:gluetun        │
│  ┌─────┴──────────┐                             │
│  │  bitmagnet-dht │ (dht_crawler only)          │
│  └────────────────┘                             │
│        │ bitmagnet-net (external network)        │
└────────┼────────────────────────────────────────┘
         │
┌────────┼────────────────────────────────────────┐
│  bitmagnet compose                              │
│  ┌─────┴──────┐    ┌──────────┐                 │
│  │  postgres  │◄───│ bitmagnet│ (main workers)  │
│  │  :5432     │    │ :3333    │                  │
│  └────────────┘    └──────────┘                 │
│        │ bitmagnet-net (external network)        │
└─────────────────────────────────────────────────┘
```

### Deployment requirements

1. Create shared network: `docker network create bitmagnet-net`
2. Gluetun compose: add `bitmagnet-net` to gluetun's networks, add DHT ports
   (`3334:3334/udp`, `3334:3334/tcp`) to gluetun's port mappings
3. Bitmagnet compose: remove DHT ports from main container, remove `dht_crawler`
   from main container's keys, add `bitmagnet-dht` service with
   `network_mode: container:gluetun`, add `bitmagnet-net` to postgres and
   bitmagnet services
4. Deploy order: gluetun first, then bitmagnet
5. `bitmagnet-dht` uses `POSTGRES_HOST=bitmagnet-postgres` (container name,
   resolved via the shared external network)

### Pattern origin

Adapted from the Pulsarr/trackarr project which uses a similar sidecar pattern
(`VPN_CONTAINER=gluetun`) to route tracker pings through gluetun while keeping
the main container on the regular network. Bitmagnet's case is simpler because
the worker split is already built into the architecture — no custom proxy or
relay code needed.

---

## DHT Sidecar Status Reporting — COMPLETE

> **Status: COMPLETE** (commits `875185a`, `33c2c20`)
> The main bitmagnet dashboard shows "DHT: Inactive" because the primary instance
> doesn't run the `dht_crawler` worker — it runs in the sidecar container. The dashboard
> has no way to know the sidecar exists or whether it's healthy.

### Problem

With the split-worker sidecar deployment, the main bitmagnet instance runs
`http_server,queue_server,prowlarr_crawler,db_trim` and the DHT sidecar runs
`dht_crawler` in a separate container behind gluetun. The dashboard's DHT status
check only looks at the local process's worker list — since `dht_crawler` isn't
running locally, it reports Inactive. This is confusing and makes it look like
the deployment is broken.

### Solution

Two new environment variables on the main instance:

```yaml
environment:
  DHT_SIDECAR_ENABLED: "true"
  DHT_SIDECAR_URL: "http://bitmagnet-dht:3334"   # or health endpoint
```

When `DHT_SIDECAR_ENABLED=true`, the dashboard status logic changes:
1. Instead of checking for a local `dht_crawler` worker, it queries the sidecar's
   health endpoint at `DHT_SIDECAR_URL`
2. If the sidecar responds healthy → dashboard shows "DHT: Active (Sidecar)"
3. If the sidecar is unreachable → dashboard shows "DHT: Sidecar Unreachable"
4. If `DHT_SIDECAR_ENABLED=false` (default) → existing behavior unchanged

### Implementation components

| Component | Description |
|---|---|
| Config fields | Add `DHTSidecarEnabled bool` and `DHTSidecarURL string` to appropriate config struct |
| Health probe | HTTP GET to sidecar URL on a 30s interval, cache result |
| Dashboard API | Modify the status endpoint to return sidecar state when enabled |
| Dashboard UI | Update Angular DHT status display to show sidecar states |
| Sidecar health endpoint | The DHT sidecar already runs bitmagnet — expose a minimal health endpoint on the sidecar (may already exist via `http_server` if added to sidecar keys, or add a lightweight `/health` route) |

### Open questions

- Does the sidecar need to run `http_server` as an additional key to expose a health endpoint, or should we add a dedicated lightweight health listener?
- Should the sidecar health check also report DHT-specific metrics (node count, queries/sec) back to the main dashboard?
- Network path: the sidecar is behind gluetun's network namespace — confirm the main instance can reach it via the shared `bitmagnet-net` external network

---

## TMDB/OMDb Classification for Prowlarr-Sourced Torrents — COMPLETE

> **Status: COMPLETE** (commit `a0dddc9`)
> TMDB and OMDb enrichment only runs on DHT-sourced torrents. Prowlarr-sourced
> torrents are not being classified/enriched. This significantly limits the value
> of the Prowlarr crawler — torrents come in with indexer metadata only, no
> poster art, no structured genre/cast/crew data, no content matching.

### Problem

When the Prowlarr crawler was built (V2.1), it was designed to skip TMDB classification
via `ClassifierFlags` in the importer. The rationale was that Prowlarr metadata was
"good enough" and classification would be expensive for bulk imports. In practice this
means Prowlarr torrents show up in search results with minimal metadata — no poster,
no TMDB/OMDb enrichment, no content type matching. They're second-class entries compared
to DHT torrents that go through the full classifier pipeline.

### What needs to happen

1. Investigate the current `ClassifierFlags` skip logic in `internal/importer/importer.go`
   to understand exactly what's being bypassed for Prowlarr sources
2. Determine whether Prowlarr torrents should run through the full classifier (including
   TMDB + OMDb) or a subset of it
3. Consider rate limiting implications — Prowlarr can import hundreds of torrents per
   crawl, and TMDB's free tier is rate-limited to ~1 req/sec
4. Option A: queue Prowlarr torrents for classification like DHT torrents (same pipeline,
   just remove the skip flag)
5. Option B: classify on import but with a throttle/batch mode
6. Ensure OMDb enrichment (which runs after TMDB in the classifier) also fires for
   Prowlarr-sourced content

