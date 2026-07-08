-- +goose Up
-- +goose StatementBegin

-- Composite index to support efficient DB trim queries.
-- Trim queries filter on source + created_at + seeders, so this index
-- covers the common query pattern without a sequential scan.

CREATE INDEX IF NOT EXISTS idx_torrents_torrent_sources_trim
    ON torrents_torrent_sources (source, created_at, seeders);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_torrents_torrent_sources_trim;

-- +goose StatementEnd
