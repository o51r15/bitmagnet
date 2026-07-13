package dbimport

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
)

// Format identifies a recognized import file format.
type Format string

const (
	FormatCSV    Format = "csv"
	FormatNDJSON Format = "ndjson"
	FormatSQL    Format = "sql"
)

// ParsedItem represents a single torrent extracted from an import file.
type ParsedItem struct {
	InfoHash    string
	Name        string
	Size        uint
	ContentType model.NullContentType
	PublishedAt time.Time
	Seeders     int
	Leechers    int
}

// AnalysisResult summarizes a parsed import file.
type AnalysisResult struct {
	Format     Format            `json:"format"`
	TotalRows  int               `json:"totalRows"`
	Categories map[string]int    `json:"categories"` // content_type -> count
	SampleRows []ParsedItem      `json:"-"`
	Errors     int               `json:"errors"`
}

// DetectFormat reads the first few bytes of data to guess the format.
// Works on both full data and small prefixes.
func DetectFormat(data []byte) Format {
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return FormatCSV // fallback
	}
	// NDJSON: first non-empty line starts with {
	if trimmed[0] == '{' {
		return FormatNDJSON
	}
	// SQL: starts with common SQL keywords
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "INSERT") ||
		strings.HasPrefix(upper, "CREATE") ||
		strings.HasPrefix(upper, "-- ") ||
		strings.HasPrefix(upper, "BEGIN") {
		return FormatSQL
	}
	return FormatCSV
}

// DetectFormatFromReader peeks the first 4KB of a reader to detect format,
// then returns the format and a new reader that replays the peeked bytes.
func DetectFormatFromReader(r io.Reader) (Format, io.Reader) {
	peek := make([]byte, 4096)
	n, _ := io.ReadFull(r, peek)
	peek = peek[:n]
	format := DetectFormat(peek)
	// Create a reader that replays the peeked bytes followed by the rest.
	combined := io.MultiReader(strings.NewReader(string(peek)), r)
	return format, combined
}

// validInfoHash checks if a string is a valid 40-char hex info hash.
var hexHashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func validInfoHash(s string) bool {
	return hexHashRe.MatchString(s)
}

// parseContentType converts a string to a NullContentType.
// Accepts both bitmagnet names ("movie", "tv_show") and common
// labels ("Movies", "TV", "Music", etc.).
func parseContentType(s string) model.NullContentType {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "movie", "movies":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeMovie}
	case "tv_show", "tv", "tv shows", "television":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeTvShow}
	case "music", "audio":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeMusic}
	case "ebook", "ebooks", "book", "books":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeEbook}
	case "comic", "comics":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeComic}
	case "audiobook", "audiobooks":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeAudiobook}
	case "game", "games":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeGame}
	case "software", "apps", "application", "applications":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeSoftware}
	case "xxx", "adult", "porn":
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeXxx}
	default:
		return model.NullContentType{}
	}
}

// ParseCSVStream reads a CSV file, calling fn for each valid item.
// Streams line-by-line without accumulating all items in memory.
func ParseCSVStream(r io.Reader, fn func(ParsedItem)) error {
	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = false // need stable records for callback

	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("reading CSV header: %w", err)
	}

	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	hashCol := -1
	for _, candidate := range []string{"info_hash", "infohash", "hash", "btih"} {
		if idx, ok := colIdx[candidate]; ok {
			hashCol = idx
			break
		}
	}

	// Auto-detect hash column from first data row if no header match.
	var peekedRow []string
	if hashCol < 0 {
		peek, peekErr := cr.Read()
		if peekErr != nil {
			return fmt.Errorf("CSV has no recognizable info_hash column and no data rows")
		}
		for i, val := range peek {
			if validInfoHash(strings.TrimSpace(val)) {
				hashCol = i
				break
			}
		}
		if hashCol < 0 {
			return fmt.Errorf("CSV has no recognizable info_hash column (tried: info_hash, infohash, hash, btih)")
		}
		peekedRow = peek
	}

	findCol := func(names ...string) int {
		for _, n := range names {
			if idx, ok := colIdx[n]; ok {
				return idx
			}
		}
		return -1
	}
	nameCol := findCol("name", "title", "torrent_name")
	sizeCol := findCol("size", "length", "total_size")
	catCol := findCol("category", "content_type", "type", "cat")
	seedCol := findCol("seeders", "seeds", "seed")
	leechCol := findCol("leechers", "leech", "peers")
	dateCol := findCol("date", "published", "published_at", "added", "created_at")

	getField := func(record []string, idx int) string {
		if idx >= 0 && idx < len(record) {
			return strings.TrimSpace(record[idx])
		}
		return ""
	}

	parseRow := func(record []string) (ParsedItem, bool) {
		hash := strings.ToLower(getField(record, hashCol))
		if !validInfoHash(hash) {
			return ParsedItem{}, false
		}
		item := ParsedItem{InfoHash: hash}
		if v := getField(record, nameCol); v != "" {
			item.Name = v
		}
		if v := getField(record, sizeCol); v != "" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				item.Size = uint(n)
			}
		}
		if v := getField(record, catCol); v != "" {
			item.ContentType = parseContentType(v)
		}
		if v := getField(record, seedCol); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				item.Seeders = n
			}
		}
		if v := getField(record, leechCol); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				item.Leechers = n
			}
		}
		if v := getField(record, dateCol); v != "" {
			for _, layout := range []string{
				time.RFC3339,
				"2006-01-02 15:04:05",
				"2006-01-02",
			} {
				if t, err := time.Parse(layout, v); err == nil {
					item.PublishedAt = t
					break
				}
			}
		}
		return item, true
	}

	if peekedRow != nil {
		if item, ok := parseRow(peekedRow); ok {
			fn(item)
		}
	}

	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if item, ok := parseRow(record); ok {
			fn(item)
		}
	}
	return nil
}

