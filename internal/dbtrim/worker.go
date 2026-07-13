package dbtrim

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const defaultTrimInterval = 24 * time.Hour

type trimWorker struct {
	config  Config
	db      lazy.Lazy[*gorm.DB]
	logger  *zap.SugaredLogger
	stopped chan struct{}
}

func (w *trimWorker) start(ctx context.Context) {
	w.logger.Info("db_trim: worker started")

	// Run once immediately on start, then on interval.
	w.runTrim(ctx)

	ticker := time.NewTicker(defaultTrimInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopped:
			return
		case <-ticker.C:
			w.runTrim(ctx)
		}
	}
}

func (w *trimWorker) runTrim(ctx context.Context) {
	db, err := w.db.Get()
	if err != nil {
		w.logger.Errorw("db_trim: failed to get database connection", "error", err)
		return
	}

	// Collect all distinct sources from the DB.
	var sources []string
	if err := db.WithContext(ctx).
		Table("torrents_torrent_sources").
		Distinct("source").
		Pluck("source", &sources).Error; err != nil {
		w.logger.Errorw("db_trim: failed to list sources", "error", err)
		return
	}

	// Collect Prowlarr-protected info_hashes if enabled.
	var protectedHashes map[string]struct{}
	if w.config.ProtectProwlarrSources {
		protectedHashes, err = w.getProtectedHashes(ctx, db, "prowlarr%")
		if err != nil {
			w.logger.Errorw("db_trim: failed to get Prowlarr-protected hashes", "error", err)
			return
		}
		w.logger.Infow("db_trim: Prowlarr protection active", "protected_hashes", len(protectedHashes))
	}

	// Collect import-protected info_hashes if enabled.
	if w.config.ProtectImportedSources {
		importHashes, importErr := w.getProtectedHashes(ctx, db, "import-%")
		if importErr != nil {
			w.logger.Errorw("db_trim: failed to get import-protected hashes", "error", importErr)
			return
		}
		if len(importHashes) > 0 {
			if protectedHashes == nil {
				protectedHashes = importHashes
			} else {
				for h := range importHashes {
					protectedHashes[h] = struct{}{}
				}
			}
			w.logger.Infow("db_trim: import protection active", "protected_hashes", len(importHashes))
		}
	}

	totalRemoved := 0
	for _, source := range sources {
		cfg := w.configForSource(source)
		if cfg.MaxAgeDays < 0 && cfg.MinSeeds < 0 {
			// Both dimensions disabled — nothing to trim for this source.
			continue
		}

		removed, err := w.trimSource(ctx, db, source, cfg, protectedHashes)
		if err != nil {
			w.logger.Errorw("db_trim: error trimming source", "source", source, "error", err)
			continue
		}
		totalRemoved += removed
	}

	if totalRemoved > 0 || w.config.DryRun {
		w.logger.Infow("db_trim: run complete", "total_removed", totalRemoved, "dry_run", w.config.DryRun)
	}
}

// configForSource returns the SourceTrimConfig for the given source key,
// falling back to "default" if no explicit entry exists.
func (w *trimWorker) configForSource(source string) SourceTrimConfig {
	var defaultCfg *SourceTrimConfig
	for _, cfg := range w.config.Sources {
		if cfg.Source == source {
			return cfg
		}
		if cfg.Source == "default" {
			c := cfg
			defaultCfg = &c
		}
	}
	if defaultCfg != nil {
		return *defaultCfg
	}
	// Ultimate fallback: everything disabled.
	return SourceTrimConfig{Source: source, MaxAgeDays: -1, MinSeeds: -1, IgnoreNoSeedData: true}
}

// getProtectedHashes returns a set of info_hashes that exist in any
// source matching the given LIKE pattern (e.g. "prowlarr%", "import-%").
func (w *trimWorker) getProtectedHashes(ctx context.Context, db *gorm.DB, pattern string) (map[string]struct{}, error) {
	var hashes []string
	if err := db.WithContext(ctx).
		Table("torrents_torrent_sources").
		Where("source LIKE ?", pattern).
		Distinct("info_hash").
		Pluck("info_hash", &hashes).Error; err != nil {
		return nil, err
	}
	m := make(map[string]struct{}, len(hashes))
	for _, h := range hashes {
		m[h] = struct{}{}
	}
	return m, nil
}

