-- +goose Up
-- +goose StatementBegin

-- Tracks the high-water mark publishDate per Prowlarr indexer.
-- Each crawl only imports results newer than last_seen_publish_date,
-- preventing re-processing of already-imported content on repeat runs.

CREATE TABLE IF NOT EXISTS prowlarr_indexer_state (
    indexer_id             INTEGER     PRIMARY KEY,
    last_seen_publish_date TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01T00:00:00Z'
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS prowlarr_indexer_state;

-- +goose StatementEnd
