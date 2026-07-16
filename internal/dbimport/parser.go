package dbimport

import (
	"bufio"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
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
	if IsSQLite(data) {
		return FormatSQLite
	}
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

	// Exact matches first.
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
	}

	// Prefix matches for RARBG-style categories (movies_x264, games_pc_iso, tv_sd, etc.).
	if strings.HasPrefix(s, "movie") {
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeMovie}
	}
	if strings.HasPrefix(s, "tv") {
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeTvShow}
	}
	if strings.HasPrefix(s, "game") {
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeGame}
	}
	if strings.HasPrefix(s, "music") {
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeMusic}
	}
	if strings.HasPrefix(s, "software") {
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeSoftware}
	}

	return model.NullContentType{}
}

// tryDecodeHashField attempts to interpret a field as an info hash.
// Supports 40-char hex hashes and base64-encoded 20-byte SHA1 hashes.
// Returns the lowercase hex hash or empty string.
func tryDecodeHashField(s string) string {
	s = strings.TrimSpace(s)
	if validInfoHash(s) {
		return strings.ToLower(s)
	}
	// Try base64 decode (20 bytes = 28 chars base64 with padding, or 27 without).
	if len(s) >= 27 && len(s) <= 28 {
		if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 20 {
			return hex.EncodeToString(b)
		}
		// Try URL-safe or raw base64 variants.
		if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == 20 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

// detectCSVDelimiter guesses the delimiter from a header line.
func detectCSVDelimiter(header string) rune {
	// Count candidate delimiters in the header.
	for _, d := range []rune{'\t', ';', '|'} {
		if strings.ContainsRune(header, d) {
			return d
		}
	}
	return ','
}

// ParseCSVStream reads a CSV file, calling fn for each valid item.
// Streams line-by-line without accumulating all items in memory.
// Auto-detects delimiter (comma, semicolon, tab, pipe) and supports
// both hex and base64-encoded info hashes.
func ParseCSVStream(r io.Reader, fn func(ParsedItem)) error {
	// Buffer the reader so we can peek the first line for delimiter detection.
	br := bufio.NewReader(r)
	firstLine, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("reading CSV first line: %w", err)
	}
	// Strip comment prefix (some dumps use # as header marker).
	headerLine := strings.TrimSpace(firstLine)
	if strings.HasPrefix(headerLine, "#") {
		headerLine = strings.TrimSpace(headerLine[1:])
	}

	delimiter := detectCSVDelimiter(headerLine)

	// Re-create the CSV reader with the peeked line + remainder.
	// We need to feed the cleaned header (without #) back.
	cleanedFirst := headerLine + "\n"
	combined := io.MultiReader(strings.NewReader(cleanedFirst), br)

	cr := csv.NewReader(combined)
	cr.Comma = delimiter
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = false

	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("reading CSV header: %w", err)
	}

	colIdx := make(map[string]int)
	for i, h := range header {
		// Normalize: lowercase, strip parens/suffixes like "HASH(B64)" → "hash"
		norm := strings.ToLower(strings.TrimSpace(h))
		if paren := strings.IndexByte(norm, '('); paren >= 0 {
			norm = strings.TrimSpace(norm[:paren])
		}
		colIdx[norm] = i
	}

	hashCol := -1
	hashIsBase64 := false
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
			v := strings.TrimSpace(val)
			if validInfoHash(v) {
				hashCol = i
				break
			}
			if tryDecodeHashField(v) != "" {
				hashCol = i
				hashIsBase64 = true
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
		rawHash := getField(record, hashCol)
		var hash string
		if hashIsBase64 {
			hash = tryDecodeHashField(rawHash)
		} else {
			hash = strings.ToLower(rawHash)
			if !validInfoHash(hash) {
				// Fallback: try base64 in case it's mixed.
				hash = tryDecodeHashField(rawHash)
			}
		}
		if hash == "" {
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
			item.PublishedAt = parseDateFlexible(v)
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

// parseDateFlexible tries multiple date formats, Unix timestamps, and natural language dates.
func parseDateFlexible(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	// Try as Unix timestamp (integer seconds).
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 946684800 && n < 2208988800 {
		return time.Unix(n, 0).UTC()
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
		"2006-Jan-02 15:04:05", // 2015-Oct-27 04:10:22
		"02 Jan 2006 15:04:05", // common log format
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
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

// sqlInsertRe matches INSERT INTO ... VALUES lines, capturing the optional column list.
var sqlInsertRe = regexp.MustCompile(`(?i)INSERT\s+INTO\s+\S+\s*(\([^)]*\))?\s*VALUES\s*`)

// sqlColumnMapping holds the column-name-to-index mapping parsed from an INSERT statement.
type sqlColumnMapping struct {
	hashCol int
	nameCol int
	sizeCol int
	catCol  int
	dateCol int
}

// parseSQLColumnList extracts column names from the INSERT's column list and maps them.
// Returns nil if there's no column list or no hash column found.
func parseSQLColumnList(colList string) *sqlColumnMapping {
	// colList looks like: (`id`, `name`, `description`, `category_id`, `size`, `hash`, ...)
	colList = strings.Trim(colList, "()")
	if colList == "" {
		return nil
	}
	m := &sqlColumnMapping{hashCol: -1, nameCol: -1, sizeCol: -1, catCol: -1, dateCol: -1}
	for i, col := range strings.Split(colList, ",") {
		col = strings.TrimSpace(col)
		col = strings.Trim(col, "`\"[] ")
		col = strings.ToLower(col)
		switch {
		case matchesAny(col, "hash", "info_hash", "infohash", "btih"):
			m.hashCol = i
		case matchesAny(col, "name", "title", "torrent_name"):
			m.nameCol = i
		case matchesAny(col, "size", "length", "total_size"):
			m.sizeCol = i
		case matchesAny(col, "tags", "content_type", "type", "category", "cat"):
			m.catCol = i
		case matchesAny(col, "date", "dt", "published", "published_at", "added", "created_at"):
			m.dateCol = i
		}
	}
	if m.hashCol < 0 {
		return nil
	}
	return m
}

// ParseSQLStream extracts torrent data from SQL INSERT statements, calling fn for each.
// Handles quoted strings containing commas and parentheses correctly.
// Supports both single-line INSERTs and multi-line INSERTs where VALUES is on one
// line and tuples follow on subsequent lines (phpMyAdmin dumps).
func ParseSQLStream(r io.Reader, fn func(ParsedItem)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10 MB lines for big INSERTs

	inMultiLineInsert := false
	var colMap *sqlColumnMapping // parsed from INSERT column list

	for scanner.Scan() {
		line := scanner.Text()

		if match := sqlInsertRe.FindStringSubmatch(line); match != nil {
			loc := sqlInsertRe.FindStringIndex(line)
			// match[1] is the optional column list.
			if match[1] != "" {
				colMap = parseSQLColumnList(match[1])
			} else {
				colMap = nil
			}
			valuesPart := strings.TrimSpace(line[loc[1]:])
			if valuesPart == "" {
				// Multi-line INSERT: VALUES is at end of line, tuples follow.
				inMultiLineInsert = true
				continue
			}
			// Single-line INSERT with values on same line.
			parseSQLTuplesAndEmit(valuesPart, colMap, fn)
			inMultiLineInsert = false
			continue
		}

		if inMultiLineInsert {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			// Tuple continuation lines start with '('.
			if trimmed[0] == '(' {
				parseSQLTuplesAndEmit(trimmed, colMap, fn)
			} else {
				// Non-tuple line (e.g. a new statement) ends the multi-line INSERT.
				inMultiLineInsert = false
			}
		}
	}
	return scanner.Err()
}

func parseSQLTuplesAndEmit(valuesPart string, colMap *sqlColumnMapping, fn func(ParsedItem)) {
	tuples := extractSQLTuples(valuesPart)
	for _, fields := range tuples {
		var item ParsedItem
		if colMap != nil {
			item = parseSQLTupleWithMapping(fields, colMap)
		} else {
			item = parseSQLTuple(fields)
		}
		if item.InfoHash != "" {
			fn(item)
		}
	}
}

// parseSQLTupleWithMapping extracts torrent data using known column positions.
func parseSQLTupleWithMapping(fields []string, m *sqlColumnMapping) ParsedItem {
	var item ParsedItem
	getField := func(idx int) string {
		if idx >= 0 && idx < len(fields) {
			f := strings.TrimSpace(fields[idx])
			if strings.EqualFold(f, "null") {
				return ""
			}
			return f
		}
		return ""
	}

	raw := getField(m.hashCol)
	if raw == "" {
		return item
	}

	// Try bytea hex format first.
	switch {
	case strings.HasPrefix(raw, "\\x") && len(raw) == 42:
		item.InfoHash = strings.ToLower(raw[2:])
	case strings.HasPrefix(raw, "x") && len(raw) == 41:
		item.InfoHash = strings.ToLower(raw[1:])
	default:
		if validInfoHash(raw) {
			item.InfoHash = strings.ToLower(raw)
		}
	}
	if item.InfoHash == "" {
		return item
	}

	if v := getField(m.nameCol); v != "" {
		item.Name = v
	}
	if v := getField(m.sizeCol); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			item.Size = uint(n)
		}
	}
	if v := getField(m.catCol); v != "" {
		item.ContentType = parseContentType(v)
	}
	if v := getField(m.dateCol); v != "" {
		item.PublishedAt = parseDateFlexible(v)
	}
	return item
}

// extractSQLTuples parses "(val1, val2, ...),(val1, val2, ...)" respecting
// single-quoted strings (with '' escapes) so commas and parens inside strings
// don't break parsing.
func extractSQLTuples(s string) [][]string {
	var result [][]string
	i := 0
	for i < len(s) {
		// Find opening paren.
		for i < len(s) && s[i] != '(' {
			i++
		}
		if i >= len(s) {
			break
		}
		i++ // skip '('
		fields := extractSQLFields(s, &i)
		if len(fields) > 0 {
			result = append(result, fields)
		}
	}
	return result
}

// extractSQLFields reads comma-separated SQL values starting after '(' until
// the matching ')'. Advances *pos past the ')'.
func extractSQLFields(s string, pos *int) []string {
	var fields []string
	for *pos < len(s) {
		// Skip whitespace.
		for *pos < len(s) && (s[*pos] == ' ' || s[*pos] == '\t' || s[*pos] == '\n' || s[*pos] == '\r') {
			*pos++
		}
		if *pos >= len(s) {
			break
		}
		if s[*pos] == ')' {
			*pos++
			break
		}

		var field string
		if s[*pos] == '\'' {
			// Quoted string — read until unescaped closing quote.
			*pos++ // skip opening quote
			var sb strings.Builder
			for *pos < len(s) {
				if s[*pos] == '\'' {
					if *pos+1 < len(s) && s[*pos+1] == '\'' {
						sb.WriteByte('\'')
						*pos += 2
						continue
					}
					*pos++ // skip closing quote
					break
				}
				// Handle backslash escapes (MySQL style).
				if s[*pos] == '\\' && *pos+1 < len(s) {
					sb.WriteByte(s[*pos+1])
					*pos += 2
					continue
				}
				sb.WriteByte(s[*pos])
				*pos++
			}
			field = sb.String()
		} else {
			// Unquoted value (number, NULL, etc.).
			start := *pos
			for *pos < len(s) && s[*pos] != ',' && s[*pos] != ')' {
				*pos++
			}
			field = strings.TrimSpace(s[start:*pos])
		}

		fields = append(fields, field)

		// Skip comma between fields.
		if *pos < len(s) && s[*pos] == ',' {
			*pos++
		}
	}
	return fields
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
// Fields have already been unquoted by extractSQLFields.
// Uses a two-pass approach: first find the hash field, then scan all other
// fields for name, size, etc. This handles schemas where name precedes hash.
func parseSQLTuple(fields []string) ParsedItem {
	var item ParsedItem
	hashIdx := -1

	// Pass 1: find the info_hash field.
	for i, f := range fields {
		f = strings.TrimSpace(f)
		if strings.EqualFold(f, "null") || f == "" {
			continue
		}

		// Check for Postgres bytea hex format.
		var hexStr string
		switch {
		case strings.HasPrefix(f, "\\x") && len(f) == 42:
			hexStr = f[2:]
		case strings.HasPrefix(f, "x") && len(f) == 41:
			hexStr = f[1:]
		}
		if hexStr != "" && validInfoHash(hexStr) {
			item.InfoHash = strings.ToLower(hexStr)
			hashIdx = i
			break
		}

		if validInfoHash(f) {
			item.InfoHash = strings.ToLower(f)
			hashIdx = i
			break
		}
	}

	if hashIdx < 0 {
		return item
	}

	// Pass 2: scan all fields (except hash and obvious ID/status fields) for name and size.
	for i, f := range fields {
		if i == hashIdx {
			continue
		}
		f = strings.TrimSpace(f)
		if strings.EqualFold(f, "null") || f == "" {
			continue
		}

		if item.Name == "" && len(f) > 2 && !isNumeric(f) {
			// Skip very short single-digit strings (likely category_id, status fields).
			item.Name = f
		} else if item.Size == 0 && isNumeric(f) {
			if n, err := strconv.ParseUint(f, 10, 64); err == nil && n > 100000 {
				// Only accept sizes > 100KB to avoid picking up IDs/counts.
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
