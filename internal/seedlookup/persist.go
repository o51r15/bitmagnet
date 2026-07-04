package seedlookup

import (
	"context"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"gorm.io/gorm/clause"
)

// persistResult upserts a dht source row with the seed/leech counts and
// updates last_seed_lookup_at on the originating Prowlarr source row.
func (w *worker) persistResult(ctx context.Context, r lookupResult) error {
	db, err := w.db.Get()
	if err != nil {
		return err
	}

	now := time.Now()

	// Upsert the dht source row with seed/leech counts.
	dhtSource := model.TorrentsTorrentSource{
		Source:   "dht",
		InfoHash: r.infoHash,
		Seeders:  r.seeders,
		Leechers: r.leechers,
	}

	if err := db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "info_hash"},
			{Name: "source"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"seeders",
			"leechers",
			"updated_at",
		}),
	}).Create(&dhtSource).Error; err != nil {
		return err
	}

	// Mark the Prowlarr source rows as looked up so the backfill scanner
	// won't re-query this hash.
	if err := db.WithContext(ctx).Exec(`
		UPDATE torrents_torrent_sources
		SET last_seed_lookup_at = ?
		WHERE info_hash = ? AND source LIKE 'prowlarr-%'
	`, now, r.infoHash).Error; err != nil {
		w.logger.Warnw("seed_lookup: failed to update last_seed_lookup_at",
			"info_hash", r.infoHash.String(), "error", err)
		// Non-fatal — the dht source was already written.
	}

	return nil
}
