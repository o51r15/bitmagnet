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

## Using this fork

Pull the pre-built image directly:

```yaml
services:
  bitmagnet:
    image: ghcr.io/o51r15/bitmagnet:latest
```

Or build from source:

```bash
git clone https://github.com/o51r15/bitmagnet.git
cd bitmagnet
docker build -f ci.Dockerfile -t bitmagnet-stability .
```

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