// ParseCSV reads a CSV file into a slice (convenience wrapper).
func ParseCSV(r io.Reader) ([]ParsedItem, error) {
	var items []ParsedItem
	err := ParseCSVStream(r, func(item ParsedItem) {
		items = append(items, item)
	})
	return items, err
}

// ndjsonItem is the JSON structure accepted from NDJSON files.
// Fields mirror importer.Item but we only decode what we need.
type ndjsonItem struct {
	InfoHash    string `json:"InfoHash"`
	Source      string `json:"Source"`
	Name        string `json:"Name"`
	Size        uint   `json:"Size"`
	ContentType string `json:"ContentType"`
	Seeders     int    `json:"Seeders"`
	Leechers    int    `json:"Leechers"`
	PublishedAt string `json:"PublishedAt"`
}

// ParseNDJSONStream reads newline-delimited JSON, calling fn for each valid item.
func ParseNDJSONStream(r io.Reader, fn func(ParsedItem)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw ndjsonItem
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(raw.InfoHash))
		if !validInfoHash(hash) {
			continue
		}
		item := ParsedItem{
			InfoHash: hash,
			Name:     raw.Name,
			Size:     raw.Size,
			Seeders:  raw.Seeders,
			Leechers: raw.Leechers,
		}
		if raw.ContentType != "" {
			item.ContentType = parseContentType(raw.ContentType)
		}
		if raw.PublishedAt != "" {
			if t, err := time.Parse(time.RFC3339, raw.PublishedAt); err == nil {
				item.PublishedAt = t
			}
		}
		fn(item)
	}
	return scanner.Err()
}

// ParseNDJSON reads newline-delimited JSON into a slice (convenience wrapper).
func ParseNDJSON(r io.Reader) ([]ParsedItem, error) {
	var items []ParsedItem
	err := ParseNDJSONStream(r, func(item ParsedItem) {
		items = append(items, item)
	})
	return items, err
}

// sqlInsertRe matches INSERT INTO ... VALUES lines and extracts the values portion.
var sqlInsertRe = regexp.MustCompile(`(?i)INSERT\s+INTO\s+\S+\s*(?:\([^)]*\))?\s*VALUES\s*(.+);?\s*$`)

// sqlValueRe extracts individual value tuples from an INSERT VALUES clause.
var sqlValueRe = regexp.MustCompile(`\(([^)]+)\)`)

// ParseSQLStream extracts torrent data from SQL INSERT statements, calling fn for each.
func ParseSQLStream(r io.Reader, fn func(ParsedItem)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10 MB lines for big INSERTs

	for scanner.Scan() {
		line := scanner.Text()
		match := sqlInsertRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		tuples := sqlValueRe.FindAllStringSubmatch(match[1], -1)
		for _, tuple := range tuples {
			if len(tuple) < 2 {
				continue
			}
			fields := strings.Split(tuple[1], ",")
			item := parseSQLTuple(fields)
			if item.InfoHash != "" {
				fn(item)
			}
		}
	}
	return scanner.Err()
}

// ParseSQL extracts torrent data from SQL INSERT statements (convenience wrapper).
func ParseSQL(r io.Reader) ([]ParsedItem, error) {
	var items []ParsedItem
	err := ParseSQLStream(r, func(item ParsedItem) {
		items = append(items, item)
	})
	return items, err
}

// parseSQLTuple extracts torrent data from a SQL value tuple's fields.
func parseSQLTuple(fields []string) ParsedItem {
	var item ParsedItem
	for _, f := range fields {
		f = strings.TrimSpace(f)
		// Remove SQL string quotes.
		if len(f) >= 2 && (f[0] == '\'' || f[0] == '"') {
			f = f[1 : len(f)-1]
		}
		if validInfoHash(f) && item.InfoHash == "" {
			item.InfoHash = strings.ToLower(f)
		} else if item.InfoHash != "" && item.Name == "" && len(f) > 2 && !isNumeric(f) {
			item.Name = f
		} else if item.InfoHash != "" && isNumeric(f) && item.Size == 0 {
			if n, err := strconv.ParseUint(f, 10, 64); err == nil && n > 0 {
				item.Size = uint(n)
			}
		}
	}
	return item
}

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// AnalyzeStream reads from r without loading the entire file into memory.
// It detects the format from the first 4KB, then streams through counting
// categories and total rows.
func AnalyzeStream(r io.Reader) AnalysisResult {
	format, combined := DetectFormatFromReader(r)

	result := AnalysisResult{
		Format:     format,
		Categories: make(map[string]int),
	}

	// Use a callback-based parse to avoid holding all items in memory.
	var parseErr error
	switch format {
	case FormatNDJSON:
		parseErr = ParseNDJSONStream(combined, func(item ParsedItem) {
			result.TotalRows++
			catKey := "unknown"
			if item.ContentType.Valid {
				catKey = string(item.ContentType.ContentType)
			}
			result.Categories[catKey]++
		})
	case FormatSQL:
		parseErr = ParseSQLStream(combined, func(item ParsedItem) {
			result.TotalRows++
			catKey := "unknown"
			if item.ContentType.Valid {
				catKey = string(item.ContentType.ContentType)
			}
			result.Categories[catKey]++
		})
	default:
		parseErr = ParseCSVStream(combined, func(item ParsedItem) {
			result.TotalRows++
			catKey := "unknown"
			if item.ContentType.Valid {
				catKey = string(item.ContentType.ContentType)
			}
			result.Categories[catKey]++
		})
	}
	if parseErr != nil {
		result.Errors++
	}

	return result
}
