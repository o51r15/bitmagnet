package dbimport

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
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

// sqliteMode indicates how to extract torrents from the SQLite database.
type sqliteMode int

const (
	sqliteModeSimple   sqliteMode = iota // Single table with hash column
	sqliteModeTorrentBlob               // TPB-style: metadata table + torrent BLOB table
)

// sqliteDiscovery holds the result of schema discovery.
type sqliteDiscovery struct {
	mode sqliteMode
	// For sqliteModeSimple:
	info *sqliteTableInfo
	// For sqliteModeTorrentBlob:
	metaTable  string // e.g. "torrentinfo"
	blobTable  string // e.g. "torrents"
	catTable   string // e.g. "cat" (optional)
	nameCol    string
	sizeCol    string
	catCol     string
	dateCol    string
}

// discoverSchema finds the best table and column mapping in a SQLite DB.
// Supports both single-table (RARBG-style) and multi-table (TPB-style) schemas.
func discoverSchema(db *sql.DB) (*sqliteDiscovery, error) {
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

	// First try: single table with a hash column (RARBG style).
	for _, table := range tables {
		info := tryMapColumns(db, table)
		if info != nil {
			return &sqliteDiscovery{mode: sqliteModeSimple, info: info}, nil
		}
	}

	// Second try: TPB-style with separate metadata and torrent BLOB tables.
	// Look for a table with a BLOB "file" column (the .torrent files) and
	// a metadata table with title/size/date columns.
	disc := tryDiscoverTPBSchema(db, tables)
	if disc != nil {
		return disc, nil
	}

	return nil, fmt.Errorf("no table with a recognizable info_hash column or torrent BLOB found")
}

// tryDiscoverTPBSchema checks for TPB-style schema: a torrent BLOB table
// (with id + file BLOB) and a metadata table (with title, size, date, etc.).
func tryDiscoverTPBSchema(db *sql.DB, tables []string) *sqliteDiscovery {
	var blobTable, catTable string

	for _, table := range tables {
		cols := getColumnInfo(db, table)
		// Check for BLOB table (has "file" column of type BLOB).
		for _, col := range cols {
			if strings.EqualFold(col.name, "file") && strings.Contains(strings.ToUpper(col.colType), "BLOB") {
				blobTable = table
			}
		}
		// Check for category lookup table (has "id" and "title" only).
		if len(cols) == 2 {
			hasID, hasTitle := false, false
			for _, col := range cols {
				if strings.EqualFold(col.name, "id") {
					hasID = true
				}
				if strings.EqualFold(col.name, "title") {
					hasTitle = true
				}
			}
			if hasID && hasTitle {
				catTable = table
			}
		}
	}

	if blobTable == "" {
		return nil
	}

	// Find the metadata table — the one with the most useful columns
	// (title, size, date, cat) that isn't the blob or cat table.
	for _, table := range tables {
		if table == blobTable || table == catTable {
			continue
		}
		cols := getColumnInfo(db, table)
		disc := &sqliteDiscovery{
			mode:      sqliteModeTorrentBlob,
			blobTable: blobTable,
			metaTable: table,
			catTable:  catTable,
		}
		for _, col := range cols {
			lower := strings.ToLower(col.name)
			switch {
			case matchesAny(lower, "name", "title", "torrent_name"):
				disc.nameCol = col.name
			case matchesAny(lower, "size", "length", "total_size"):
				disc.sizeCol = col.name
			case matchesAny(lower, "category", "content_type", "type", "cat"):
				disc.catCol = col.name
			case matchesAny(lower, "date", "dt", "published", "published_at", "added", "created_at"):
				disc.dateCol = col.name
			}
		}
		if disc.nameCol != "" {
			return disc
		}
	}
	return nil
}

type columnInfo struct {
	name    string
	colType string
}

func getColumnInfo(db *sql.DB, table string) []columnInfo {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var cid int
		var name, ct string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ct, &notNull, &dflt, &pk); err != nil {
			continue
		}
		cols = append(cols, columnInfo{name: name, colType: ct})
	}
	return cols
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

	disc, err := discoverSchema(db)
	if err != nil {
		return err
	}

	if disc.mode == sqliteModeTorrentBlob {
		return parseSQLiteTorrentBlob(db, disc, fn)
	}

	return parseSQLiteSimple(db, disc.info, fn)
}

