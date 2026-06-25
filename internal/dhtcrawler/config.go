package dhtcrawler

import (
	"time"
)

type Config struct {
	// ScalingFactor is a rough proxy for resource usage of the crawler; concurrency and buffer size of the various
	// pipeline channels are multiplied by this value. Diminishing returns may result from exceeding the
	// default value of 10. Since the software has not been tested on a wide variety of hardware and network
	// conditions; your mileage may vary here...
	ScalingFactor                uint
	BootstrapNodes               []string
	ReseedBootstrapNodesInterval time.Duration
	// SaveFilesThreshold specifies a maximum number of files in a torrent before file information is discarded.
	// Some torrents contain thousands of files which can severely impact performance and uses a lot of disk space.
	SaveFilesThreshold uint
	// SavePieces when true, torrent pieces will be persisted to the database.
	// The pieces take up quite a lot of space, and aren't currently very useful,
	// but they may be used by future features.
	SavePieces bool
	// RescrapeThreshold is the amount of time that must pass before a torrent is rescraped
	// to count seeders and leechers.
	RescrapeThreshold time.Duration
	// MaxQueueDepth is the maximum number of pending+retry queue jobs before the crawler
	// stops adding new classification jobs. 0 disables the check. When the threshold is
	// reached, torrents are still written to the DB but skipped for classification until
	// the queue drains. Prevents unbounded queue_jobs table growth under TMDB rate limits.
	MaxQueueDepth uint
}

func NewDefaultConfig() Config {
	return Config{
		ScalingFactor:                10,
		BootstrapNodes:               defaultBootstrapNodes,
		ReseedBootstrapNodesInterval: time.Minute,
		SaveFilesThreshold:           100,
		SavePieces:                   false,
		RescrapeThreshold:            time.Hour * 24 * 30,
		MaxQueueDepth:                50000,
	}
}

// defaultBootstrapNodes is the list of well-known DHT bootstrap nodes used to
// seed the routing table on startup. Updated to remove stale/unreliable nodes
// and add current alternatives. (PR #454)
//
// dht.anacrolix.link and router.silotis.us have been observed to produce
// frequent "failed to resolve" warnings and cause slow cold-start routing.
// The replacements below are actively maintained and more reliable.
var defaultBootstrapNodes = []string{
	"router.utorrent.com:6881",
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"dht.libtorrent.org:25401",
	"dht.aelitis.com:6881",
	// router.bittorrent.cloud removed — does not resolve through VPN/gluetun DNS
}
