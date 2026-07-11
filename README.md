# bitmagnet -- Stability & Features Fork

> **Community fork of [bitmagnet-io/bitmagnet](https://github.com/bitmagnet-io/bitmagnet).**
> Not affiliated with the upstream project.

A self-hosted BitTorrent indexer, DHT crawler, content classifier and torrent search engine with a web UI and GraphQL API. This fork adds stability fixes, Prowlarr integration, OMDb enrichment, DB size management, and VPN sidecar deployments.

```
ghcr.io/o51r15/bitmagnet:latest
```

Full documentation is in the **[Wiki](https://github.com/o51r15/bitmagnet/wiki)**.

---

## Key features

**Stability fixes** -- queue index fix ([#496](https://github.com/bitmagnet-io/bitmagnet/issues/496)), DHT network recovery ([#359](https://github.com/bitmagnet-io/bitmagnet/issues/359)), UDP connection cap, queue backpressure, bootstrap node updates, BEP-47 padding exclusion, disk guardian script.

**Prowlarr integration** -- scheduled crawling of configured indexers, persistent local index (survives indexer downtime), per-indexer Crawl Now button, seed count refresh. [Wiki: Prowlarr Integration](https://github.com/o51r15/bitmagnet/wiki/Prowlarr-Integration)

**OMDb enrichment** -- Rotten Tomatoes scores, Metacritic ratings, awards, box office data via the OMDb API. Runs automatically after TMDB classification on both DHT and Prowlarr torrents. [Wiki: OMDb Enrichment](https://github.com/o51r15/bitmagnet/wiki/OMDb-Enrichment)

**DB size management** -- configurable per-source trim with Prowlarr protection, dry-run mode, batch processing. [Wiki: DB Size Management](https://github.com/o51r15/bitmagnet/wiki/DB-Size-Management)

**VPN sidecar deployment** -- split DHT crawler behind gluetun while the main instance stays on the regular network. Dashboard health probing reports sidecar status. [Wiki: DHT Sidecar Setup](https://github.com/o51r15/bitmagnet/wiki/DHT-Sidecar-Setup)

---

## Quick start

1. Get a free [TMDB API key](https://www.themoviedb.org/settings/api)
2. Create config directory and `config.yml`:

```bash
mkdir -p ~/docker/bitmagnet/config ~/docker/bitmagnet/data
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
      - TMDB_API_KEY=your_tmdb_key_here
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


For VPN-protected crawling, Prowlarr integration, or advanced Postgres tuning, see the [Wiki](https://github.com/o51r15/bitmagnet/wiki).

---

## Changes from upstream

| Area | What changed |
|------|-------------|
| Queue index | Fix for [#496](https://github.com/bitmagnet-io/bitmagnet/issues/496) -- missing index on queue jobs |
| DHT bootstrap | Updated nodes, nil-panic guard ([#359](https://github.com/bitmagnet-io/bitmagnet/issues/359)) |
| UDP semaphore | Caps concurrent UDP connections to prevent fd exhaustion |
| Queue backpressure | Valve prevents runaway queue growth under load |
| BEP-47 exclusion | Filters padding-file-only torrents from results |
| Disk guardian | Shell script pauses crawling when disk usage exceeds threshold |
| Prowlarr REST API | `POST /api/prowlarr/crawl` with per-indexer scheduling |
| OMDb classifier | Enriches TMDB-classified torrents with RT/Metacritic/awards |
| DB trim | Age-based cleanup per source with Prowlarr protection |
| DHT sidecar | Split-container VPN deployment with dashboard status |


---

## Relationship to upstream

This fork tracks [bitmagnet-io/bitmagnet](https://github.com/bitmagnet-io/bitmagnet) `main`. Changes are additive -- nothing is removed from upstream. If upstream merges equivalent fixes, this fork will drop the duplicates.

Issues and PRs welcome. This is a community project, not a commercial product.
