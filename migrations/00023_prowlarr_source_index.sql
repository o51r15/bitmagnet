-- +goose Up
-- +goose StatementBegin

-- Index on torrents_torrent_sources.source to support per-source filtering.
--
-- The existing torrentSource search facet and the new Prowlarr UI page both
-- filter by source key (e.g. "prowlarr-37"). Without this index those queries
-- scan the entire torrents_torrent_sources table.
--
-- The "dht" source alone already has hundreds of thousands of rows; adding
-- multiple Prowlarr sources makes an unindexed scan increasingly expensive.

CREATE INDEX IF NOT EXISTS idx_torrents_torrent_sources_source
    ON torrents_torrent_sources (source);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_torrents_torrent_sources_source;

-- +goose StatementEnd
