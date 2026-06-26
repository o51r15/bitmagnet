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
)

const defaultCrawlInterval = time.Hour

// CrawlNowFunc triggers an on-demand crawl of a specific Prowlarr indexer by ID.
// Exported so the GraphQL mutation resolver can call it.
type CrawlNowFunc func(indexerID int)

type crawler struct {
	config      Config
	client      *prowlarrClient
	imp         lazy.Lazy[importer.Importer]
	logger      *zap.SugaredLogger
	triggerChan chan int
	stopped     chan struct{}
}

func (c *crawler) start(ctx context.Context) {
	indexers, err := c.client.getIndexers()
	if err != nil {
		c.logger.Warnw("prowlarr: failed to fetch indexers on startup", "error", err)
		return
	}

	// Index configured entries by ID for fast lookup
	configured := make(map[int]IndexerConfig)
	for _, ic := range c.config.Indexers {
		configured[ic.ID] = ic
	}

	// Start a ticker goroutine for each enabled torrent indexer
	for _, idx := range indexers {
		if idx.Protocol != "torrent" || !idx.Enable {
			continue
		}
		ic, ok := configured[idx.ID]
		if !ok || !ic.Enabled {
			continue
		}
		interval := ic.Interval
		if interval == 0 {
			interval = defaultCrawlInterval
		}
		go c.runIndexerLoop(ctx, idx.ID, ic.Categories, interval)
	}

	// On-demand trigger loop
	go func() {
		for {
			select {
			case <-c.stopped:
				return
			case indexerID := <-c.triggerChan:
				go c.crawlIndexer(ctx, indexerID, nil)
			}
		}
	}()
}

func (c *crawler) runIndexerLoop(ctx context.Context, indexerID int, categories []int, interval time.Duration) {
	// Crawl immediately on start, then repeat on interval
	c.crawlIndexer(ctx, indexerID, categories)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopped:
			return
		case <-ticker.C:
			c.crawlIndexer(ctx, indexerID, categories)
		}
	}
}

func (c *crawler) crawlIndexer(ctx context.Context, indexerID int, categories []int) {
	c.logger.Infow("prowlarr: crawling indexer", "indexer_id", indexerID)

	results, err := c.client.search(indexerID, categories)
	if err != nil {
		c.logger.Warnw("prowlarr: search failed", "indexer_id", indexerID, "error", err)
		return
	}

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
			InfoHash:    id,
			Name:        r.Title,
			Size:        uint(r.Size),
			ContentType: contentTypeForCategories(r.Categories),
			PublishedAt: r.PublishDate,
		}); importErr != nil {
			c.logger.Warnw("prowlarr: import error", "error", importErr)
			break
		}
		imported++
	}

	ai.Drain()
	if closeErr := ai.Close(); closeErr != nil {
		c.logger.Warnw("prowlarr: import close error", "indexer_id", indexerID, "error", closeErr)
	}
	c.logger.Infow("prowlarr: crawl complete",
		"indexer_id", indexerID, "imported", imported, "total_results", len(results))
}
