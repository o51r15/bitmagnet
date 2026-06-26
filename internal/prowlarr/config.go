package prowlarr

import (
	"github.com/bitmagnet-io/bitmagnet/internal/model"
)

// Config holds the Prowlarr integration settings.
// URL and APIKey must both be set to enable the integration.
// Indexers lists per-indexer crawl settings; only explicitly listed and
// enabled indexers are crawled.
type Config struct {
	URL      string          `yaml:"url"`
	APIKey   string          `yaml:"api_key"`
	Indexers []IndexerConfig `yaml:"indexers"`
}

// IndexerConfig defines crawl settings for one Prowlarr indexer.
// Crawl interval is fixed at defaultCrawlInterval (1h) for all indexers.
type IndexerConfig struct {
	ID         int   `yaml:"id"`
	Enabled    bool  `yaml:"enabled"`
	Categories []int `yaml:"categories"` // Newznab IDs; empty = all
}

// NewDefaultConfig returns an empty config (integration disabled until configured).
func NewDefaultConfig() Config {
	return Config{}
}

// newznabCategoryMap maps top-level Newznab category IDs to model.ContentType.
// Subcategories (e.g. 2040) resolve by rounding down to the parent (2000).
var newznabCategoryMap = map[int]model.ContentType{
	1000: model.ContentTypeSoftware, // Console
	2000: model.ContentTypeMovie,
	3000: model.ContentTypeMusic,
	4000: model.ContentTypeSoftware, // PC/Software
	5000: model.ContentTypeTvShow,
	6000: model.ContentTypeXxx,
	7000: model.ContentTypeEbook,
	// 8000 Other — no content type hint
}

// contentTypeForCategories returns the first matching ContentType from a
// Newznab category list, or an invalid NullContentType if no match is found.
func contentTypeForCategories(cats []Category) model.NullContentType {
	for _, cat := range cats {
		topLevel := (cat.ID / 1000) * 1000
		if ct, ok := newznabCategoryMap[topLevel]; ok {
			return model.NullContentType{Valid: true, ContentType: ct}
		}
	}
	return model.NullContentType{}
}
