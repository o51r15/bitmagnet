package omdb

// Result holds the JSON response from OMDb API.
type LookupResult struct {
	Response  string `json:"Response"`
	Error     string `json:"Error,omitempty"`
	Title     string `json:"Title"`
	Year      string `json:"Year"`
	Rated     string `json:"Rated"`
	Released  string `json:"Released"`
	Runtime   string `json:"Runtime"`
	Genre     string `json:"Genre"`
	Director  string `json:"Director"`
	Writer    string `json:"Writer"`
	Actors    string `json:"Actors"`
	Plot      string `json:"Plot"`
	Language  string `json:"Language"`
	Country   string `json:"Country"`
	Awards    string `json:"Awards"`
	Poster    string `json:"Poster"`
	Ratings   []Rating `json:"Ratings"`
	Metascore  string `json:"Metascore"`
	ImdbRating string `json:"imdbRating"`
	ImdbVotes  string `json:"imdbVotes"`
	ImdbID     string `json:"imdbID"`
	Type       string `json:"Type"`
	BoxOffice  string `json:"BoxOffice,omitempty"`
}

type Rating struct {
	Source string `json:"Source"`
	Value  string `json:"Value"`
}
