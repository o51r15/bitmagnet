package seedlookup

import (
	"context"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
)

// runBackfill scans the database for Prowlarr-sourced torrents that lack seed
// data and yields their infohashes. It only runs when the hot queue is empty,
// making it secondary to new imports.
//
// The scanner uses last_seed_lookup_at IS NULL as its cursor — once a hash is
// looked up (even with 0 seeders), it won't be re-scanned. This keeps the
// backfill purely DB-driven with zero in-memory state.
func (w *worker) runBackfill(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		hashes, err := w.fetchBackfillBatch(ctx)
		if err != nil {
			w.logger.Warnw("seed_lookup: backfill query failed", "error", err)
			w.sleepOrDone(ctx, w.config.BackfillPollInterval)
			continue
		}

		if len(hashes) == 0 {
			// Backlog clear — sleep before polling again.
			w.sleepOrDone(ctx, w.config.BackfillPollInterval)
			continue
		}

		for _, h := range hashes {
			select {
			case <-ctx.Done():
				return
			case w.backfillQueue <- h:
			}
		}
	}
}

// fetchBackfillBatch returns a batch of infohashes from Prowlarr sources that
// have never had a seed lookup performed (last_seed_lookup_at IS NULL) and
// don't already have a dht source row with valid seeders.
func (w *worker) fetchBackfillBatch(ctx context.Context) ([]protocol.ID, error) {
	db, err := w.db.Get()
	if err != nil {
		return nil, err
	}

	var hashes []protocol.ID
	result := db.WithContext(ctx).Raw(`
		SELECT info_hash FROM (
		  SELECT DISTINCT ON (tts.info_hash) tts.info_hash, tts.created_at
		  FROM torrents_torrent_sources tts
		  WHERE tts.source LIKE 'prowlarr-%'
		    AND tts.last_seed_lookup_at IS NULL
		    AND NOT EXISTS (
		      SELECT 1 FROM torrents_torrent_sources dht
		      WHERE dht.info_hash = tts.info_hash
		        AND dht.source = 'dht'
		        AND dht.seeders IS NOT NULL
		    )
		  ORDER BY tts.info_hash, tts.created_at DESC
		) sub
		ORDER BY sub.created_at DESC
		LIMIT ?
	`, w.config.BackfillBatchSize).Scan(&hashes)

	if result.Error != nil {
		return nil, result.Error
	}
	return hashes, nil
}

// sleepOrDone sleeps for the given duration or returns early if ctx is cancelled.
func (w *worker) sleepOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
