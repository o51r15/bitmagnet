package seedlookup

import (
	"context"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/client"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const hotQueueCap = 1000

// worker is the seed lookup worker. It queries the DHT for seeder/leecher
// counts on torrents imported via Prowlarr that lack seed data.
type worker struct {
	config           Config
	client           client.Client
	kTable           ktable.Table
	db               lazy.Lazy[*gorm.DB]
	dhtCrawlerActive *concurrency.AtomicValue[bool]
	logger           *zap.SugaredLogger
	stopped          chan struct{}

	// hotQueue receives infohashes from new Prowlarr imports for immediate
	// seed lookup. Bounded to hotQueueCap; overflow falls through to the
	// backfill scanner naturally since the hash is already in the DB.
	hotQueue chan protocol.ID

	// backfillQueue is an internal channel between the backfill scanner and
	// the processing loop. Small buffer — the scanner controls pacing.
	backfillQueue chan protocol.ID
}

// HotQueue returns the channel that the Prowlarr crawler pushes new
// infohashes into for immediate seed lookup. Safe to call even when the
// worker is disabled — callers should check config.Enabled or use a
// non-blocking send.
func (w *worker) HotQueue() chan<- protocol.ID {
	return w.hotQueue
}

// start launches the seed lookup pipeline. It creates its own cancellable
// context and blocks until the stopped channel is closed (by OnStop).
func (w *worker) start() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.logger.Info("seed_lookup: starting")

	// Bootstrap the routing table if the DHT crawler isn't handling it.
	w.runBootstrap(ctx)

	// Wait for routing table to populate before starting lookups.
	w.sleepOrDone(ctx, 10*time.Second)

	// Start the backfill scanner.
	go w.runBackfill(ctx)

	// Start the priority processing loop (non-blocking, runs in goroutines).
	go w.runProcessingLoop(ctx)

	// Block until stopped.
	<-w.stopped
}

// runProcessingLoop processes hashes with hot queue priority over backfill.
// It runs ConcurrentLookups parallel workers that all draw from the same
// priority-aware source.
func (w *worker) runProcessingLoop(ctx context.Context) {
	var wg sync.WaitGroup

	for i := 0; i < w.config.ConcurrentLookups; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.processHashes(ctx)
		}()
	}

	// Block until context cancelled.
	<-ctx.Done()
	wg.Wait()
}

// processHashes is the per-worker goroutine that pulls hashes from the hot
// queue (priority) or backfill queue (secondary) and performs lookups.
func (w *worker) processHashes(ctx context.Context) {
	for {
		var infoHash protocol.ID
		var ok bool

		// Priority: hot queue first, then backfill.
		select {
		case <-ctx.Done():
			return
		case infoHash, ok = <-w.hotQueue:
			if !ok {
				return
			}
		default:
			// Hot queue empty — try backfill, but also keep checking hot queue.
			select {
			case <-ctx.Done():
				return
			case infoHash, ok = <-w.hotQueue:
				if !ok {
					return
				}
			case infoHash, ok = <-w.backfillQueue:
				if !ok {
					return
				}
			}
		}

		// Run the lookup with a per-hash timeout.
		lookupCtx, cancel := context.WithTimeout(ctx, w.config.LookupTimeout)
		result, err := w.lookupSeeds(lookupCtx, infoHash)
		cancel()

		if err != nil {
			w.logger.Debugw("seed_lookup: lookup failed",
				"info_hash", infoHash.String(), "error", err)
			continue
		}

		if persistErr := w.persistResult(ctx, result); persistErr != nil {
			w.logger.Warnw("seed_lookup: persist failed",
				"info_hash", infoHash.String(), "error", persistErr)
		} else {
			w.logger.Debugw("seed_lookup: persisted",
				"info_hash", infoHash.String(),
				"seeders", result.seeders.Uint,
				"leechers", result.leechers.Uint)
		}
	}
}
