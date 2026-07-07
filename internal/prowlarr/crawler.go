package prowlarr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/classifier"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const defaultCrawlInterval = time.Hour

// CrawlNowFunc triggers an on-demand crawl of a specific Prowlarr indexer by ID.
// Exported so the GraphQL mutation resolver can call it.
type CrawlNowFunc func(indexerID int)

// indexerMeta caches the name and configured categories for an active indexer.
// Used by the on-demand trigger handler so it reproduces the same crawl
// parameters as the scheduled ticker loop.
type indexerMeta struct {
	name       string
	categories []int
}

type crawler struct {
	config        Config
	client        *prowlarrClient
	db            lazy.Lazy[*gorm.DB]
	imp           lazy.Lazy[importer.Importer]
	seedHotQueue  chan protocol.ID
	logger        *zap.SugaredLogger
	triggerChan   chan int
	stopped       chan struct{}
	knownIndexers map[int]indexerMeta // populated on start, used by trigger handler
}

func (c *crawler) start(ctx context.Context) {
	// Retry fetching indexers until Prowlarr is reachable. On a Pi where
	// container startup isn't perfectly sequenced, Prowlarr may not be ready
	// at the exact moment bitmagnet starts. Without this loop the per-indexer
	// ticker goroutines are never spawned and the crawler stays permanently
	// dead until the next container restart.
	var indexers []Indexer
	for {
		var err error
		indexers, err = c.client.getIndexers()
		if err == nil {
			break
		}
		c.logger.Warnw("prowlarr: failed to fetch indexers, retrying in 60s", "error", err)
		select {
		case <-c.stopped:
			return
		case <-time.After(60 * time.Second):
		}
	}

	// Index configured entries by ID for fast lookup
	configured := make(map[int]IndexerConfig)
	for _, ic := range c.config.Indexers {
		configured[ic.ID] = ic
	}

	// Build knownIndexers and start a ticker goroutine per enabled indexer.
	c.knownIndexers = make(map[int]indexerMeta)
	for _, idx := range indexers {
		if idx.Protocol != "torrent" || !idx.Enable {
			continue
		}
		ic, ok := configured[idx.ID]
		if !ok || !ic.Enabled {
			continue
		}
		c.knownIndexers[idx.ID] = indexerMeta{name: idx.Name, categories: ic.Categories}
	}

	// Startup hash compatibility check: probe each configured indexer with a
	// small search to confirm it returns infoHash values. Indexers that never
	// return hashes (private trackers, NZB-only sources) are skipped and a
	// WARN is logged so users can update their config. (Confirmed broken in
	// Session 10: Torrenting id:70, LimeTorrents id:15.)
	for id, meta := range c.knownIndexers {
		results, probeErr := c.client.search(id, meta.categories)
		if probeErr != nil {
			c.logger.Warnw("prowlarr: indexer probe failed, will still attempt crawls",
				"indexer_id", id, "name", meta.name, "error", probeErr)
			continue
		}
		hasHash := false
		for _, r := range results {
			if r.InfoHash != "" {
				hasHash = true
				break
			}
		}
		if !hasHash && len(results) > 0 {
			c.logger.Warnw("prowlarr: indexer returned no infoHash values, skipping"+
				" (NZB-only or private tracker — configure a torrent-capable indexer)",
				"indexer_id", id, "name", meta.name)
			delete(c.knownIndexers, id)
		}
	}

	// Start a ticker goroutine for each indexer that passed the hash check.
	for id, meta := range c.knownIndexers {
		interval := defaultCrawlInterval
		for _, ic := range c.config.Indexers {
			if ic.ID == id && ic.Interval > 0 {
				interval = ic.Interval
				break
			}
		}
		go c.runIndexerLoop(ctx, id, meta.name, meta.categories, interval)
	}

	// On-demand trigger loop — look up name and categories from knownIndexers
	// so triggered crawls use the same parameters as the scheduled ticker.
	go func() {
		for {
			select {
			case <-c.stopped:
				return
			case indexerID := <-c.triggerChan:
				meta := c.knownIndexers[indexerID]
				go c.crawlIndexer(ctx, indexerID, meta.name, meta.categories)
			}
		}
	}()
}

func (c *crawler) runIndexerLoop(ctx context.Context, indexerID int, indexerName string, categories []int, interval time.Duration) {
	// Crawl immediately on start, then repeat on interval
	c.crawlIndexer(ctx, indexerID, indexerName, categories)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopped:
			return
		case <-ticker.C:
			c.crawlIndexer(ctx, indexerID, indexerName, categories)
		}
	}
}

