package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type prowlarrClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newClient(baseURL, apiKey string) *prowlarrClient {
	// Force IPv4 to avoid IPv6 connectivity issues inside VPN namespaces
	// where only an IPv4 exit exists (common with gluetun setups).
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		},
	}
	return &prowlarrClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// Indexer represents a Prowlarr indexer from GET /api/v1/indexer.
type Indexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Enable   bool   `json:"enable"`
	Priority int    `json:"priority"`
}

// Category is a Newznab category returned in search results.
type Category struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// SearchResult is a single result from GET /api/v1/search.
type SearchResult struct {
	Title       string     `json:"title"`
	Size        int64      `json:"size"`
	InfoHash    string     `json:"infoHash"`
	PublishDate time.Time  `json:"publishDate"`
	Seeders     int        `json:"seeders"`
	Leechers    int        `json:"leechers"`
	Categories  []Category `json:"categories"`
}

func (c *prowlarrClient) get(path string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	u := fmt.Sprintf("%s/api/v1/%s?%s", c.baseURL, path, params.Encode())
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Send the API key as a header rather than a query param so it never
	// appears in Prowlarr access logs, proxy logs, or network captures.
	req.Header.Set("X-Api-Key", c.apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr: API returned status %d for %s", resp.StatusCode, path)
	}
	// Cap response body to prevent memory exhaustion from large or adversarial payloads.
	const maxBodyBytes = 32 * 1024 * 1024 // 32 MB — well above any real API response
	return io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
}

func (c *prowlarrClient) getIndexers() ([]Indexer, error) {
	body, err := c.get("indexer", nil)
	if err != nil {
		return nil, err
	}
	var indexers []Indexer
	return indexers, json.Unmarshal(body, &indexers)
}

func (c *prowlarrClient) search(indexerID int, categories []int) ([]SearchResult, error) {
	params := url.Values{}
	// Prowlarr requires plain "indexerIds" and "categories" — bracket form
	// (indexerIds[]) gets percent-encoded by url.Values and is silently ignored,
	// causing all indexers to be searched instead of the specified one.
	params.Add("indexerIds", strconv.Itoa(indexerID))
	params.Set("query", "")
	params.Set("limit", "100")
	for _, cat := range categories {
		params.Add("categories", strconv.Itoa(cat))
	}
	body, err := c.get("search", params)
	if err != nil {
		return nil, err
	}
	var results []SearchResult
	return results, json.Unmarshal(body, &results)
}
