package classifier

import (
	"github.com/bitmagnet-io/bitmagnet/internal/classifier/classification"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
)

const attachOMDBContentName = "attach_omdb_content"

type attachOMDBContentAction struct{}

func (attachOMDBContentAction) name() string {
	return attachOMDBContentName
}

var attachOMDBContentPayloadSpec = payloadLiteral[string]{
	literal:     attachOMDBContentName,
	description: "Enrich already-attached content with OMDb metadata (ratings, cast, etc.) using the IMDB ID",
}

func (attachOMDBContentAction) compileAction(ctx compilerContext) (action, error) {
	if _, err := attachOMDBContentPayloadSpec.Unmarshal(ctx); err != nil {
		return action{}, ctx.error(err)
	}

	return action{
		run: func(ctx executionContext) (classification.Result, error) {
			cl := ctx.result
			if cl.Content == nil {
				return cl, classification.ErrUnmatched
			}

			if ctx.omdbClient == nil {
				return cl, classification.ErrUnmatched
			}

			// Find the IMDB ID from the content's attributes
			imdbID, ok := cl.Content.Identifier("imdb")
			if !ok || imdbID == "" {
				return cl, classification.ErrUnmatched
			}

			result, err := ctx.omdbClient.LookupByIMDBID(ctx.Context, imdbID)
			if err != nil {
				// OMDb failure is non-fatal — content is already classified via TMDB
				return cl, nil
			}

			// Append OMDb attributes to the content
			attrs := map[string]string{
				"rated":       result.Rated,
				"runtime":     result.Runtime,
				"genre":       result.Genre,
				"director":    result.Director,
				"writer":      result.Writer,
				"actors":      result.Actors,
				"plot":        result.Plot,
				"language":    result.Language,
				"country":     result.Country,
				"awards":      result.Awards,
				"poster":      result.Poster,
				"metascore":   result.Metascore,
				"imdb_rating": result.ImdbRating,
				"imdb_votes":  result.ImdbVotes,
				"box_office":  result.BoxOffice,
			}

			for key, value := range attrs {
				if value == "" || value == "N/A" {
					continue
				}
				cl.Content.Attributes = append(cl.Content.Attributes, model.ContentAttribute{
					Source: "omdb",
					Key:    key,
					Value:  value,
				})
			}

			// Store individual ratings (e.g. Rotten Tomatoes, Metacritic)
			for _, rating := range result.Ratings {
				cl.Content.Attributes = append(cl.Content.Attributes, model.ContentAttribute{
					Source: "omdb",
					Key:    "rating_" + sanitizeRatingSource(rating.Source),
					Value:  rating.Value,
				})
			}

			return cl, nil
		},
	}, nil
}

func (attachOMDBContentAction) JSONSchema() JSONSchema {
	return attachOMDBContentPayloadSpec.JSONSchema()
}

// sanitizeRatingSource lowercases and replaces non-alphanumeric chars with underscores.
func sanitizeRatingSource(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			out = append(out, c+32)
		} else if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
