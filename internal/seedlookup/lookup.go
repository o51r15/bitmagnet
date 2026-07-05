package seedlookup

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
)

// lookupResult holds the seed/leech counts for a single infohash.
type lookupResult struct {
	infoHash protocol.ID
	seeders  model.NullUint
	leechers model.NullUint
}

// lookupSeeds queries the DHT for seeder/leecher counts for the given infohash.
// It finds the closest nodes in the routing table, issues GetPeers to locate
// nodes that track the hash, then issues GetPeersScrape (BEP-33) to get the
// bloom filter estimates.
func (w *worker) lookupSeeds(ctx context.Context, infoHash protocol.ID) (lookupResult, error) {
	// Get the closest nodes from our routing table as starting points.
	closestNodes := w.kTable.GetClosestNodes(infoHash)
	if len(closestNodes) == 0 {
		return lookupResult{}, fmt.Errorf("no nodes in routing table")
	}

	// Phase 1: GetPeers — find nodes that have peer information for this hash.
	// We query the closest nodes and iteratively get closer. For seed lookup
	// we don't need to be exhaustive — a few responsive nodes are enough.
	var respondingAddrs []netip.AddrPort
	queried := make(map[string]struct{})
	peerAddrs := make(map[string]struct{}) // unique peer addresses seen

	// Seed the work queue with closest nodes (up to 8).
	candidates := make([]netip.AddrPort, 0, 8)
	for i, n := range closestNodes {
		if i >= 8 {
			break
		}
		candidates = append(candidates, n.Addr())
	}

	// Iterate up to 3 rounds to find nodes with peers.
	for round := 0; round < 3 && len(respondingAddrs) == 0; round++ {
		var nextCandidates []netip.AddrPort
		for _, addr := range candidates {
			key := addr.String()
			if _, ok := queried[key]; ok {
				continue
			}
			queried[key] = struct{}{}

			res, err := w.client.GetPeers(ctx, addr, infoHash)
			if err != nil {
				continue
			}
			// Record this node as responsive.
			w.kTable.BatchCommand(ktable.PutNode{
				ID:      res.ID,
				Addr:    addr,
				Options: []ktable.NodeOption{ktable.NodeResponded()},
			})

			if len(res.Values) > 0 {
				// This node has peers — it's a good candidate for scrape.
				respondingAddrs = append(respondingAddrs, addr)
				for _, peer := range res.Values {
					peerAddrs[peer.String()] = struct{}{}
				}
			}
			// Collect closer nodes for next round.
			for _, n := range res.Nodes {
				nextCandidates = append(nextCandidates, n.Addr)
			}
		}
		candidates = nextCandidates
	}

	// Count unique peers discovered during GetPeers as a floor estimate.
	peerCount := len(peerAddrs)
	w.logger.Infow("seed_lookup: GetPeers phase complete",
		"info_hash", infoHash.String(),
		"responding_nodes", len(respondingAddrs),
		"unique_peers", peerCount,
		"total_queried", len(queried))

	// Phase 2: GetPeersScrape (BEP-33) — get bloom filter based seed/leech estimates.
	// Try scraping from responding nodes first, fall back to closest nodes.
	scrapeTargets := respondingAddrs
	if len(scrapeTargets) == 0 {
		// No nodes had peers, but we can still try scraping closest nodes.
		for i, n := range closestNodes {
			if i >= 3 {
				break
			}
			scrapeTargets = append(scrapeTargets, n.Addr())
		}
	}

	scrapeErrors := 0
	for _, addr := range scrapeTargets {
		res, err := w.client.GetPeersScrape(ctx, addr, infoHash)
		if err != nil {
			scrapeErrors++
			continue
		}
		w.kTable.BatchCommand(ktable.PutNode{
			ID:      res.ID,
			Addr:    addr,
			Options: []ktable.NodeOption{ktable.NodeResponded()},
		})

		seeders := model.NewNullUint(uint(res.BfSeeders.ApproximatedSize()))
		leechers := model.NewNullUint(uint(res.BfPeers.ApproximatedSize()))

		w.logger.Infow("seed_lookup: BEP-33 scrape succeeded",
			"info_hash", infoHash.String(),
			"seeders", seeders.Uint,
			"leechers", leechers.Uint)

		return lookupResult{
			infoHash: infoHash,
			seeders:  seeders,
			leechers: leechers,
		}, nil
	}

	// No scrape succeeded — fall back to peer count from GetPeers.
	// If GetPeers found nodes with peers, use that as a floor estimate.
	w.logger.Infow("seed_lookup: BEP-33 scrape failed, using peer count fallback",
		"info_hash", infoHash.String(),
		"scrape_errors", scrapeErrors,
		"scrape_targets", len(scrapeTargets),
		"unique_peers_fallback", peerCount)

	return lookupResult{
		infoHash: infoHash,
		seeders:  model.NewNullUint(uint(peerCount)),
		leechers: model.NewNullUint(0),
	}, nil
}
