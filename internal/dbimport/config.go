package dbimport

// Config holds settings for the database import feature.
type Config struct {
	// MaxUploadBytes limits the size of uploaded files. Default 25 GB.
	// Set to 0 to disable the limit.
	MaxUploadBytes int64 `yaml:"max_upload_bytes"`
	// ClassifyDelaySeconds is the delay applied to classification queue
	// jobs created during import. This throttles TMDB/OMDb API usage so
	// bulk imports don't overwhelm rate limits. Default 30 seconds.
	ClassifyDelaySeconds int `yaml:"classify_delay_seconds"`
	// ClassifyBatchSize controls how many info hashes are grouped into
	// each classification queue job. Default 50.
	ClassifyBatchSize int `yaml:"classify_batch_size"`
}

// NewDefaultConfig returns safe defaults for the import feature.
func NewDefaultConfig() Config {
	return Config{
		MaxUploadBytes:       25 * 1024 * 1024 * 1024, // 25 GB
		ClassifyDelaySeconds: 30,
		ClassifyBatchSize:    50,
	}
}
