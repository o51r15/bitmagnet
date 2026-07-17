-- +goose Up
-- RSS feed poller high-water mark (mirrors prowlarr_indexer_state pattern).
CREATE TABLE IF NOT EXISTS rss_feed_state (
    feed_name TEXT PRIMARY KEY,
    last_seen_publish_date TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01'
);

-- +goose Down
DROP TABLE IF EXISTS rss_feed_state;
