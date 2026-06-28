# bitmagnet — Stability & Features Fork

> **Community fork of [bitmagnet-io/bitmagnet](https://github.com/bitmagnet-io/bitmagnet).**
> Not affiliated with the upstream project.

---

## What is bitmagnet?

A self-hosted BitTorrent indexer, DHT crawler, content classifier and torrent search engine with a web UI and GraphQL API.

This fork extends the upstream project with stability fixes and new features. It ships as a ready-to-deploy Docker image built automatically on every push to `main`.

```
ghcr.io/o51r15/bitmagnet:latest
```

---

## Two ways to use bitmagnet

### 1 — Recent cache (lightweight, storage-friendly)

Poll DHT and configured indexers continuously and keep only recent content — for example, the last 90 days. Older torrents and unseeded content are purged automatically on a schedule.

**Good for:** Users who want a fast local search index of currently active torrents without committing to unlimited storage growth. A recent cache gives you search results for content that's actually alive and seeded right now.

**Key config:** Set `db_trim.sources.dht.max_age_days: 90` and `db_trim.sources.dht.purge_unseeded_after_hours: 40`. The unseeded purge delay gives new torrents time to establish seeders before being considered dead.

### 2 — Full backfill (hoarder mode)

Crawl DHT and indexers continuously with no age limit. Build a permanent local database of every torrent you can locate. Combined with Prowlarr indexer integration, this creates a resilient offline cache — torrents remain searchable even when the original indexer goes offline.

**Good for:** Users who want maximum coverage and are willing to manage storage growth manually or via the trim tools.

**Key config:** Leave trim disabled (default). Add as many Prowlarr indexers as you have access to.

---

## Key features

### Stability (all upstream issues addressed)
- **Queue index fix** — eliminates the primary DB memory pressure that caused 24-hour crash cycles ([#496](https://github.com/bitmagnet-io/bitmagnet/issues/496))
- **DHT network recovery** — self-healing ktable monitor, no more permanent stalls after network interruption ([#359](https://github.com/bitmagnet-io/bitmagnet/issues/359))
- **UDP connection cap** — global semaphore prevents goroutine storms under high scaling factors
- **Queue backpressure** — configurable depth limit stops the queue growing unboundedly under TMDB load
- **Bootstrap node fixes** — updated DHT bootstrap nodes, removed stale/unreliable entries
- **BEP-47 padding exclusion** — synthetic padding files no longer bloat the torrent_files table
- **Disk guardian** — optional script pauses crawling at configurable disk usage threshold

### Prowlarr integration
Connect bitmagnet to a running [Prowlarr](https://prowlarr.com) instance to crawl configured indexers on a schedule. Imported torrents persist in the local database and remain searchable even when the source indexer goes offline.

```yaml
# config.yml
prowlarr:
  url: http://prowlarr:9696
  api_key: your_key_here
  indexers:
    - id: 20    # The Pirate Bay
      enabled: true
      categories:
        - Movies
        - TV
    - id: 74    # 1337x
      enabled: true
      categories:
        - Movies
        - TV
        - Audio
```

**Note:** Not all Prowlarr indexers return torrent hashes. Private trackers in particular often omit them. Check your first crawl log — if `imported: 0` but `new_results: 35+`, that indexer isn't compatible. Public trackers like The Pirate Bay and 1337x work reliably.

### DB size management *(coming in V3)*
Configurable age-based trim and dead torrent purge, per source. Keep Prowlarr content indefinitely while trimming DHT to a rolling window. Unseeded torrent purge with a configurable grace period so new uploads have time to establish seeders before being considered dead.

---

## Deployment

### Prerequisites

**TMDB API key** — bitmagnet uses [The Movie Database](https://www.themoviedb.org) for content classification. Get a free key at [themoviedb.org/settings/api](https://www.themoviedb.org/settings/api).

**Prowlarr** *(optional)* — only needed if you want indexer crawling. Any Prowlarr instance reachable from the bitmagnet container works.

---

### Step 1 — Create directories and config

```bash
mkdir -p ~/docker/bitmagnet/config
mkdir -p ~/docker/bitmagnet/data

cat > ~/docker/bitmagnet/config/config.yml << 'EOF'
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

# Optional: Prowlarr integration
# prowlarr:
#   url: http://your-prowlarr:9696
#   api_key: your_api_key
#   indexers:
#     - id: 20
#       enabled: true
#       categories:
#         - Movies
#         - TV
EOF
```

---

### Step 2 — Deploy

**Via Portainer:**
1. Go to **Stacks → Add stack**
2. Paste `docker-compose.yml` into the editor
3. Replace `your_tmdb_key_here` with your TMDB key
4. Click **Deploy the stack**

**Via Docker Compose CLI:**
```bash
git clone https://github.com/o51r15/bitmagnet.git
cd bitmagnet
# Edit docker-compose.yml — replace your_tmdb_key_here
docker compose up -d
```

---

### Step 3 — Enable Prowlarr crawler *(optional)*

Add `--keys=prowlarr_crawler` to the bitmagnet `command:` block in your compose file and add your Prowlarr config to `config.yml`. The Prowlarr tab will appear in the web UI once the first crawl completes.

```yaml
command:
  - worker
  - run
  - --keys=http_server
  - --keys=queue_server
  - --keys=dht_crawler
  - --keys=prowlarr_crawler   # add this line
```

---

## Gluetun / VPN deployments

If you run bitmagnet behind gluetun, keep postgres on its own bridge network — **do not** put postgres on `network_mode: service:gluetun`. When gluetun loses VPN connectivity, any container sharing its network namespace becomes unreachable. Postgres behind gluetun means a VPN hiccup takes your database offline and causes bitmagnet to crash-loop for the duration of the outage.

The correct topology:

```yaml
services:
  gluetun:
    networks:
      - bitmagnet_internal   # gluetun joins the internal network

  postgres:
    networks:
      - bitmagnet_internal   # postgres is on the bridge, NOT behind gluetun
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U bitmagnet -d bitmagnet"]
      interval: 10s
      timeout: 5s
      retries: 5

  bitmagnet:
    network_mode: "service:gluetun"   # bitmagnet still routes through VPN
    environment:
      - POSTGRES_HOST=postgres         # resolves via shared bridge network
    depends_on:
      postgres:
        condition: service_healthy

networks:
  bitmagnet_internal:
    driver: bridge
```

With this topology, VPN outages cause DHT and TMDB timeouts (logged as warnings) but bitmagnet keeps running. Postgres is unaffected.

---

## Postgres tuning

The included `docker-compose.yml` sets Postgres performance parameters via the `command:` block — no custom image needed. Settings are calibrated for an **8–16GB host** running a write-heavy DHT crawl workload on SSD.

For a **Raspberry Pi (4GB)**, reduce to:
```
-c shared_buffers=256MB -c work_mem=16MB -c maintenance_work_mem=128MB -c max_wal_size=1GB
```

---

## Changes from upstream

| Fix | Issue/PR | Commit |
|---|---|---|
| `queue_jobs` expression index + drop 678MB dead GIN index | [#496](https://github.com/bitmagnet-io/bitmagnet/issues/496) | `c4291d7` |
| `StableBloomFilter` pointer embed — nil panic fix | [#446](https://github.com/bitmagnet-io/bitmagnet/pull/446) | `b67e253` |
| Updated DHT bootstrap nodes | [#454](https://github.com/bitmagnet-io/bitmagnet/pull/454) | `2699390` |
| Exclude BEP-47 `.pad/` padding files | [#458](https://github.com/bitmagnet-io/bitmagnet/pull/458) | `eae031e` |
| TZ fix, fd limit, processor tuning | [#485](https://github.com/bitmagnet-io/bitmagnet/issues/485), [#348](https://github.com/bitmagnet-io/bitmagnet/issues/348) | `6c6016d` |
| Global UDP query semaphore | [#348](https://github.com/bitmagnet-io/bitmagnet/issues/348) | `e62c755` |
| DHT network recovery — ktable health monitor | [#359](https://github.com/bitmagnet-io/bitmagnet/issues/359) | `9757191` |
| Queue backpressure valve | — | `b06a191` |
| Disk usage guardian script | [#187](https://github.com/bitmagnet-io/bitmagnet/issues/187), [#495](https://github.com/bitmagnet-io/bitmagnet/issues/495) | `3048dd5` |
| CI — multi-arch GHCR build on push to main | — | `9dc16bf` |
| Prowlarr indexer crawler integration | — | `d7d291d` |
| Prowlarr web UI page | — | `bafbf7f` |

---

## Relationship to upstream

All stability fixes in this fork are intended to be upstreamable. Where fixes have been merged upstream they will be dropped from this fork on the next rebase to avoid drift.

Upstream repository: https://github.com/bitmagnet-io/bitmagnet
Upstream website: https://bitmagnet.io
