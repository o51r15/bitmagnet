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

## Milestone 0.2 — Quick Wins (Config / Compose Level)

Low-risk, no Go code required. Estimated effort: 1 session.

| # | Fix | Source | Status |
|---|---|---|---|
| 🔲 | Add `TZ=UTC` to compose — fixes misleading "last torrent found N hours ago" display | Issue [#485](https://github.com/bitmagnet-io/bitmagnet/issues/485) | Planned |
| 🔲 | Add `ulimits: nofile: 65535` to bitmagnet container — raises fd limit to prevent silent UDP socket exhaustion | Issue [#348](https://github.com/bitmagnet-io/bitmagnet/issues/348) | Planned |
| 🔲 | Add `processor.concurrency: 2` to `config/config.yml` — now safe post index-fix; improves queue drain rate | Upstream FAQ + our fix | Planned |
| 🔲 | Add `config/config.yml` to the repo with documented safe defaults | Deployment hygiene | Planned |

---

## Milestone 0.3 — UDP Connection Cap (Go Code)

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

## Milestone 0.4 — DHT Crawler Network Recovery (Go Code)

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

## Milestone 0.5 — Queue Backpressure (Go Code)

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

## Milestone 0.6 — DB Size Guardrails (Config Level)

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
