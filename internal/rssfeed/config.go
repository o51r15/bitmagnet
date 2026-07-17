package rssfeed

import "time"

// Config holds RSS feed integration settings.
// Feeds lists per-feed poll settings; only explicitly listed and enabled feeds are polled.
type Config struct {
	Feeds []FeedConfig `yaml:"feeds"`
}

// FeedConfig defines settings for one RSS/Torznab feed.
// Interval controls how often the feed is polled; empty or "0" uses defaultPollInterval (15m).
// Stored as string to avoid mapstructure decode issues with time.Duration in nested slices.
type FeedConfig struct {
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Enabled  bool   `yaml:"enabled"`
	Interval string `yaml:"interval"` // e.g. "15m", "1h", "30s"
}

// ParseInterval returns the feed's poll interval as a time.Duration.
// Returns defaultPollInterval if the interval is empty or unparseable.
func (f FeedConfig) ParseInterval(defaultInterval time.Duration) time.Duration {
	if f.Interval == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(f.Interval)
	if err != nil || d <= 0 {
		return defaultInterval
	}
	return d
}

// NewDefaultConfig returns an empty config (no feeds configured).
func NewDefaultConfig() Config {
	return Config{}
}