// parseSQLiteSimple handles the single-table case (RARBG-style).
func parseSQLiteSimple(db *sql.DB, info *sqliteTableInfo, fn func(ParsedItem)) error {
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

// parseSQLiteTorrentBlob handles TPB-style databases where info_hash must be
// extracted from .torrent file BLOBs by computing SHA1 of the bencoded info dict.
func parseSQLiteTorrentBlob(db *sql.DB, disc *sqliteDiscovery, fn func(ParsedItem)) error {
	// Build a category lookup map if a category table exists.
	catLookup := make(map[int]string)
	if disc.catTable != "" {
		catRows, err := db.Query(fmt.Sprintf("SELECT id, title FROM %s", disc.catTable))
		if err == nil {
			defer catRows.Close()
			for catRows.Next() {
				var id int
				var title string
				if catRows.Scan(&id, &title) == nil {
					catLookup[id] = title
				}
			}
		}
	}

	// Build the query joining metadata and blob tables.
	var selectCols []string
	selectCols = append(selectCols, "b.file")
	if disc.nameCol != "" {
		selectCols = append(selectCols, fmt.Sprintf("m.%s", disc.nameCol))
	}
	if disc.sizeCol != "" {
		selectCols = append(selectCols, fmt.Sprintf("m.%s", disc.sizeCol))
	}
	if disc.catCol != "" {
		selectCols = append(selectCols, fmt.Sprintf("m.%s", disc.catCol))
	}
	if disc.dateCol != "" {
		selectCols = append(selectCols, fmt.Sprintf("m.%s", disc.dateCol))
	}

	query := fmt.Sprintf(
		"SELECT %s FROM %s m INNER JOIN %s b ON m.id = b.id",
		strings.Join(selectCols, ", "),
		disc.metaTable,
		disc.blobTable,
	)

	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("querying torrent blob tables: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fileBlob []byte
		var nameVal, sizeVal, catVal, dateVal sql.NullString
		dests := []interface{}{&fileBlob}
		if disc.nameCol != "" {
			dests = append(dests, &nameVal)
		}
		if disc.sizeCol != "" {
			dests = append(dests, &sizeVal)
		}
		if disc.catCol != "" {
			dests = append(dests, &catVal)
		}
		if disc.dateCol != "" {
			dests = append(dests, &dateVal)
		}

		if err := rows.Scan(dests...); err != nil {
			continue
		}

		// Extract info_hash from the .torrent BLOB.
		hash := extractInfoHashFromTorrent(fileBlob)
		if hash == "" {
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
			// TPB uses numeric category IDs — try lookup.
			catStr := catVal.String
			var numID int
			if _, err := fmt.Sscanf(catStr, "%d", &numID); err == nil && numID > 0 {
				if title, ok := catLookup[numID]; ok {
					item.ContentType = parseTPBCategory(numID, title)
				} else {
					item.ContentType = parseTPBCategory(numID, "")
				}
			} else {
				item.ContentType = parseContentType(catStr)
			}
		}
		if dateVal.Valid {
			item.PublishedAt = parseDateFlex(dateVal.String)
		}
		fn(item)
	}
	return rows.Err()
}

// extractInfoHashFromTorrent computes the info_hash from a .torrent file's raw bytes.
// It bencode-decodes to find the "info" key, then SHA1-hashes the raw bencoded info dict.
func extractInfoHashFromTorrent(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	// Decode the torrent file as a generic map.
	var torrent map[string]interface{}
	if err := bencode.Unmarshal(data, &torrent); err != nil {
		return ""
	}

	infoRaw, ok := torrent["info"]
	if !ok {
		return ""
	}

	// Re-encode the info dictionary to get its canonical bencoded form.
	encoded, err := bencode.Marshal(infoRaw)
	if err != nil {
		return ""
	}

	h := sha1.Sum(encoded)
	return hex.EncodeToString(h[:])
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

// parseTPBCategory maps TPB numeric category IDs to bitmagnet content types.
// TPB categories: 100s=Audio, 200s=Video, 300s=Applications, 400s=Games, 500s=Porn, 600s=Other.
func parseTPBCategory(id int, title string) model.NullContentType {
	// Use the hundred-group to determine the broad category.
	switch id / 100 {
	case 1: // Audio
		if id == 102 {
			return model.NullContentType{Valid: true, ContentType: model.ContentTypeAudiobook}
		}
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeMusic}
	case 2: // Video
		switch id {
		case 205: // TV shows
			return model.NullContentType{Valid: true, ContentType: model.ContentTypeTvShow}
		case 208: // Highres - TV shows
			return model.NullContentType{Valid: true, ContentType: model.ContentTypeTvShow}
		default: // Movies, Movie clips, etc.
			return model.NullContentType{Valid: true, ContentType: model.ContentTypeMovie}
		}
	case 3: // Applications
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeSoftware}
	case 4: // Games
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeGame}
	case 5: // Porn
		return model.NullContentType{Valid: true, ContentType: model.ContentTypeXxx}
	case 6: // Other
		switch id {
		case 601: // E-books
			return model.NullContentType{Valid: true, ContentType: model.ContentTypeEbook}
		case 602: // Comics
			return model.NullContentType{Valid: true, ContentType: model.ContentTypeComic}
		}
		return model.NullContentType{}
	}

	// Fallback: try the title string if provided.
	if title != "" {
		return parseContentType(title)
	}
	return model.NullContentType{}
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

// parseDateFlex delegates to the unified parseDateFlexible in parser.go.
func parseDateFlex(s string) time.Time {
	return parseDateFlexible(s)
}
