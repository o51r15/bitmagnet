-- +goose Up
-- +goose StatementBegin

-- Tracks when the seed lookup worker last queried the DHT for a given
-- Prowlarr source row. Used by the backfill scanner to skip already-checked
-- hashes and by the future periodic re-scrape feature (Phase 2).

ALTER TABLE torrents_torrent_sources
    ADD COLUMN IF NOT EXISTS last_seed_lookup_at TIMESTAMPTZ;

-- Index for the backfill scanner's query: find Prowlarr sources that
-- haven't been looked up yet.
CREATE INDEX IF NOT EXISTS idx_tts_seed_lookup_backfill
    ON torrents_torrent_sources (created_at DESC)
    WHERE source LIKE 'prowlarr-%' AND last_seed_lookup_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_tts_seed_lookup_backfill;
ALTER TABLE torrents_torrent_sources DROP COLUMN IF EXISTS last_seed_lookup_at;

-- +goose StatementEnd
