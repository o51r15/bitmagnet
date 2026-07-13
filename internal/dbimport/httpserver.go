package dbimport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Params struct {
	fx.In
	Config   Config
	Importer lazy.Lazy[importer.Importer]
	Logger   *zap.SugaredLogger
}

type Result struct {
	fx.Out
	Option httpserver.Option `group:"http_server_options"`
}

func NewHTTPServer(p Params) Result {
	return Result{
		Option: &handler{
			config:   p.Config,
			importer: p.Importer,
			logger:   p.Logger.Named("dbimport"),
		},
	}
}

type handler struct {
	config   Config
	importer lazy.Lazy[importer.Importer]
	logger   *zap.SugaredLogger
}

func (*handler) Key() string { return "dbimport_api" }

func (h *handler) Apply(e *gin.Engine) error {
	e.POST("/api/import/analyze", h.handleAnalyze)
	e.POST("/api/import/execute", h.handleExecute)
	return nil
}

// handleAnalyze accepts a multipart file upload and streams through it to
// return format detection, category counts, and total row count. The file
// is never loaded entirely into memory.
func (h *handler) handleAnalyze(c *gin.Context) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file uploaded"})
		return
	}
	defer file.Close()

	cr := &countingReader{r: file, limit: h.config.MaxUploadBytes}
	result := AnalyzeStream(cr)
	if cr.exceeded {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("file exceeds maximum size of %d bytes", h.config.MaxUploadBytes),
		})
		return
	}

	c.JSON(http.StatusOK, result)
}

type executeRequest struct {
	SourceName string   `json:"sourceName"`
	Categories []string `json:"categories"` // empty = all
}

// handleExecute accepts a multipart upload with a JSON "config" field and
// "file" field. It parses the file, filters by selected categories, and
// imports torrents using the importer pipeline with throttled classification.
func (h *handler) handleExecute(c *gin.Context) {
	// Parse the config JSON from the multipart form.
	configJSON := c.PostForm("config")
	if configJSON == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing config field"})
		return
	}

	var req executeRequest
	if err := json.Unmarshal([]byte(configJSON), &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config JSON"})
		return
	}

	// Validate source name: alphanumeric, hyphens, underscores only.
	sourceName := strings.TrimSpace(req.SourceName)
	if sourceName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sourceName is required"})
		return
	}
	if !isValidSourceName(sourceName) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "sourceName must contain only letters, numbers, hyphens, and underscores",
		})
		return
	}

	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file uploaded"})
		return
	}
	defer file.Close()

	// Get the importer before we start streaming.
	imp, err := h.importer.Get()
	if err != nil {
		h.logger.Errorw("dbimport: failed to get importer", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "importer unavailable"})
		return
	}

	// Build category filter set.
	catFilter := make(map[string]bool)
	for _, cat := range req.Categories {
		catFilter[strings.ToLower(strings.TrimSpace(cat))] = true
	}
	filterActive := len(catFilter) > 0

	sourceKey := "import-" + sourceName
	ai := imp.New(c.Request.Context(), importer.Info{
		ID: fmt.Sprintf("import-%s-%d", sourceName, time.Now().Unix()),
	})

	cr := &countingReader{r: file, limit: h.config.MaxUploadBytes}
	format, combined := DetectFormatFromReader(cr)

	var imported, skipped int
	var importErr error

	callback := func(item ParsedItem) {
		if importErr != nil || cr.exceeded {
			return
		}

		if filterActive {
			catKey := "unknown"
			if item.ContentType.Valid {
				catKey = string(item.ContentType.ContentType)
			}
			if !catFilter[catKey] {
				skipped++
				return
			}
		}

		id, parseIDErr := protocol.ParseID(item.InfoHash)
		if parseIDErr != nil {
			skipped++
			return
		}

		importItem := importer.Item{
			Source:      sourceKey,
			SourceName:  sourceName,
			InfoHash:    id,
			Name:        item.Name,
			Size:        item.Size,
			ContentType: item.ContentType,
			PublishedAt: item.PublishedAt,
		}

		if err := ai.Import(importItem); err != nil {
			h.logger.Warnw("dbimport: import error", "hash", item.InfoHash, "error", err)
			importErr = err
			return
		}
		imported++
	}

	var parseErr error
	switch format {
	case FormatNDJSON:
		parseErr = ParseNDJSONStream(combined, callback)
	case FormatSQL:
		parseErr = ParseSQLStream(combined, callback)
	default:
		parseErr = ParseCSVStream(combined, callback)
	}
	if parseErr != nil {
		h.logger.Warnw("dbimport: parse error", "format", format, "error", parseErr)
	}

	if cr.exceeded {
		ai.Drain()
		ai.Close()
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("file exceeds maximum size of %d bytes", h.config.MaxUploadBytes),
		})
		return
	}

	if imported == 0 {
		ai.Drain()
		ai.Close()
		c.JSON(http.StatusBadRequest, gin.H{"error": "no valid torrents found in file"})
		return
	}

	ai.Drain()
	if closeErr := ai.Close(); closeErr != nil {
		h.logger.Warnw("dbimport: close error", "error", closeErr)
	}

	h.logger.Infow("dbimport: import complete",
		"source", sourceKey,
		"imported", imported,
		"skipped", skipped,
	)

	c.JSON(http.StatusOK, gin.H{
		"status":   "complete",
		"source":   sourceKey,
		"imported": imported,
		"skipped":  skipped,
	})
}

// countingReader wraps a reader and tracks bytes read, setting exceeded=true
// if the limit is passed. After exceeding, reads return io.EOF.
type countingReader struct {
	r        io.Reader
	n        int64
	limit    int64
	exceeded bool
}

func (cr *countingReader) Read(p []byte) (int, error) {
	if cr.exceeded {
		return 0, io.EOF
	}
	n, err := cr.r.Read(p)
	cr.n += int64(n)
	if cr.n > cr.limit {
		cr.exceeded = true
		return n, io.EOF
	}
	return n, err
}

// isValidSourceName allows only safe characters for source keys.
func isValidSourceName(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return len(s) > 0 && len(s) <= 64
}
