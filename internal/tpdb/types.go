package tpdb

// SearchResponse represents the paginated response from TPDB scene/movie search.
type SearchResponse struct {
	Data []SearchResult `json:"data"`
}

// SearchResult represents a single scene or movie result from TPDB.
type SearchResult struct {
	ID          string       `json:"_id"`
	Title       string       `json:"title"`
	Date        string       `json:"date"`
	Description string       `json:"description"`
	Site        *Site        `json:"site"`
	Performers  []Performer  `json:"performers"`
	Tags        []Tag        `json:"tags"`
	Posters     []Image      `json:"posters"`
	Background  *Image       `json:"background"`
}

type Site struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type Performer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Tag struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Image struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
