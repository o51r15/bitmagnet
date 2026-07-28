# bitmagnet — Stability & Features Fork

**Self-hosted BitTorrent DHT crawler, indexer, and content classifier — hardened for stability and extended with Prowlarr, RSS, bulk import, and richer metadata.**

Crawls the BitTorrent DHT, classifies what it finds against TMDB, and gives you a searchable index with a web UI — with the upstream crash-and-lockup cycle fixed and a stack of new ingestion sources bolted on.

[![GitHub last commit](https://img.shields.io/github/last-commit/o51r15/bitmagnet)](https://github.com/o51r15/bitmagnet)
[![Docker Image](https://img.shields.io/badge/ghcr.io-bitmagnet%3Alatest-blue)](https://ghcr.io/o51r15/bitmagnet)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](https://github.com/o51r15/bitmagnet/blob/main/LICENSE)
[![Go 1.23+](https://img.shields.io/badge/go-1.23%2B-blue.svg)](https://go.dev/)

---

> **Community fork of [bitmagnet-io/bitmagnet](https://github.com/bitmagnet-io/bitmagnet), not affiliated with the upstream project.** Changes are additive — nothing is removed from upstream, and if upstream merges equivalent fixes this fork drops the duplicates. Under active development; expect rough edges. Full documentation lives in the **[Wiki](https://github.com/o51r15/bitmagnet/wiki)**.

## What It Does

bitmagnet listens on the BitTorrent DHT network, collects info hashes, fetches their metadata, classifies the content against TMDB, and stores everything in Postgres behind a web UI and GraphQL API. It integrates with the Servarr stack as a Torznab indexer.

Upstream bitmagnet is alpha software that, under sustained load, tends to exhaust host resources and lock up the machine roughly every 24 hours. This fork's first job was to fix that — a queue index fix, DHT network recovery, a UDP connection cap, and queue backpressure all target documented crash paths. Beyond stability, it adds ingestion sources upstream doesn't have: Prowlarr crawling, RSS/Torznab feeds, bulk database dump import, plus OMDb and ThePornDB enrichment on top of TMDB.

## Features

- **Stability fixes** — queue index fix ([#496](https://github.com/bitmagnet-io/bitmagnet/issues/496)), DHT network recovery ([#359](https://github.com/bitmagnet-io/bitmagnet/issues/359)), UDP connection cap, queue backpressure, bootstrap node updates, BEP-47 padding exclusion, and a disk guardian script
- **Prowlarr integration** — scheduled crawling of configured indexers into a persistent local index that survives indexer downtime, with a per-indexer Crawl Now button and seed count refresh
- **Bulk database import** — import existing torrent database dumps (RARBG, Pirate Bay, magnetico, and more) through the web UI; auto-detects CSV, NDJSON, SQL dump, and SQLite formats, streams large files instead of loading them into memory, runs asynchronously with live progress, filters by category, and trim-protects imported sources
- **RSS feed ingestion** — poll RSS and Torznab feeds on a per-feed schedule; a format-agnostic parser resolves an info hash from magnet links (hex or base32), Torznab/Newznab attributes, plain `info_hash` tags, ezRSS blocks, or a bare-hash GUID, and decodes non-UTF-8 feeds
- **OMDb enrichment** — Rotten Tomatoes scores, Metacritic ratings, awards, and box office data via the OMDb API, applied automatically after TMDB classification on both DHT and Prowlarr torrents
- **Adult content classification** — a ThePornDB (TPDB) classifier matches and titles XXX content that TMDB does not cover
- **DB size management** — configurable per-source trim with Prowlarr protection, dry-run mode, and batch processing
- **VPN sidecar deployment** — split the DHT crawler behind gluetun while the main instance stays on the regular network, with dashboard health probing of the sidecar

## Quick Start

### Docker (recommended)

1. Get a free [TMDB API key](https://www.themoviedb.org/settings/api).

2. Create the config directory and `config.yml`:

```bash
mkdir -p ~/docker/bitmagnet/config ~/docker/bitmagnet/data/postgres
```

```yaml
# ~/docker/bitmagnet/config/config.yml
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
```

3. Deploy with Docker Compose:

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
      - TMDB_API_KEY=your_tmdb_key_here   # free at themoviedb.org/settings/api
      - TZ=UTC
    volumes:
      - ~/docker/bitmagnet/config:/root/.config/bitmagnet
    command:
      - worker
      - run
      - --keys=http_server
      - --keys=queue_server
      - --keys=dht_crawler
    depends_on:
      postgres:
        condition: service_healthy

  postgres:
    image: postgres:16-alpine
    container_name: bitmagnet-postgres
    restart: unless-stopped
    volumes:
      - ~/docker/bitmagnet/data/postgres:/var/lib/postgresql/data
    environment:
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=bitmagnet
      - PGUSER=postgres
    healthcheck:
      test: ["CMD-SHELL", "pg_isready"]
      interval: 10s
      start_period: 20s
```

Dashboard: **<http://localhost:3333>**

### From Source

```bash
git clone https://github.com/o51r15/bitmagnet.git
cd bitmagnet
docker compose up -d
```

For VPN-protected crawling, Prowlarr integration, or advanced Postgres tuning, see the [Wiki](https://github.com/o51r15/bitmagnet/wiki).

## Configuration

See the [Wiki](https://github.com/o51r15/bitmagnet/wiki) for all options. Key settings:

| Setting | Default | Description |
| --- | --- | --- |
| `dht_crawler.scaling_factor` | `1` | Pipeline concurrency multiplier; raise on capable hardware |
| `dht_crawler.save_files_threshold` | `50` | Max files saved per torrent, limits DB write storms |
| `dht_crawler.rescrape_threshold` | `720h` | Age before a torrent's seeder/leecher counts are refreshed |
| `processor.concurrency` | `2` | Queue processor workers; safe at 2 with the queue index fix |
| `tmdb.enabled` | `true` | Classify movies and TV against TMDB |
| `log.level` | `warn` | Reduce log I/O under sustained crawl load |

Environment variables set in the compose file:

| Variable | Description |
| --- | --- |
| `TMDB_API_KEY` | Free key from themoviedb.org/settings/api |
| `POSTGRES_HOST` | Postgres hostname (`postgres` in the compose above) |
| `POSTGRES_PASSWORD` | Must match the Postgres container's password |
| `TZ` | Set to `UTC` to keep UI timestamps correct |

## How It Works

```
BitTorrent DHT
  -> DHT Crawler (info hash discovery, metadata fetch)
  -> Persist (Postgres, queue backpressure valve)
  -> Queue Processor (classification workflow)
  -> Classifier (content type -> local search -> TMDB -> OMDb / TPDB enrichment)
  -> Searchable Index (web UI + GraphQL + Torznab)

Additional ingestion sources feed the same pipeline:
  Prowlarr indexers ---+
  RSS / Torznab feeds --+--> Importer -> Persist -> Queue Processor -> ...
  Bulk DB dumps -------+
```

The DHT crawler is the baseline source. Prowlarr, RSS, and bulk import all feed the same importer and persistence path, so everything lands in one searchable index regardless of where it came from. Classification runs the same workflow for every source; Prowlarr-sourced items skip the TMDB API call but still get typed and titled from indexer metadata.

## Requirements

- Docker host with a Postgres 16 instance (included in the compose above)
- A free [TMDB API key](https://www.themoviedb.org/settings/api) for content classification
- Optional: a running [Prowlarr](https://prowlarr.com) instance for indexer crawling
- Optional: [gluetun](https://github.com/qdm12/gluetun) for VPN-protected crawling

## Tech Stack

Go 1.23+, PostgreSQL 16, Angular, GraphQL (gqlgen), Docker multi-arch (amd64 + arm64). Metadata via TMDB, OMDb, and ThePornDB.

## License

[MIT](https://github.com/o51r15/bitmagnet/blob/main/LICENSE)

## Relationship to Upstream

This fork tracks [bitmagnet-io/bitmagnet](https://github.com/bitmagnet-io/bitmagnet) `main`. Changes are additive — nothing is removed from upstream. If upstream merges equivalent fixes, this fork drops the duplicates. Issues and PRs welcome; this is a community project, not a commercial product.
