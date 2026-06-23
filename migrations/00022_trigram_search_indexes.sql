-- +goose Up
-- +goose StatementBegin

-- Trigram indexes for full-text search stability
--
-- Without these, every search query (UI, GraphQL, Torznab) performs a full
-- sequential scan of the torrents and torrent_files tables. Under crawl
-- load these scans hold read locks and compete with write operations,
-- causing cascading slowdowns and connection starvation.
--
-- These are not included in the upstream migrations. They are baked in here
-- so fresh installs get them automatically without manual intervention.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS idx_torrents_name_trgm
    ON torrents USING gin (name gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_torrent_files_path_trgm
    ON torrent_files USING gin (path gin_trgm_ops);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_torrents_name_trgm;
DROP INDEX IF EXISTS idx_torrent_files_path_trgm;

-- +goose StatementEnd
