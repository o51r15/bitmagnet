package dbtrim

// Config holds the DB trim settings.
// Trim is disabled by default; users must explicitly enable it and configure
// per-source rules to start purging data.
type Config struct {
	// Enabled is the master switch for the trim worker.
	Enabled bool `yaml:"enabled"`
	// Schedule is a cron expression controlling when trim runs (e.g. "0 3 * * *").
	Schedule string `yaml:"schedule"`
	// DryRun when true logs what would be removed without actually deleting.
	DryRun bool `yaml:"dry_run"`
	// ProtectProwlarrSources when true prevents trimming any torrent that also
	// exists in a Prowlarr source, regardless of other sources' trim rules.
	ProtectProwlarrSources bool `yaml:"protect_prowlarr_sources"`
	// Sources maps source keys (e.g. "dht", "prowlarr-20") to per-source trim rules.
	// The special key "default" applies to any source not explicitly listed.
	Sources map[string]SourceTrimConfig `yaml:"sources"`
}

// SourceTrimConfig defines trim thresholds for a single source.
// All thresholds default to -1 (disabled). Both MaxAgeDays and MinSeeds must
// be satisfied for a torrent to be trimmed (AND logic).
type SourceTrimConfig struct {
	// MaxAgeDays trims torrents older than this many days. -1 = disabled.
	MaxAgeDays int `yaml:"max_age_days"`
	// MinSeeds trims torrents with fewer seeders than this value. -1 = disabled.
	MinSeeds int `yaml:"min_seeds"`
	// IgnoreNoSeedData when true exempts torrents that have no seed data at all
	// from seed-based trim. Prevents purging entries that simply lack seed counts.
	IgnoreNoSeedData bool `yaml:"ignore_no_seed_data"`
}

// NewDefaultConfig returns a config with trim disabled and safe defaults.
func NewDefaultConfig() Config {
	return Config{
		Enabled:                false,
		Schedule:               "0 3 * * *",
		DryRun:                 false,
		ProtectProwlarrSources: true,
		Sources: map[string]SourceTrimConfig{
			"default": {
				MaxAgeDays:       -1,
				MinSeeds:         -1,
				IgnoreNoSeedData: true,
			},
		},
	}
}
