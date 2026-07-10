-- +goose Up
INSERT INTO metadata_sources (key, name, created_at, updated_at)
VALUES ('omdb', 'OMDb', now(), now())
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM metadata_sources WHERE key = 'omdb';
