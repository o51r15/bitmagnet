package dhtcrawler

import (
	"context"
	"net"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
)

func (c *crawler) reseedBootstrapNodes(ctx context.Context) {
	interval := time.Duration(0)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
			for _, strAddr := range c.bootstrapNodes {
				addr, err := net.ResolveUDPAddr("udp", strAddr)
				if err != nil {
					c.logger.Warnf("failed to resolve bootstrap node address: %s", err)
					continue
				}
				select {
				case <-ctx.Done():
					return
				case c.nodesForPing.In() <- ktable.NewNode(ktable.ID{}, addr.AddrPort()):
					continue
				}
			}
		}

		interval = c.reseedBootstrapNodesInterval
	}
}

// runKtableHealthMonitor watches the routing table node count every 10 seconds.
// If the table has been empty for 3 consecutive checks (30 seconds), it triggers
// an emergency bootstrap reseed without waiting for the normal interval timer.
//
// This addresses the 24-hour crash pattern where a network interruption drains
// the ktable and the crawler sits idle until the next scheduled reseed fires.
// With a 1-minute reseed interval the maximum idle window is already reduced,
// but this monitor cuts recovery to ~30 seconds regardless of interval timing.
func (c *crawler) runKtableHealthMonitor(ctx context.Context) {
	const (
		checkInterval = 10 * time.Second
		dryThreshold  = 3 // 3 × 10s = 30s with no nodes before emergency reseed
	)

	dryCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkInterval):
			nodes := c.kTable.GetNodesForSampleInfoHashes(1)
			if len(nodes) > 0 {
				// Routing table is healthy — reset the dry counter.
				dryCount = 0
				continue
			}

			dryCount++
			if dryCount < dryThreshold {
				continue
			}

			// Routing table has been empty for dryThreshold consecutive checks.
			// Trigger an emergency reseed immediately.
			c.logger.Warnw("ktable empty — triggering emergency bootstrap reseed",
				"dry_checks", dryCount,
				"elapsed_seconds", int(checkInterval.Seconds())*dryCount,
			)
			dryCount = 0

			for _, strAddr := range c.bootstrapNodes {
				addr, err := net.ResolveUDPAddr("udp", strAddr)
				if err != nil {
					c.logger.Warnf("emergency reseed: failed to resolve bootstrap node: %s", err)
					continue
				}
				select {
				case <-ctx.Done():
					return
				case c.nodesForPing.In() <- ktable.NewNode(ktable.ID{}, addr.AddrPort()):
					continue
				}
			}
		}
	}
}
