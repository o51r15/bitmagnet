package rssfeed

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const defaultPollInterval = 15 * time.Minute

// PollNowFunc triggers an on-demand poll of a specific feed by name.
type PollNowFunc func(feedName string)

type poller struct {
	config      Config
	httpClient  *http.Client
	db          lazy.Lazy[*gorm.DB]
	imp         lazy.Lazy[importer.Importer]
	logger      *zap.SugaredLogger
	triggerChan chan string
	stopped     chan struct{}

	inflightMu sync.Mutex
	inflight   map[string]bool
}

func newPoller(config Config, db lazy.Lazy[*gorm.DB], imp lazy.Lazy[importer.Importer], logger *zap.SugaredLogger) *poller {
	// Force IPv4 for VPN/gluetun compatibility (same as Prowlarr client)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
	}
	return &poller{
		config:      config,
		httpClient:  &http.Client{Timeout: 60 * time.Second, Transport: transport},
		db:          db,
		imp:         imp,
		logger:      logger,
		triggerChan: make(chan string, 10),
		stopped:     make(chan struct{}),
		inflight:    make(map[string]bool),
	}
}

func (p *poller) start(ctx context.Context) {
	for _, feed := range p.config.Feeds {
		if !feed.Enabled || feed.URL == "" {
			continue
		}
		interval := feed.ParseInterval(defaultPollInterval)
		go p.runFeedLoop(ctx, feed, interval)
	}

	// On-demand trigger loop
	go func() {
		for {
			select {
			case <-p.stopped:
				return
			case name := <-p.triggerChan:
				for _, feed := range p.config.Feeds {
					if feed.Name == name && feed.Enabled {
						go p.pollFeed(ctx, feed)
						break
					}
				}
			}
		}
	}()
}

func (p *poller) runFeedLoop(ctx context.Context, feed FeedConfig, interval time.Duration) {
	p.pollFeed(ctx, feed)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopped:
			return
		case <-ticker.C:
			p.pollFeed(ctx, feed)
		}
	}
}

func (p *poller) pollFeed(ctx context.Context, feed FeedConfig) {
	source := feedSourceKey(feed.Name)

	// In-flight guard (same pattern as Prowlarr)
	p.inflightMu.Lock()
	if p.inflight[source] {
		p.inflightMu.Unlock()
		p.logger.Debugw("rssfeed: poll already in progress, skipping", "feed", feed.Name)
		return
	}
	p.inflight[source] = true
	p.inflightMu.Unlock()
	defer func() {
		p.inflightMu.Lock()
		delete(p.inflight, source)
		p.inflightMu.Unlock()
	}()

	p.logger.Infow("rssfeed: polling feed", "feed", feed.Name, "url", feed.URL)

	// Load high-water mark
	lastSeen := p.loadLastSeen(feed.Name)

	// Fetch the feed
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.URL, nil)
	if err != nil {
		p.logger.Warnw("rssfeed: failed to create request", "feed", feed.Name, "error", err)
		return
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		p.logger.Warnw("rssfeed: fetch failed", "feed", feed.Name, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.logger.Warnw("rssfeed: non-200 response", "feed", feed.Name, "status", resp.StatusCode)
		return
	}

	// Parse
	items, err := ParseFeed(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		p.logger.Warnw("rssfeed: parse failed", "feed", feed.Name, "error", err)
		return
	}

	// Track newest publishDate
	var maxDate time.Time
	for _, item := range items {
		if item.PublishedAt.After(maxDate) {
			maxDate = item.PublishedAt
		}
	}

	// Filter to items after high-water mark (>= for boundary safety, same as Prowlarr)
	var newItems []FeedItem
	for _, item := range items {
		if !item.PublishedAt.Before(lastSeen) {
			newItems = append(newItems, item)
		}
	}

	imp, err := p.imp.Get()
	if err != nil {
		p.logger.Warnw("rssfeed: failed to get importer", "error", err)
		return
	}

	ai := imp.New(ctx, importer.Info{
		ID: fmt.Sprintf("rssfeed-%s-%d", feed.Name, time.Now().Unix()),
	})

	imported := 0
	for _, item := range newItems {
		id, parseErr := protocol.ParseID(strings.ToLower(item.InfoHash))
		if parseErr != nil {
			p.logger.Debugw("rssfeed: skipping item with invalid hash",
				"title", item.Title, "hash", item.InfoHash, "error", parseErr)
			continue
		}
		if importErr := ai.Import(importer.Item{
			Source:      source,
			SourceName:  feed.Name,
			InfoHash:    id,
			Name:        item.Title,
			Size:        uint(max64(item.Size, 0)),
			PublishedAt: item.PublishedAt,
		}); importErr != nil {
			p.logger.Warnw("rssfeed: import error", "error", importErr)
			break
		}
		imported++
	}

	ai.Drain()
	if closeErr := ai.Close(); closeErr != nil {
		p.logger.Warnw("rssfeed: import close error", "feed", feed.Name, "error", closeErr)
	}

	// Apply seed counts after import (same pattern as Prowlarr)
	p.applySeedCounts(source, newItems)

	if !maxDate.IsZero() {
		p.saveLastSeen(feed.Name, maxDate)
	}
	p.logger.Infow("rssfeed: poll complete",
		"feed", feed.Name, "imported", imported, "total_items", len(items), "new_items", len(newItems))
}

func (p *poller) applySeedCounts(source string, items []FeedItem) {
	var updates []struct {
		id       protocol.ID
		seeders  int
		leechers int
	}
	for _, item := range items {
		if item.Seeders <= 0 && item.Leechers <= 0 {
			continue
		}
		id, err := protocol.ParseID(strings.ToLower(item.InfoHash))
		if err != nil {
			continue
		}
		updates = append(updates, struct {
			id       protocol.ID
			seeders  int
			leechers int
		}{id, item.Seeders, item.Leechers})
	}
	if len(updates) == 0 {
		return
	}
	d, err := p.db.Get()
	if err != nil {
		return
	}
	txErr := d.Transaction(func(tx *gorm.DB) error {
		for _, su := range updates {
			if err := tx.Exec(
				`UPDATE torrents_torrent_sources SET seeders = ?, leechers = ? WHERE info_hash = ? AND source = ?`,
				su.seeders, su.leechers, su.id, source,
			).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		p.logger.Debugw("rssfeed: failed to apply seed counts", "source", source, "error", txErr)
	}
}

func (p *poller) loadLastSeen(feedName string) time.Time {
	d, err := p.db.Get()
	if err != nil {
		return time.Time{}
	}
	var lastSeen time.Time
	row := d.Raw(
		"SELECT last_seen_publish_date FROM rss_feed_state WHERE feed_name = ?",
		feedName,
	).Scan(&lastSeen)
	if row.Error != nil || row.RowsAffected == 0 {
		return time.Time{}
	}
	return lastSeen
}

func (p *poller) saveLastSeen(feedName string, date time.Time) {
	d, err := p.db.Get()
	if err != nil {
		return
	}
	result := d.Exec(
		`INSERT INTO rss_feed_state (feed_name, last_seen_publish_date)
		VALUES (?, ?)
		ON CONFLICT (feed_name) DO UPDATE SET last_seen_publish_date = EXCLUDED.last_seen_publish_date
		WHERE rss_feed_state.last_seen_publish_date < EXCLUDED.last_seen_publish_date`,
		feedName, date,
	)
	if result.Error != nil {
		p.logger.Warnw("rssfeed: failed to save feed state", "feed", feedName, "error", result.Error)
	}
}

func feedSourceKey(name string) string {
	return "rss-" + name
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
