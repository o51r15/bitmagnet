package rssfeed

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// FeedItem represents a single parsed item from an RSS or Torznab feed.
type FeedItem struct {
	Title       string
	InfoHash    string
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

type rssItem struct {
	Title       string        `xml:"title"`
	Link        string        `xml:"link"`
	GUID        string        `xml:"guid"`
	PubDate     string        `xml:"pubDate"`
	Size        int64         `xml:"size"`
	Enclosure   rssEnclosure  `xml:"enclosure"`
	TorznabAttr []torznabAttr `xml:"http://torznab.com/schemas/2015/feed attr"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// magnetHashRegex extracts the info_hash from a magnet URI.
var magnetHashRegex = regexp.MustCompile(`(?i)urn:btih:([0-9a-f]{40})`)

// ParseFeed parses an RSS 2.0 or Torznab XML feed and returns the items.
func ParseFeed(r io.Reader) ([]FeedItem, error) {
	// Cap read to 32 MB to prevent memory exhaustion
	data, err := io.ReadAll(io.LimitReader(r, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("rssfeed: failed to read feed body: %w", err)
	}

	var doc rssDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("rssfeed: failed to parse XML: %w", err)
	}

	var items []FeedItem
	for _, ri := range doc.Channel.Items {
		item := FeedItem{
			Title: ri.Title,
			Link:  ri.Link,
		}

		// Parse publish date (RFC1123 / RFC1123Z / RFC822 are common in RSS)
		if ri.PubDate != "" {
			for _, layout := range []string{
				time.RFC1123Z,
				time.RFC1123,
				"Mon, 2 Jan 2006 15:04:05 -0700",
				"Mon, 2 Jan 2006 15:04:05 MST",
				time.RFC3339,
			} {
				if t, parseErr := time.Parse(layout, ri.PubDate); parseErr == nil {
					item.PublishedAt = t
					break
				}
			}
		}

		// Size: prefer enclosure length, fall back to <size> element
		if ri.Enclosure.Length > 0 {
			item.Size = ri.Enclosure.Length
		} else if ri.Size > 0 {
			item.Size = ri.Size
		}

		// Extract info_hash: prefer Torznab attr, then magnet link, then enclosure URL
		for _, attr := range ri.TorznabAttr {
			switch attr.Name {
			case "infohash":
				item.InfoHash = strings.ToLower(attr.Value)
			case "seeders":
				item.Seeders = parseInt(attr.Value)
			case "leechers":
				item.Leechers = parseInt(attr.Value)
			case "size":
				if item.Size == 0 {
					item.Size = parseInt64(attr.Value)
				}
			}
		}

		if item.InfoHash == "" {
			item.InfoHash = extractHashFromMagnet(ri.Link)
		}
		if item.InfoHash == "" {
			item.InfoHash = extractHashFromMagnet(ri.Enclosure.URL)
		}
		if item.InfoHash == "" {
			item.InfoHash = extractHashFromMagnet(ri.GUID)
		}

		// Skip items with no info_hash — we can't do anything with them
		if item.InfoHash == "" {
			continue
		}

		items = append(items, item)
	}

	return items, nil
}

func extractHashFromMagnet(s string) string {
	if !strings.Contains(s, "magnet:") {
		return ""
	}
	// Try regex first for raw hex hash
	if m := magnetHashRegex.FindStringSubmatch(s); len(m) > 1 {
		return strings.ToLower(m[1])
	}
	// Fall back to URL parsing for base32 or other encoded forms
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	xt := u.Query().Get("xt")
	if strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
		hash := xt[9:]
		if len(hash) == 40 {
			return strings.ToLower(hash)
		}
	}
	return ""
}

func parseInt(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func parseInt64(s string) int64 {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}
