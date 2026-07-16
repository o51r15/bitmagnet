package dbimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

// importJob tracks the state of an async import operation.
type importJob struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	SourceName string    `json:"sourceName"`
	Phase      string    `json:"phase"` // "uploading", "parsing", "importing", "complete", "failed"
	Total      int64     `json:"total"`
	Imported   int64     `json:"imported"`
	Skipped    int64     `json:"skipped"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type handler struct {
	config   Config
	importer lazy.Lazy[importer.Importer]
	logger   *zap.SugaredLogger

	// Singleton job — only one import at a time.
	mu         sync.Mutex
	currentJob *importJob
}

func (*handler) Key() string { return "dbimport_api" }

func (h *handler) Apply(e *gin.Engine) error {
	e.POST("/api/import/analyze", h.handleAnalyze)
	e.POST("/api/import/execute", h.handleExecute)
	e.GET("/api/import/status", h.handleStatus)
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

	// Peek first bytes to detect format.
	cr := &countingReader{r: file, limit: h.config.MaxUploadBytes}
	format, combined := DetectFormatFromReader(cr)

	if format == FormatSQLite {
		// SQLite requires random access; save to temp file.
		tmpPath, err := SaveToTemp(combined, h.config.MaxUploadBytes)
		if err != nil {
			if strings.Contains(err.Error(), "exceeds maximum size") {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
			}
			return
		}
		defer os.Remove(tmpPath)
		result := AnalyzeSQLite(tmpPath)
		c.JSON(http.StatusOK, result)
		return
	}

	result := AnalyzeStream(combined)
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

// handleExecute accepts a multipart upload, saves the file to disk, and
// launches an async background import. Returns immediately with a job ID.
// Only one import can run at a time.
func (h *handler) handleExecute(c *gin.Context) {
	// Check if an import is already running.
	h.mu.Lock()
	if h.currentJob != nil && (h.currentJob.Phase == "uploading" || h.currentJob.Phase == "parsing" || h.currentJob.Phase == "importing") {
		job := *h.currentJob
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("an import is already in progress (source: %s, %d imported so far)", job.Source, job.Imported),
			"job":   job,
		})
		return
	}
	h.mu.Unlock()

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

	// Validate source name.
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

	// Create the job before saving — this locks out other imports.
	now := time.Now()
	sourceKey := "import-" + sourceName
	jobID := fmt.Sprintf("import-%s-%d", sourceName, now.Unix())
	job := &importJob{
		ID:         jobID,
		Source:     sourceKey,
		SourceName: sourceName,
		Phase:      "uploading",
		StartedAt:  now,
		UpdatedAt:  now,
	}

	h.mu.Lock()
	// Double-check under lock.
	if h.currentJob != nil && (h.currentJob.Phase == "uploading" || h.currentJob.Phase == "parsing" || h.currentJob.Phase == "importing") {
		existing := *h.currentJob
		h.mu.Unlock()
		file.Close()
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("an import is already in progress (source: %s)", existing.Source),
			"job":   existing,
		})
		return
	}
	h.currentJob = job
	h.mu.Unlock()

	// Build category filter set.
	catFilter := make(map[string]bool)
	for _, cat := range req.Categories {
		catFilter[strings.ToLower(strings.TrimSpace(cat))] = true
	}

	// Save the uploaded file to a temp file so we can close the HTTP
	// connection and process asynchronously.
	cr := &countingReader{r: file, limit: h.config.MaxUploadBytes}
	tmpPath, saveErr := SaveToTemp(cr, h.config.MaxUploadBytes)
	file.Close()

	if saveErr != nil {
		h.mu.Lock()
		job.Phase = "failed"
		job.Error = saveErr.Error()
		job.UpdatedAt = time.Now()
		h.mu.Unlock()

		if strings.Contains(saveErr.Error(), "exceeds maximum size") {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": saveErr.Error()})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save uploaded file"})
		}
		return
	}

	h.logger.Infow("dbimport: file saved, starting async import",
		"source", sourceKey,
		"job_id", jobID,
		"file_size", cr.n,
	)

	// Launch the import in a background goroutine.
	go h.runImport(job, tmpPath, catFilter)

	c.JSON(http.StatusAccepted, gin.H{
		"status": "accepted",
		"job":    job,
	})
}

// runImport processes the saved temp file in the background.
func (h *handler) runImport(job *importJob, tmpPath string, catFilter map[string]bool) {
	defer os.Remove(tmpPath)

	// Detect format from the saved file.
	f, err := os.Open(tmpPath)
	if err != nil {
		h.setJobFailed(job, fmt.Sprintf("failed to open temp file: %v", err))
		return
	}

	format, combined := DetectFormatFromReader(f)
	// For non-SQLite formats, we'll read from combined (which includes the peeked bytes).
	// For SQLite, we close and use the file path directly.
	if format == FormatSQLite {
		f.Close()
	}

	h.mu.Lock()
	job.Phase = "parsing"
	job.UpdatedAt = time.Now()
	h.mu.Unlock()

	// Get the importer.
	imp, err := h.importer.Get()
	if err != nil {
		if format != FormatSQLite {
			f.Close()
		}
		h.setJobFailed(job, "importer unavailable")
		return
	}

	filterActive := len(catFilter) > 0

	ai := imp.New(context.Background(), importer.Info{
		ID: job.ID,
	})

	var imported, skipped int64
	var importErr atomic.Value // stores error

	callback := func(item ParsedItem) {
		if v := importErr.Load(); v != nil {
			return
		}

		if filterActive {
			catKey := "unknown"
			if item.ContentType.Valid {
				catKey = string(item.ContentType.ContentType)
			}
			if !catFilter[catKey] {
				atomic.AddInt64(&skipped, 1)
				return
			}
		}

		id, parseIDErr := protocol.ParseID(item.InfoHash)
		if parseIDErr != nil {
			atomic.AddInt64(&skipped, 1)
			return
		}

		importItem := importer.Item{
			Source:      job.Source,
			SourceName:  job.SourceName,
			InfoHash:    id,
			Name:        item.Name,
			Size:        item.Size,
			ContentType: item.ContentType,
			PublishedAt: item.PublishedAt,
		}

		if err := ai.Import(importItem); err != nil {
			h.logger.Warnw("dbimport: import error", "hash", item.InfoHash, "error", err)
			importErr.Store(err)
			return
		}

		newImported := atomic.AddInt64(&imported, 1)

		// Update job status periodically (every 1000 rows).
		if newImported%1000 == 0 {
			h.mu.Lock()
			job.Phase = "importing"
			job.Imported = newImported
			job.Skipped = atomic.LoadInt64(&skipped)
			job.UpdatedAt = time.Now()
			h.mu.Unlock()
		}
	}

	h.mu.Lock()
	job.Phase = "importing"
	job.UpdatedAt = time.Now()
	h.mu.Unlock()

	var parseErr error
	if format == FormatSQLite {
		parseErr = ParseSQLiteStream(tmpPath, callback)
	} else {
		switch format {
		case FormatNDJSON:
			parseErr = ParseNDJSONStream(combined, callback)
		case FormatSQL:
			parseErr = ParseSQLStream(combined, callback)
		default:
			parseErr = ParseCSVStream(combined, callback)
		}
		f.Close()
	}

	if parseErr != nil {
		h.logger.Warnw("dbimport: parse error", "format", format, "error", parseErr)
	}

	// Check if import had an error.
	if v := importErr.Load(); v != nil {
		ai.Drain()
		ai.Close()
		h.setJobFailed(job, fmt.Sprintf("import error: %v", v))
		return
	}

	finalImported := atomic.LoadInt64(&imported)
	finalSkipped := atomic.LoadInt64(&skipped)

	if finalImported == 0 {
		ai.Drain()
		ai.Close()
		h.setJobFailed(job, "no valid torrents found in file")
		return
	}

	ai.Drain()
	if closeErr := ai.Close(); closeErr != nil {
		h.logger.Warnw("dbimport: close error", "error", closeErr)
	}

	h.mu.Lock()
	job.Phase = "complete"
	job.Imported = finalImported
	job.Skipped = finalSkipped
	job.UpdatedAt = time.Now()
	h.mu.Unlock()

	h.logger.Infow("dbimport: import complete",
		"source", job.Source,
		"job_id", job.ID,
		"imported", finalImported,
		"skipped", finalSkipped,
		"duration", time.Since(job.StartedAt).String(),
	)
}

func (h *handler) setJobFailed(job *importJob, errMsg string) {
	h.mu.Lock()
	job.Phase = "failed"
	job.Error = errMsg
	job.UpdatedAt = time.Now()
	h.mu.Unlock()
	h.logger.Errorw("dbimport: import failed",
		"source", job.Source,
		"job_id", job.ID,
		"error", errMsg,
	)
}

// handleStatus returns the current import job status. Used for polling.
func (h *handler) handleStatus(c *gin.Context) {
	h.mu.Lock()
	job := h.currentJob
	h.mu.Unlock()

	if job == nil {
		c.JSON(http.StatusOK, gin.H{"active": false})
		return
	}

	h.mu.Lock()
	snapshot := *job
	h.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"active": snapshot.Phase == "uploading" || snapshot.Phase == "parsing" || snapshot.Phase == "importing",
		"job":    snapshot,
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
