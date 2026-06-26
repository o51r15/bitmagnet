package prowlarr

import (
	"encoding/json"
	"fmt"
	"io"
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
	return &prowlarrClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
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
	params.Set("apikey", c.apiKey)
	u := fmt.Sprintf("%s/api/v1/%s?%s", c.baseURL, path, params.Encode())
	resp, err := c.httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr: API returned status %d for %s", resp.StatusCode, path)
	}
	return io.ReadAll(resp.Body)
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
	params.Add("indexerIds[]", strconv.Itoa(indexerID))
	params.Set("query", "")
	params.Set("limit", "100")
	for _, cat := range categories {
		params.Add("categories[]", strconv.Itoa(cat))
	}
	body, err := c.get("search", params)
	if err != nil {
		return nil, err
	}
	var results []SearchResult
	return results, json.Unmarshal(body, &results)
}
