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

	totalRemoved := 0
	for _, source := range sources {
		cfg := w.configForSource(source)
		if cfg.MaxAgeDays < 0 && cfg.MinSeeds < 0 {
			// Both dimensions disabled -- nothing to trim for this source.
			continue
		}

		removed, err := w.trimSource(ctx, db, source, cfg)
		if err != nil {
			w.logger.Errorw("db_trim: error trimming source", "source", source, "error", err)
			continue
		}
		totalRemoved += removed
	}

	// Reap torrents whose last source link was removed. One statement per run
	// so it is atomic and also self-heals orphans left by an interrupted run.
	if !w.config.DryRun && totalRemoved > 0 {
		orphanResult := db.WithContext(ctx).Exec(`
			DELETE FROM torrents
			WHERE NOT EXISTS (
				SELECT 1 FROM torrents_torrent_sources tts
				WHERE tts.info_hash = torrents.info_hash
			)`)
		if orphanResult.Error != nil {
			w.logger.Errorw("db_trim: error cleaning orphaned torrents", "error", orphanResult.Error)
		} else if orphanResult.RowsAffected > 0 {
			w.logger.Infow("db_trim: removed orphaned torrents", "count", orphanResult.RowsAffected)
		}
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

// trimSource runs the trim query for a single source and returns the number of
// rows removed (or that would be removed in dry-run mode).
func (w *trimWorker) trimSource(
	ctx context.Context,
	db *gorm.DB,
	source string,
	cfg SourceTrimConfig,
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
			// Treat NULL seeders as 0 -- eligible for trim.
			conditions = append(conditions, "(seeders IS NULL OR seeders < ?)")
		}
		args = append(args, cfg.MinSeeds)
	}

	// Protection: never trim a source link whose info_hash also exists in a
	// protected source (Prowlarr or imported). Evaluated server-side via a
	// NOT EXISTS anti-join so hash sets are never pulled into memory.
	var protectPatterns []string
	if w.config.ProtectProwlarrSources {
		protectPatterns = append(protectPatterns, "prowlarr%")
	}
	if w.config.ProtectImportedSources {
		protectPatterns = append(protectPatterns, "import-%")
	}
	if len(protectPatterns) > 0 {
		likeClauses := make([]string, 0, len(protectPatterns))
		for _, p := range protectPatterns {
			likeClauses = append(likeClauses, "p.source LIKE ?")
			args = append(args, p)
		}
		conditions = append(conditions,
			"NOT EXISTS (SELECT 1 FROM torrents_torrent_sources p"+
				" WHERE p.info_hash = torrents_torrent_sources.info_hash AND ("+
				strings.Join(likeClauses, " OR ")+"))")
	}

	where := strings.Join(conditions, " AND ")

	// Dry run: count what would be removed, delete nothing.
	if w.config.DryRun {
		var count int64
		if err := db.WithContext(ctx).
			Raw("SELECT COUNT(*) FROM torrents_torrent_sources WHERE "+where, args...).
			Scan(&count).Error; err != nil {
			return 0, fmt.Errorf("count candidates: %w", err)
		}
		if count > 0 {
			w.logger.Infow("db_trim: dry run -- would remove source links",
				"source", source, "count", count)
		}
		return int(count), nil
	}

	// Delete in bounded batches (by ctid) so each statement is a small
	// transaction rather than one giant delete. Orphaned torrents are reaped
	// once per run by the caller.
	const batchSize = 1000
	removed := 0
	for {
		batchArgs := append(append([]interface{}{}, args...), batchSize)
		result := db.WithContext(ctx).Exec(
			"DELETE FROM torrents_torrent_sources WHERE ctid IN"+
				" (SELECT ctid FROM torrents_torrent_sources WHERE "+where+" LIMIT ?)",
			batchArgs...,
		)
		if result.Error != nil {
			return removed, fmt.Errorf("delete source links: %w", result.Error)
		}
		removed += int(result.RowsAffected)
		if result.RowsAffected < int64(batchSize) {
			break
		}
	}

	if removed > 0 {
		w.logger.Infow("db_trim: trimmed source links", "source", source, "removed", removed)
	}
	return removed, nil
}
