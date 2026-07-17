package rssfeed

import (
	"bytes"
	"encoding/base32"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html/charset"
)

// FeedItem represents a single parsed item from an RSS or Torznab feed.
type FeedItem struct {
	Title       string
	InfoHash    string // always normalized to 40-char lowercase hex
	Size        int64
	PublishedAt time.Time
	Seeders     int
	Leechers    int
	Link        string
}

// rssDoc is the top-level RSS 2.0 XML structure.
type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

// rssItem captures the well-known elements explicitly and everything else via
// the Any catch-all, so the parser is format-agnostic: torznab/newznab, ezRSS,
// nyaa, plain <info_hash> tags, and magnet-in-enclosure feeds all resolve.
type rssItem struct {
	Title     string       `xml:"title"`
	Link      string       `xml:"link"`
	GUID      string       `xml:"guid"`
	PubDate   string       `xml:"pubDate"`
	Date      string       `xml:"date"` // dc:date (RFC3339) fallback
	Size      int64        `xml:"size"`
	Enclosure rssEnclosure `xml:"enclosure"`
	Torrent   ezTorrent    `xml:"torrent"` // ezRSS namespace (matched by local name)
	Any       []anyElement `xml:",any"`
}

// ezTorrent is the ezRSS <torrent> block used by EZTV and similar feeds.
type ezTorrent struct {
	InfoHash      string `xml:"infoHash"`
	MagnetURI     string `xml:"magnetURI"`
	ContentLength int64  `xml:"contentLength"`
	Seeders       int    `xml:"seeders"`
	Peers         int    `xml:"peers"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

// anyElement captures any otherwise-unmatched child element, including its
// character data and attributes, so we can scan generically for hashes,
// magnets, and seed counts regardless of the feed's tag conventions.
type anyElement struct {
	XMLName xml.Name
	Value   string     `xml:",chardata"`
	Attrs   []xml.Attr `xml:",any,attr"`
}

// magnetHashRegex extracts an info-hash (hex or base32) from a magnet URI.
var magnetHashRegex = regexp.MustCompile(`(?i)urn:btih:([0-9a-z]{32,40})`)

var (
	hex40Regex   = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	base32Regex  = regexp.MustCompile(`^[A-Za-z2-7]{32}$`)
	base32Encode = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// ParseFeed parses an RSS 2.0 / Torznab / ezRSS feed and returns its items.
func ParseFeed(r io.Reader) ([]FeedItem, error) {
	// Cap read to 32 MB to prevent memory exhaustion
	data, err := io.ReadAll(io.LimitReader(r, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("rssfeed: failed to read feed body: %w", err)
	}

	// Use a decoder with a CharsetReader so feeds declared as ISO-8859-1,
	// windows-1252, etc. decode correctly. Plain xml.Unmarshal only accepts
	// UTF-8 and errors on any other declared encoding.
	var doc rssDoc
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = charset.NewReaderLabel
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("rssfeed: failed to parse XML: %w", err)
	}

	var items []FeedItem
	for _, ri := range doc.Channel.Items {
		if item, ok := parseItem(ri); ok {
			items = append(items, item)
		}
	}

	return items, nil
}

func parseItem(ri rssItem) (FeedItem, bool) {
	item := FeedItem{Title: ri.Title, Link: ri.Link}

	// Publish date — try the common RSS/Atom layouts.
	item.PublishedAt = parseDate(ri.PubDate, ri.Date)

	// Size: enclosure length, then <size>, then ezRSS contentLength.
	switch {
	case ri.Enclosure.Length > 0:
		item.Size = ri.Enclosure.Length
	case ri.Size > 0:
		item.Size = ri.Size
	case ri.Torrent.ContentLength > 0:
		item.Size = ri.Torrent.ContentLength
	}

	// Seed counts from the ezRSS block if present.
	if ri.Torrent.Seeders > 0 {
		item.Seeders = ri.Torrent.Seeders
	}
	if ri.Torrent.Peers > 0 {
		item.Leechers = ri.Torrent.Peers
	}

	// Resolve the info-hash from the richest source available, in priority order.
	hash := ""

	// 1. ezRSS <torrent><infoHash> / <magnetURI>
	if h := normalizeInfoHash(ri.Torrent.InfoHash); h != "" {
		hash = h
	}
	if hash == "" {
		hash = extractHashFromMagnet(ri.Torrent.MagnetURI)
	}

	// 2. Generic scan of all other elements (torznab/newznab attrs, plain
	//    <info_hash>, nyaa:infoHash, magnet-bearing tags, seed counts, size).
	for _, el := range ri.Any {
		local := normalizeKey(el.XMLName.Local)
		name := normalizeKey(attrValue(el.Attrs, "name")) // torznab/newznab: <attr name="infohash" value="...">

		switch {
		case local == "attr":
			val := attrValue(el.Attrs, "value")
			switch name {
			case "infohash":
				if h := normalizeInfoHash(val); h != "" && hash == "" {
					hash = h
				}
			case "seeders":
				item.Seeders = firstPositive(item.Seeders, parseInt(val))
			case "leechers", "peers":
				item.Leechers = firstPositive(item.Leechers, parseInt(val))
			case "size":
				if item.Size == 0 {
					item.Size = parseInt64(val)
				}
			case "magneturl", "magneturi":
				if hash == "" {
					hash = extractHashFromMagnet(val)
				}
			}
		case strings.Contains(local, "infohash") || local == "hash":
			if h := normalizeInfoHash(el.Value); h != "" && hash == "" {
				hash = h
			}
		case local == "magneturi" || local == "magnet" || local == "magneturl":
			if hash == "" {
				hash = extractHashFromMagnet(el.Value)
			}
		case local == "seeders" || local == "seeds":
			item.Seeders = firstPositive(item.Seeders, parseInt(el.Value))
		case local == "leechers" || local == "peers":
			item.Leechers = firstPositive(item.Leechers, parseInt(el.Value))
		case local == "size" || local == "contentlength":
			if item.Size == 0 {
				item.Size = parseInt64(el.Value)
			}
		}

		// Some feeds hide a magnet in an element attribute (e.g. url="magnet:?...").
		if hash == "" {
			for _, a := range el.Attrs {
				if h := extractHashFromMagnet(a.Value); h != "" {
					hash = h
					break
				}
			}
		}
	}

	// 3. Magnet URIs in the standard link/enclosure/guid fields.
	if hash == "" {
		hash = extractHashFromMagnet(ri.Link)
	}
	if hash == "" {
		hash = extractHashFromMagnet(ri.Enclosure.URL)
	}
	if hash == "" {
		hash = extractHashFromMagnet(ri.GUID)
	}

	// 4. A GUID that is itself a bare info-hash.
	if hash == "" {
		hash = normalizeInfoHash(ri.GUID)
	}

	// No usable hash — nothing we can import.
	if hash == "" {
		return FeedItem{}, false
	}

	item.InfoHash = hash
	return item, true
}

// parseDate tries the candidate strings against the common feed date layouts.
func parseDate(candidates ...string) time.Time {
	layouts := []string{
		time.RFC1123Z,
		time.RFC1123,
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, c); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// extractHashFromMagnet pulls a normalized 40-hex info-hash from a magnet URI,
// handling both hex (40) and base32 (32) btih forms.
func extractHashFromMagnet(s string) string {
	if !strings.Contains(strings.ToLower(s), "magnet:") {
		return ""
	}
	if m := magnetHashRegex.FindStringSubmatch(s); len(m) > 1 {
		return normalizeInfoHash(m[1])
	}
	// Fall back to proper URL parsing of the xt parameter.
	if u, err := url.Parse(s); err == nil {
		xt := u.Query().Get("xt")
		if strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
			return normalizeInfoHash(xt[len("urn:btih:"):])
		}
	}
	return ""
}

// normalizeInfoHash returns a 40-char lowercase hex info-hash from either a
// 40-char hex string or a 32-char base32 string, or "" if neither.
func normalizeInfoHash(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case hex40Regex.MatchString(s):
		return strings.ToLower(s)
	case base32Regex.MatchString(s):
		b, err := base32Encode.DecodeString(strings.ToUpper(s))
		if err != nil || len(b) != 20 {
			return ""
		}
		return hex.EncodeToString(b)
	default:
		return ""
	}
}

// normalizeKey lowercases an element/attribute name and strips separators so
// info_hash, infoHash, and info-hash all compare equal.
func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

func attrValue(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if strings.EqualFold(a.Name.Local, name) {
			return a.Value
		}
	}
	return ""
}

func firstPositive(existing, candidate int) int {
	if existing > 0 {
		return existing
	}
	return candidate
}

func parseInt(s string) int {
	var n int
	_, _ = fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n
}

func parseInt64(s string) int64 {
	var n int64
	_, _ = fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n
}
