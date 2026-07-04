package seedlookup

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
)

// runBootstrap maintains the DHT routing table when the DHT crawler is not
// active. It resolves bootstrap nodes, pings them, and runs find_node to
// populate the routing table with enough entries for seed lookups.
//
// If the DHT crawler is already running, this function returns immediately —
// the crawler handles all routing table maintenance.
func (w *worker) runBootstrap(ctx context.Context) {
	if w.dhtCrawlerActive.Get() {
		w.logger.Info("seed_lookup: DHT crawler active, skipping bootstrap")
		return
	}

	w.logger.Info("seed_lookup: DHT crawler inactive, starting own bootstrap")

	// Initial bootstrap + periodic reseed.
	go w.reseedBootstrapNodes(ctx)
	// Discover and maintain nodes via find_node.
	go w.runFindNodeLoop(ctx)
	// Health monitor — emergency reseed if routing table drains.
	go w.runHealthMonitor(ctx)
}

// reseedBootstrapNodes resolves and pings bootstrap nodes on startup and
// then periodically to keep the routing table populated.
func (w *worker) reseedBootstrapNodes(ctx context.Context) {
	interval := time.Duration(0)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			for _, strAddr := range w.config.BootstrapNodes {
				addr, err := net.ResolveUDPAddr("udp", strAddr)
				if err != nil {
					w.logger.Warnf("seed_lookup: failed to resolve bootstrap node: %s", err)
					continue
				}
				// Ping the bootstrap node to add it to the routing table.
				res, pingErr := w.client.Ping(ctx, addr.AddrPort())
				if pingErr != nil {
					w.logger.Infow("seed_lookup: bootstrap ping failed",
						"addr", strAddr, "error", pingErr)
					continue
				}
				w.kTable.BatchCommand(ktable.PutNode{
					ID:      res.ID,
					Addr:    addr.AddrPort(),
					Options: []ktable.NodeOption{ktable.NodeResponded()},
				})
			}
		}
		interval = time.Minute
	}
}

// runFindNodeLoop periodically picks random target IDs and runs find_node
// against the closest nodes in the routing table to discover new nodes.
// This is a much lighter version of the full crawler's find_node pipeline.
func (w *worker) runFindNodeLoop(ctx context.Context) {
	// Wait a few seconds for initial bootstrap pings to populate the table.
	w.sleepOrDone(ctx, 5*time.Second)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		target := protocol.RandomNodeID()
		nodes := w.kTable.GetClosestNodes(target)

		queried := 0
		for _, n := range nodes {
			if queried >= 3 {
				break
			}
			res, err := w.client.FindNode(ctx, n.Addr(), target)
			if err != nil {
				w.kTable.BatchCommand(ktable.DropNode{
					ID:     n.ID(),
					Reason: fmt.Errorf("seed_lookup find_node failed: %w", err),
				})
				continue
			}
			w.kTable.BatchCommand(ktable.PutNode{
				ID:      res.ID,
				Addr:    n.Addr(),
				Options: []ktable.NodeOption{ktable.NodeResponded()},
			})
			for _, discovered := range res.Nodes {
				w.kTable.BatchCommand(ktable.PutNode{
					ID:   discovered.ID,
					Addr: discovered.Addr,
				})
			}
			queried++
		}

		// Run find_node every 10 seconds — enough to keep the table warm
		// without generating excessive traffic.
		w.sleepOrDone(ctx, 10*time.Second)
	}
}

// runHealthMonitor watches the routing table and triggers an emergency
// bootstrap reseed if it has been empty for 30 seconds.
func (w *worker) runHealthMonitor(ctx context.Context) {
	const (
		checkInterval = 10 * time.Second
		dryThreshold  = 3
	)

	dryCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkInterval):
			nodes := w.kTable.GetNodesForSampleInfoHashes(1)
			if len(nodes) > 0 {
				dryCount = 0
				continue
			}

			dryCount++
			if dryCount < dryThreshold {
				continue
			}

			w.logger.Warnw("seed_lookup: routing table empty, emergency reseed",
				"dry_checks", dryCount)
			dryCount = 0

			for _, strAddr := range w.config.BootstrapNodes {
				addr, err := net.ResolveUDPAddr("udp", strAddr)
				if err != nil {
					continue
				}
				res, pingErr := w.client.Ping(ctx, addr.AddrPort())
				if pingErr != nil {
					continue
				}
				w.kTable.BatchCommand(ktable.PutNode{
					ID:      res.ID,
					Addr:    addr.AddrPort(),
					Options: []ktable.NodeOption{ktable.NodeResponded()},
				})
			}
		}
	}
}
