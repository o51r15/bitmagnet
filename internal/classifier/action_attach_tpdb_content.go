package classifier

import (
	"strings"

	"github.com/bitmagnet-io/bitmagnet/internal/classifier/classification"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/tpdb"
)

const attachTPDBContentName = "attach_tpdb_content"

type attachTPDBContentAction struct{}

func (attachTPDBContentAction) name() string {
	return attachTPDBContentName
}

var attachTPDBContentPayloadSpec = payloadLiteral[string]{
	literal:     attachTPDBContentName,
	description: "Search ThePornDB for XXX content and attach metadata (title, date, studio, performers)",
}

func (attachTPDBContentAction) compileAction(ctx compilerContext) (action, error) {
	if _, err := attachTPDBContentPayloadSpec.Unmarshal(ctx); err != nil {
		return action{}, ctx.error(err)
	}

	return action{
		run: func(ctx executionContext) (classification.Result, error) {
			cl := ctx.result

			if ctx.tpdbClient == nil {
				return cl, classification.ErrUnmatched
			}

			if !cl.BaseTitle.Valid {
				return cl, classification.ErrUnmatched
			}

			// Only use TPDB for XXX content.
			if cl.ContentType.ContentType != model.ContentTypeXxx {
				return cl, classification.ErrUnmatched
			}

			searchResult, err := ctx.tpdbClient.SearchScenes(ctx.Context, cl.BaseTitle.String)
			if err != nil {
				return cl, err
			}

			if len(searchResult.Data) == 0 {
				return cl, classification.ErrUnmatched
			}

			// Use Levenshtein matching on titles like TMDB does.
			bestMatch, ok := levenshteinFindBestMatch(
				cl.BaseTitle.String,
				searchResult.Data,
				func(item tpdb.SearchResult) []string {
					return []string{item.Title}
				},
			)

			if !ok {
				// Fallback: just use the first result if Levenshtein doesn't match well.
				bestMatch = searchResult.Data[0]
			}

			// Build content model from TPDB result.
			content := &model.Content{
				Type:   model.ContentTypeXxx,
				Source: "tpdb",
				ID:     bestMatch.ID,
				Title:  bestMatch.Title,
				Adult:  model.NewNullBool(true),
			}

			if bestMatch.Date != "" {
				if d, err := model.NewDateFromIsoString(bestMatch.Date); err == nil {
					content.ReleaseDate = d
					content.ReleaseYear = d.Year
				}
			}

			if bestMatch.Description != "" {
				content.Overview = model.NullString{String: bestMatch.Description, Valid: true}
			}

			var attrs []model.ContentAttribute

			if bestMatch.Site != nil && bestMatch.Site.Name != "" {
				attrs = append(attrs, model.ContentAttribute{
					Source: "tpdb",
					Key:    "studio",
					Value:  bestMatch.Site.Name,
				})
			}

			// Collect performer names.
			var performers []string
			for _, p := range bestMatch.Performers {
				if p.Name != "" {
					performers = append(performers, p.Name)
				}
			}
			if len(performers) > 0 {
				attrs = append(attrs, model.ContentAttribute{
					Source: "tpdb",
					Key:    "performers",
					Value:  strings.Join(performers, ", "),
				})
			}

			// Collect tags.
			var tagNames []string
			for _, t := range bestMatch.Tags {
				if t.Name != "" {
					tagNames = append(tagNames, t.Name)
				}
			}
			if len(tagNames) > 0 {
				attrs = append(attrs, model.ContentAttribute{
					Source: "tpdb",
					Key:    "tags",
					Value:  strings.Join(tagNames, ", "),
				})
			}

			content.Attributes = attrs

			cl.AttachContent(content)
			return cl, nil
		},
	}, nil
}

func (attachTPDBContentAction) JSONSchema() JSONSchema {
	return attachTPDBContentPayloadSpec.JSONSchema()
}
