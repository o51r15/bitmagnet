package dbimport

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// FormatSQLite identifies a SQLite database file.
const FormatSQLite Format = "sqlite"

// sqliteMagic is the first 16 bytes of every SQLite database file.
const sqliteMagic = "SQLite format 3\x00"

// IsSQLite checks if the first bytes match the SQLite file header.
func IsSQLite(data []byte) bool {
	return len(data) >= 16 && string(data[:16]) == sqliteMagic
}

// SaveToTemp writes a reader to a temporary file and returns the path.
// The caller is responsible for removing the file.
func SaveToTemp(r io.Reader, limit int64) (string, error) {
	f, err := os.CreateTemp("", "dbimport-*.sqlite")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	path := f.Name()

	cr := &countingReader{r: r, limit: limit}
	_, err = io.Copy(f, cr)
	f.Close()
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	if cr.exceeded {
		os.Remove(path)
		return "", fmt.Errorf("file exceeds maximum size of %d bytes", limit)
	}
	return path, nil
}

// sqliteTableInfo holds column names for a discovered torrent table.
type sqliteTableInfo struct {
	table   string
	hashCol string
	nameCol string
	sizeCol string
	catCol  string
	dateCol string
}

// discoverTable finds the best table and column mapping in a SQLite DB.
func discoverTable(db *sql.DB) (*sqliteTableInfo, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables = append(tables, name)
		}
	}

	for _, table := range tables {
		info := tryMapColumns(db, table)
		if info != nil {
			return info, nil
		}
	}
	return nil, fmt.Errorf("no table with a recognizable info_hash column found")
}

func tryMapColumns(db *sql.DB, table string) *sqliteTableInfo {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil
	}
	defer rows.Close()

	info := &sqliteTableInfo{table: table}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			continue
		}
		lower := strings.ToLower(name)
		switch {
		case matchesAny(lower, "hash", "info_hash", "infohash", "btih"):
			info.hashCol = name
		case matchesAny(lower, "name", "title", "torrent_name"):
			info.nameCol = name
		case matchesAny(lower, "size", "length", "total_size"):
			info.sizeCol = name
		case matchesAny(lower, "category", "content_type", "type", "cat"):
			info.catCol = name
		case matchesAny(lower, "date", "dt", "published", "published_at", "added", "created_at"):
			info.dateCol = name
		}
	}

	if info.hashCol == "" {
		return nil
	}
	return info
}

func matchesAny(val string, candidates ...string) bool {
	for _, c := range candidates {
		if val == c {
			return true
		}
	}
	return false
}

// ParseSQLiteStream opens a SQLite database file and calls fn for each torrent row.
func ParseSQLiteStream(path string, fn func(ParsedItem)) error {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening sqlite db: %w", err)
	}
	defer db.Close()

	info, err := discoverTable(db)
	if err != nil {
		return err
	}

	// Build SELECT with discovered columns.
	cols := []string{info.hashCol}
	if info.nameCol != "" {
		cols = append(cols, info.nameCol)
	}
	if info.sizeCol != "" {
		cols = append(cols, info.sizeCol)
	}
	if info.catCol != "" {
		cols = append(cols, info.catCol)
	}
	if info.dateCol != "" {
		cols = append(cols, info.dateCol)
	}

	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(cols, ", "), info.table)
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("querying table %s: %w", info.table, err)
	}
	defer rows.Close()

	for rows.Next() {
		// Build scan destinations dynamically.
		var hashVal string
		var nameVal, sizeVal, catVal, dateVal sql.NullString
		dests := []interface{}{&hashVal}
		if info.nameCol != "" {
			dests = append(dests, &nameVal)
		}
		if info.sizeCol != "" {
			dests = append(dests, &sizeVal)
		}
		if info.catCol != "" {
			dests = append(dests, &catVal)
		}
		if info.dateCol != "" {
			dests = append(dests, &dateVal)
		}

		if err := rows.Scan(dests...); err != nil {
			continue
		}

		hash := strings.ToLower(strings.TrimSpace(hashVal))
		if !validInfoHash(hash) {
			continue
		}

		item := ParsedItem{InfoHash: hash}
		if nameVal.Valid {
			item.Name = nameVal.String
		}
		if sizeVal.Valid {
			if n, err := parseUint(sizeVal.String); err == nil {
				item.Size = uint(n)
			}
		}
		if catVal.Valid {
			item.ContentType = parseContentType(catVal.String)
		}
		if dateVal.Valid {
			item.PublishedAt = parseDateFlex(dateVal.String)
		}
		fn(item)
	}
	return rows.Err()
}

// AnalyzeSQLite analyzes a SQLite database file and returns category counts.
func AnalyzeSQLite(path string) AnalysisResult {
	result := AnalysisResult{
		Format:     FormatSQLite,
		Categories: make(map[string]int),
	}
	err := ParseSQLiteStream(path, func(item ParsedItem) {
		result.TotalRows++
		catKey := "unknown"
		if item.ContentType.Valid {
			catKey = string(item.ContentType.ContentType)
		}
		result.Categories[catKey]++
	})
	if err != nil {
		result.Errors++
	}
	return result
}

func parseUint(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	// Handle floating point sizes (some DBs store as real).
	if strings.Contains(s, ".") {
		var f float64
		if _, err := fmt.Sscanf(s, "%f", &f); err == nil && f >= 0 {
			return uint64(f), nil
		}
	}
	var n uint64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func parseDateFlex(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
