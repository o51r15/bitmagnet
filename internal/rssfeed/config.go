package rssfeed

import "time"

// Config holds RSS feed integration settings.
// Feeds lists per-feed poll settings; only explicitly listed and enabled feeds are polled.
type Config struct {
	Feeds []FeedConfig `yaml:"feeds"`
}

// FeedConfig defines settings for one RSS/Torznab feed.
// Interval controls how often the feed is polled; 0 uses defaultPollInterval (15m).
type FeedConfig struct {
	Name     string        `yaml:"name"`
	URL      string        `yaml:"url"`
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"` // 0 = default (15m)
}

// NewDefaultConfig returns an empty config (no feeds configured).
func NewDefaultConfig() Config {
	return Config{}
}
