package seedlookup

import "time"

// Config holds settings for the seed lookup worker.
// The worker queries the DHT for seeder/leecher counts on Prowlarr-imported
// torrents that lack seed data. Disabled by default; enable via
// SEED_LOOKUP_ENABLED=true (or seed_lookup.enabled in config.yml).
type Config struct {
	// Enabled controls whether the seed lookup worker starts.
	Enabled bool `yaml:"enabled"`
	// ConcurrentLookups is the number of parallel DHT queries in flight.
	ConcurrentLookups int `yaml:"concurrent_lookups"`
	// BackfillBatchSize is how many hashes the backfill scanner fetches per DB query.
	BackfillBatchSize int `yaml:"backfill_batch_size"`
	// BackfillPollInterval is how long to sleep when the backfill has no work.
	BackfillPollInterval time.Duration `yaml:"backfill_poll_interval"`
	// LookupTimeout is the per-hash timeout for the full DHT lookup sequence.
	LookupTimeout time.Duration `yaml:"lookup_timeout"`
	// BootstrapNodes is the list of DHT bootstrap nodes used when the DHT
	// crawler is not active and the seed lookup worker must maintain its own
	// routing table.
	BootstrapNodes []string `yaml:"bootstrap_nodes"`
}

func NewDefaultConfig() Config {
	return Config{
		Enabled:              false,
		ConcurrentLookups:    5,
		BackfillBatchSize:    100,
		BackfillPollInterval: 30 * time.Second,
		LookupTimeout:        15 * time.Second,
		BootstrapNodes: []string{
			"router.utorrent.com:6881",
			"router.bittorrent.com:6881",
			"dht.transmissionbt.com:6881",
			"dht.libtorrent.org:25401",
			"dht.aelitis.com:6881",
		},
	}
}