func (c *crawler) crawlIndexer(ctx context.Context, indexerID int, indexerName string, categories []int) {
	c.logger.Infow("prowlarr: crawling indexer", "indexer_id", indexerID)

	// Load high-water mark for this indexer
	lastSeen := c.loadLastSeen(indexerID)

	results, err := c.client.search(indexerID, categories)
	if err != nil {
		c.logger.Warnw("prowlarr: search failed", "indexer_id", indexerID, "error", err)
		return
	}

	// Track the newest publishDate across all results for state update
	var maxDate time.Time
	for _, r := range results {
		if r.PublishDate.After(maxDate) {
			maxDate = r.PublishDate
		}
	}

	// Filter to only results newer than last seen
	var newResults []SearchResult
	for _, r := range results {
		if r.PublishDate.After(lastSeen) {
			newResults = append(newResults, r)
		}
	}
	results = newResults

	imp, err := c.imp.Get()
	if err != nil {
		c.logger.Warnw("prowlarr: failed to get importer", "error", err)
		return
	}

	source := fmt.Sprintf("prowlarr-%d", indexerID)
	ai := imp.New(ctx, importer.Info{
		ID:              fmt.Sprintf("prowlarr-%d-%d", indexerID, time.Now().Unix()),
		ClassifierFlags: classifier.Flags{"tmdb_enabled": false},
	})

	imported := 0
	for _, r := range results {
		if r.InfoHash == "" {
			continue
		}
		id, parseErr := protocol.ParseID(strings.ToLower(r.InfoHash))
		if parseErr != nil {
			c.logger.Debugw("prowlarr: skipping result with invalid hash",
				"title", r.Title, "hash", r.InfoHash, "error", parseErr)
			continue
		}
		if importErr := ai.Import(importer.Item{
			Source:      source,
			SourceName:  indexerName,
			InfoHash:    id,
			Name:        r.Title,
			Size:        uint(max(r.Size, 0)), // guard: r.Size is int64; negative values wrap silently without this
			ContentType: contentTypeForCategories(r.Categories),
			PublishedAt: r.PublishDate,
		}); importErr != nil {
			c.logger.Warnw("prowlarr: import error", "error", importErr)
			break
		}
		imported++
		// Persist seeders/leechers from the indexer's search results directly
		// onto the Prowlarr source row. This is far more accurate than DHT-based
		// lookups because the indexer already has tracker-reported counts.
		if r.Seeders > 0 || r.Leechers > 0 {
			c.updateSeedCounts(source, id, r.Seeders, r.Leechers)
		}
	}

	ai.Drain()
	if closeErr := ai.Close(); closeErr != nil {
		c.logger.Warnw("prowlarr: import close error", "indexer_id", indexerID, "error", closeErr)
	}
	if !maxDate.IsZero() {
		c.saveLastSeen(indexerID, maxDate)
	}
	c.logger.Infow("prowlarr: crawl complete",
		"indexer_id", indexerID, "imported", imported, "new_results", len(results), "last_seen", maxDate)
}

// updateSeedCounts writes the indexer-reported seeders/leechers onto the
// Prowlarr source row in torrents_torrent_sources.
func (c *crawler) updateSeedCounts(source string, infoHash protocol.ID, seeders, leechers int) {
	d, err := c.db.Get()
	if err != nil {
		return
	}
	if updateErr := d.Exec(
		`UPDATE torrents_torrent_sources SET seeders = ?, leechers = ? WHERE info_hash = ? AND source = ?`,
		seeders, leechers, infoHash, source,
	).Error; updateErr != nil {
		c.logger.Debugw("prowlarr: failed to update seed counts",
			"info_hash", infoHash.String(), "error", updateErr)
	}
}

// loadLastSeen returns the last seen publishDate for an indexer, or zero time if unknown.
func (c *crawler) loadLastSeen(indexerID int) time.Time {
	d, err := c.db.Get()
	if err != nil {
		c.logger.Warnw("prowlarr: failed to get db for state load", "error", err)
		return time.Time{}
	}
	var lastSeen time.Time
	row := d.Raw(
		"SELECT last_seen_publish_date FROM prowlarr_indexer_state WHERE indexer_id = ?",
		indexerID,
	).Scan(&lastSeen)
	if row.Error != nil || row.RowsAffected == 0 {
		return time.Time{}
	}
	return lastSeen
}

// saveLastSeen updates the high-water mark publishDate for an indexer.
func (c *crawler) saveLastSeen(indexerID int, date time.Time) {
	d, err := c.db.Get()
	if err != nil {
		c.logger.Warnw("prowlarr: failed to get db for state save", "error", err)
		return
	}
	result := d.Exec(
		`INSERT INTO prowlarr_indexer_state (indexer_id, last_seen_publish_date)
		VALUES (?, ?)
		ON CONFLICT (indexer_id) DO UPDATE SET last_seen_publish_date = EXCLUDED.last_seen_publish_date
		WHERE prowlarr_indexer_state.last_seen_publish_date < EXCLUDED.last_seen_publish_date`,
		indexerID, date,
	)
	if result.Error != nil {
		c.logger.Warnw("prowlarr: failed to save indexer state", "indexer_id", indexerID, "error", result.Error)
	}
}