// trimSource runs the trim query for a single source and returns the number of
// rows removed (or that would be removed in dry-run mode).
func (w *trimWorker) trimSource(
	ctx context.Context,
	db *gorm.DB,
	source string,
	cfg SourceTrimConfig,
	protectedHashes map[string]struct{},
) (int, error) {
	// Build the WHERE clause.
	var conditions []string
	var args []interface{}

	conditions = append(conditions, "source = ?")
	args = append(args, source)

	if cfg.MaxAgeDays >= 0 {
		cutoff := time.Now().AddDate(0, 0, -cfg.MaxAgeDays)
		conditions = append(conditions, "created_at < ?")
		args = append(args, cutoff)
	}

	if cfg.MinSeeds >= 0 {
		if cfg.IgnoreNoSeedData {
			// Only trim if seeders IS NOT NULL AND seeders < threshold.
			conditions = append(conditions, "seeders IS NOT NULL AND seeders < ?")
		} else {
			// Treat NULL seeders as 0 — eligible for trim.
			conditions = append(conditions, "(seeders IS NULL OR seeders < ?)")
		}
		args = append(args, cfg.MinSeeds)
	}

	where := strings.Join(conditions, " AND ")

	// First, find the candidate info_hashes.
	var candidates []string
	if err := db.WithContext(ctx).
		Table("torrents_torrent_sources").
		Where(where, args...).
		Pluck("info_hash", &candidates).Error; err != nil {
		return 0, fmt.Errorf("query candidates: %w", err)
	}

	if len(candidates) == 0 {
		return 0, nil
	}

	// Filter out Prowlarr-protected hashes.
	if protectedHashes != nil {
		filtered := make([]string, 0, len(candidates))
		for _, h := range candidates {
			if _, protected := protectedHashes[h]; !protected {
				filtered = append(filtered, h)
			}
		}
		candidates = filtered
	}

	if len(candidates) == 0 {
		return 0, nil
	}

	// For multi-source torrents: only delete the source link, not the torrent.
	// The torrent itself is only deleted when ALL source links are gone.
	// We need to check if removing this source link would leave the torrent orphaned.
	//
	// Strategy: delete the source link. Then delete orphaned torrents (those with
	// no remaining source links). FK CASCADE handles torrent_files, torrent_contents, etc.

	if w.config.DryRun {
		w.logger.Infow("db_trim: dry run — would remove source links",
			"source", source,
			"count", len(candidates),
		)
		return len(candidates), nil
	}

	// Delete in batches to avoid huge transactions.
	// Use raw Exec instead of GORM's Delete to avoid model/nil issues with Table().
	const batchSize = 1000
	removed := 0
	for i := 0; i < len(candidates); i += batchSize {
		end := i + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[i:end]

		result := db.WithContext(ctx).Exec(
			"DELETE FROM torrents_torrent_sources WHERE source = ? AND info_hash IN ?",
			source, batch,
		)
		if result.Error != nil {
			return removed, fmt.Errorf("delete source links: %w", result.Error)
		}
		removed += int(result.RowsAffected)
	}

	// Clean up orphaned torrents (no remaining source links).
	orphanResult := db.WithContext(ctx).Exec(`
		DELETE FROM torrents
		WHERE info_hash IN (
			SELECT t.info_hash FROM torrents t
			LEFT JOIN torrents_torrent_sources tts ON t.info_hash = tts.info_hash
			WHERE tts.info_hash IS NULL
			AND t.info_hash IN ?
		)
	`, candidates)
	if orphanResult.Error != nil {
		w.logger.Errorw("db_trim: error cleaning orphaned torrents", "error", orphanResult.Error)
	} else if orphanResult.RowsAffected > 0 {
		w.logger.Infow("db_trim: removed orphaned torrents", "source", source, "count", orphanResult.RowsAffected)
	}

	w.logger.Infow("db_trim: trimmed source links",
		"source", source,
		"removed", removed,
	)
	return removed, nil
}
