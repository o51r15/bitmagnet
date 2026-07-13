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

// handleAnalyze accepts a multipart file upload, parses it, and returns
// format detection, category counts, and total row count.
func (h *handler) handleAnalyze(c *gin.Context) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file uploaded"})
		return
	}
	defer file.Close()

	// Read up to MaxUploadBytes + 1 to detect oversized files.
	lr := io.LimitReader(file, h.config.MaxUploadBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read file"})
		return
	}
	if int64(len(data)) > h.config.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("file exceeds maximum size of %d bytes", h.config.MaxUploadBytes),
		})
		return
	}

	result := Analyze(data)
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

	lr := io.LimitReader(file, h.config.MaxUploadBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read file"})
		return
	}
	if int64(len(data)) > h.config.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("file exceeds maximum size of %d bytes", h.config.MaxUploadBytes),
		})
		return
	}

	// Parse the file.
	format := DetectFormat(data)
	reader := strings.NewReader(string(data))
	var items []ParsedItem
	switch format {
	case FormatNDJSON:
		items, err = ParseNDJSON(reader)
	case FormatSQL:
		items, err = ParseSQL(reader)
	default:
		items, err = ParseCSV(reader)
	}
	if err != nil {
		h.logger.Warnw("dbimport: parse error", "format", format, "error", err)
	}
	if len(items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no valid torrents found in file"})
		return
	}

	// Build category filter set.
	catFilter := make(map[string]bool)
	for _, cat := range req.Categories {
		catFilter[strings.ToLower(strings.TrimSpace(cat))] = true
	}
	filterActive := len(catFilter) > 0

	// Get the importer.
	imp, err := h.importer.Get()
	if err != nil {
		h.logger.Errorw("dbimport: failed to get importer", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "importer unavailable"})
		return
	}

	sourceKey := "import-" + sourceName
	ai := imp.New(c.Request.Context(), importer.Info{
		ID: fmt.Sprintf("import-%s-%d", sourceName, time.Now().Unix()),
	})

	imported := 0
	skipped := 0
	for _, item := range items {
		// Apply category filter.
		if filterActive {
			catKey := ""
			if item.ContentType.Valid {
				catKey = string(item.ContentType.ContentType)
			} else {
				catKey = "unknown"
			}
			if !catFilter[catKey] {
				skipped++
				continue
			}
		}

		id, parseErr := protocol.ParseID(item.InfoHash)
		if parseErr != nil {
			skipped++
			continue
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

		if importErr := ai.Import(importItem); importErr != nil {
			h.logger.Warnw("dbimport: import error", "hash", item.InfoHash, "error", importErr)
			break
		}
		imported++
	}

	ai.Drain()
	if closeErr := ai.Close(); closeErr != nil {
		h.logger.Warnw("dbimport: close error", "error", closeErr)
	}

	h.logger.Infow("dbimport: import complete",
		"source", sourceKey,
		"imported", imported,
		"skipped", skipped,
		"total_parsed", len(items),
	)

	c.JSON(http.StatusOK, gin.H{
		"status":   "complete",
		"source":   sourceKey,
		"imported": imported,
		"skipped":  skipped,
	})
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
