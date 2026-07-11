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

Poll DHT and configured indexers continuously and keep only recent content — for example, the last 90 days. Older torrents and unseeded content are purged automatically on a schedule via the built-in db_trim worker.

**Good for:** Users who want a fast local search index of currently active torrents without committing to unlimited storage growth.

> **Status:** Fully functional. DHT crawling, Prowlarr indexer crawling, and configurable per-source trim are all live.

### 2 — Full backfill (hoarder mode)

Crawl DHT and indexers continuously with no age limit. Build a permanent local database of every torrent you can locate. Combined with Prowlarr integration, this creates a resilient offline cache — torrents remain searchable even when the original indexer goes offline.

**Good for:** Users who want maximum coverage and are willing to manage storage growth manually.

> **Status:** Fully functional. Prowlarr integration and db_trim (disabled by default) are live. Trim is opt-in — hoarders keep everything by default.

---

## Key features

### Stability fixes — all complete
- **Queue index fix** — eliminates the primary DB memory pressure that caused 24-hour crash cycles ([#496](https://github.com/bitmagnet-io/bitmagnet/issues/496))
- **DHT network recovery** — self-healing ktable monitor, no more permanent stalls after network interruption ([#359](https://github.com/bitmagnet-io/bitmagnet/issues/359))
- **UDP connection cap** — global semaphore prevents goroutine storms under high scaling factors
- **Queue backpressure** — configurable depth limit stops the queue growing unboundedly under TMDB load
- **Bootstrap node fixes** — updated DHT bootstrap nodes, removed stale/unreliable entries
- **BEP-47 padding exclusion** — synthetic padding files no longer bloat the torrent_files table
- **Disk guardian** — optional script pauses crawling at a configurable disk usage threshold

### OMDb enrichment � live
- **OMDb integration** � enriches movies and TV with Rotten Tomatoes scores, Metacritic ratings, awards, box office data, and detailed plot summaries via the [OMDb API](https://www.omdbapi.com). Runs automatically after TMDB classification for any torrent with an IMDB ID.
- Works on both DHT-sourced and Prowlarr-sourced torrents

### Prowlarr integration — live
Connect bitmagnet to a running [Prowlarr](https://prowlarr.com) instance to crawl configured indexers on a schedule. Imported torrents persist in the local database and remain searchable even when the source indexer goes offline.

```yaml
# config.yml
prowlarr:
  url: http://your-prowlarr:9696
  api_key: your_api_key_here
  indexers:
    - id: 20        # The Pirate Bay
      enabled: true
      categories:   # Newznab category IDs; omit to crawl all categories
        - 2000      # Movies
        - 5000      # TV
    - id: 74        # 1337x
      enabled: true
      categories:
        - 2000      # Movies
        - 5000      # TV
        - 3000      # Audio
```

**Newznab category reference:**

| ID | Category |
|---|---|
| 2000 | Movies |
| 5000 | TV |
| 3000 | Audio / Music |
| 7000 | Books / E-Books |
| 4000 | PC / Software |
| 1000 | Console / Games |
| 6000 | XXX |
| 8000 | Other |

**Note:** Not all Prowlarr indexers return torrent hashes. Private trackers in particular often omit them. Check your first crawl log — if `imported: 0` alongside `new_results: 35+`, that indexer is not compatible. Public trackers like The Pirate Bay and 1337x work reliably.

### DB size management — live (V3)

Configurable per-source trim via the `db_trim` worker. Runs as a background service on a 24-hour cycle. Disabled by default — must be explicitly enabled and configured in `config.yml`.

- Per-source rules: trim DHT torrents older than X days with fewer than Y seeders while keeping Prowlarr content indefinitely
- Prowlarr protection: torrents that exist in any Prowlarr source are exempt from trim regardless of other rules
- Dry-run mode to preview what would be removed before committing
- Torrents without seed data can be protected from seed-based trim (`ignore_no_seed_data`)

```yaml
# config.yml
db_trim:
  enabled: true
  dry_run: false
  protect_prowlarr_sources: true
  sources:
    - source: dht
      max_age_days: 180
      min_seeds: 1
      ignore_no_seed_data: true
    - source: default
      max_age_days: -1          # -1 = disabled
      min_seeds: -1
      ignore_no_seed_data: true
```

Add `--keys=db_trim` to the worker command in your compose to enable the worker.

**Still planned:** seed data revalidation service (re-query indexers for updated seed counts) and manual seed/leech refresh button in the torrent detail UI.

---

## Deployment

### Prerequisites

**TMDB API key** — bitmagnet uses [The Movie Database](https://www.themoviedb.org) for content classification. Get a free key at [themoviedb.org/settings/api](https://www.themoviedb.org/settings/api).

**Prowlarr** *(optional)* — only needed for indexer crawling. Any Prowlarr instance reachable from the bitmagnet container works.

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

# Optional: OMDb enrichment (Rotten Tomatoes scores, Metacritic, awards)
# Get a free key at https://www.omdbapi.com/apikey.aspx
# omdb:
#   enabled: true
#   api_key: your_omdb_key_here

log:
  level: warn

# Optional: Prowlarr integration
# prowlarr:
#   url: http://your-prowlarr:9696
#   api_key: your_api_key_here
#   indexers:
#     - id: 20          # The Pirate Bay
#       enabled: true
#       categories:
#         - 2000        # Movies
#         - 5000        # TV

# Optional: DB trim — prune old/dead torrents on a schedule
# db_trim:
#   enabled: true
#   dry_run: true                    # start with dry_run to preview
#   protect_prowlarr_sources: true
#   sources:
#     - source: dht
#       max_age_days: 180
#       min_seeds: 1
#       ignore_no_seed_data: true
#     - source: default
#       max_age_days: -1
#       min_seeds: -1
#       ignore_no_seed_data: true
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

### Step 3 — Enable optional workers

Add worker keys to the `command:` block in your compose file to enable additional features:

```yaml
command:
  - worker
  - run
  - --keys=http_server
  - --keys=queue_server
  - --keys=dht_crawler
  - --keys=prowlarr_crawler   # optional: Prowlarr indexer crawling
  - --keys=db_trim            # optional: automatic trim of old/dead torrents
```

**Prowlarr crawler:** Add your Prowlarr config to `config.yml` (see Prowlarr section above). The Prowlarr tab appears in the web UI once the first crawl completes.

**DB trim:** Add your trim config to `config.yml` (see DB size management section above). Start with `dry_run: true` to preview what would be removed.

---

## Gluetun / VPN deployments

If you want DHT traffic routed through a VPN, there are two approaches depending on your network setup.

### Standard setup — single compose with gluetun

The simplest approach puts bitmagnet and gluetun in the same compose file. Bitmagnet runs under gluetun's network namespace so all traffic (DHT, TMDB, etc.) goes through the VPN. Postgres stays on its own bridge network so a VPN hiccup doesn't take the database offline.

Use gluetun's `FIREWALL_OUTBOUND_SUBNETS` to allow bitmagnet to reach local services (Prowlarr, *arr stack) on your LAN:

```yaml
services:
  gluetun:
    image: qmcgaw/gluetun
    cap_add:
      - NET_ADMIN
    ports:
      - "3333:3333"       # bitmagnet WebUI
      - "3334:3334/udp"   # DHT
      - "3334:3334/tcp"   # DHT
    environment:
      - FIREWALL_OUTBOUND_SUBNETS=192.168.1.0/24   # allow LAN access
      # ... your VPN provider config ...
    networks:
      - bitmagnet_internal

  bitmagnet:
    image: ghcr.io/o51r15/bitmagnet:latest
    network_mode: "service:gluetun"
    environment:
      - POSTGRES_HOST=postgres
      - POSTGRES_PASSWORD=postgres
      - TMDB_API_KEY=your_key
    volumes:
      - ./config:/root/.config/bitmagnet
    command:
      - worker
      - run
      - --keys=http_server
      - --keys=queue_server
      - --keys=dht_crawler
      - --keys=prowlarr_crawler
    depends_on:
      postgres:
        condition: service_healthy

  postgres:
    image: postgres:16-alpine
    networks:
      - bitmagnet_internal
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
      interval: 10s
      timeout: 5s
      retries: 5

networks:
  bitmagnet_internal:
    driver: bridge
```

This works for most users. VPN outages cause DHT and TMDB timeouts (logged as warnings) but bitmagnet keeps running because Postgres is on a separate bridge.

### Alternate setup — split worker sidecar (separate gluetun compose)

If gluetun's `FIREWALL_OUTBOUND_SUBNETS` doesn't reliably allow access to your local network — which can happen depending on your Docker host, network topology, or VPN provider — there is an alternate approach that avoids the problem entirely.

Instead of running all of bitmagnet under gluetun's network, you split the workers across two containers. The main container runs on the normal Docker network with full access to Postgres, Prowlarr, and your *arr stack. A second container runs only the DHT crawler under gluetun's network namespace. Both containers share the same Postgres database — bitmagnet's worker architecture already supports this with no code changes.

An external Docker network bridges the two composes so the DHT container can reach Postgres.

**Step 1 — Create the shared network:**

```bash
docker network create bitmagnet-net
```

**Step 2 — Gluetun compose** (your existing gluetun stack, add DHT ports and the shared network):

```yaml
services:
  gluetun:
    image: qmcgaw/gluetun
    cap_add:
      - NET_ADMIN
    ports:
      - "3334:3334/udp"   # DHT port for bitmagnet-dht
      - "3334:3334/tcp"
      # ... your existing ports ...
    environment:
      # ... your existing VPN config ...
    networks:
      - default
      - bitmagnet-net

  # ... your other services (qbittorrent, socks5, etc.) unchanged ...

networks:
  bitmagnet-net:
    external: true
```

**Step 3 — Bitmagnet compose:**

```yaml
services:
  bitmagnet:
    image: ghcr.io/o51r15/bitmagnet:latest
    container_name: bitmagnet
    restart: unless-stopped
    ports:
      - "3333:3333"
    environment:
      - POSTGRES_HOST=postgres
      - POSTGRES_PASSWORD=postgres
      - TMDB_API_KEY=your_key
      - DHT_CRAWLER_SIDECAR_ENABLED=true
      - DHT_CRAWLER_SIDECAR_URL=http://gluetun:3333
    volumes:
      - ./config:/root/.config/bitmagnet
    command:
      - worker
      - run
      - --keys=http_server
      - --keys=queue_server
      - --keys=prowlarr_crawler
    depends_on:
      postgres:
        condition: service_healthy
    networks:
      - default
      - bitmagnet-net

  bitmagnet-dht:
    image: ghcr.io/o51r15/bitmagnet:latest
    container_name: bitmagnet-dht
    restart: unless-stopped
    network_mode: "container:gluetun"
    environment:
      - POSTGRES_HOST=bitmagnet-postgres    # container name, not service name
      - POSTGRES_PASSWORD=postgres
    volumes:
      - ./config:/root/.config/bitmagnet
    command:
      - worker
      - run
      - --keys=dht_crawler
      - --keys=http_server

  postgres:
    image: postgres:16-alpine
    container_name: bitmagnet-postgres
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
      interval: 10s
      start_period: 20s
    networks:
      - default
      - bitmagnet-net

networks:
  bitmagnet-net:
    external: true
```

**Deploy order:** gluetun stack first, then bitmagnet stack.

**How it works:** The DHT crawler discovers info_hashes and writes them to the Postgres queue. The queue_server in the main container picks them up and handles classification, Prowlarr lookups, etc. The handoff happens entirely through the database — no direct communication between the two bitmagnet containers is needed.

**Dashboard status:** The sidecar environment variables (`DHT_CRAWLER_SIDECAR_ENABLED`, `DHT_CRAWLER_SIDECAR_URL`) tell the main instance to probe the sidecar's HTTP server for health status. The dashboard shows DHT as active when the sidecar is reachable. The sidecar must run `--keys=http_server` alongside `--keys=dht_crawler` to expose its health endpoint.

**Note:** `bitmagnet-dht` uses `POSTGRES_HOST=bitmagnet-postgres` (the container name) because it resolves via the shared external network, not the compose's internal service names. The main bitmagnet container uses `POSTGRES_HOST=postgres` (the service name) since it's in the same compose.

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
| Configurable per-source DB trim worker | — | — |
| DB trim migration — source/age/seeders index | — | — |
| OMDb enrichment via classifier pipeline | � | � |
| DHT sidecar health status reporting | � | `875185a` |
| TMDB/OMDb classification for Prowlarr torrents | � | `a0dddc9` |
| DHT sidecar worker status in dashboard | � | `33c2c20` |

---

## Relationship to upstream

All stability fixes in this fork are intended to be upstreamable. Where fixes are merged upstream they will be dropped from this fork on the next rebase to avoid drift.

Upstream repository: https://github.com/bitmagnet-io/bitmagnet
Upstream website: https://bitmagnet.io
