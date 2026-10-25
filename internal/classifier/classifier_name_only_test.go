package classifier

import (
	"context"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifierNameOnly covers torrents with no file info (database dump imports),
// which can only be classified from the torrent name. APIs and local search are
// disabled so only the content-type / title-parsing logic is exercised.
func TestClassifierNameOnly(t *testing.T) {
	t.Parallel()

	unknown := model.NullContentType{}
	movie := model.NewNullContentType(model.ContentTypeMovie)
	tv := model.NewNullContentType(model.ContentTypeTvShow)
	xxx := model.NewNullContentType(model.ContentTypeXxx)

	testCases := []struct {
		name          string
		expectedType  model.NullContentType
		expectedTitle string // empty = don't check
	}{
		{"Napoleon.1927.1080p.BluRay.x265-RARBG", movie, "Napoleon"},
		{"Stockton.On.My.Mind.2020.1080p.AMZN.WEBRip.DDP5.1.x264-NTG", movie, "Stockton On My Mind"},
		{"Inside.Taronga.Zoo.S01E04.720p.HDTV.x264-FiHTV[rartv]", tv, "Inside Taronga Zoo"},
		{"Seth.Meyers.2020.07.28.Cameron.Diaz.1080p.WEB.H264-BTX[rartv]", tv, ""},
		{"Sex.Education.S01E01.1080p.WEB.H264-GROUP", tv, "Sex Education"},
		{"MP3-daily-2020-July-28-Techno", model.NewNullContentType(model.ContentTypeMusic), ""},
		{"Pink Floyd - Discography (1967-2014) [FLAC]", model.NewNullContentType(model.ContentTypeMusic), ""},
		{"Stephen King - The Stand (Unabridged) Audiobook", model.NewNullContentType(model.ContentTypeAudiobook), ""},
		{"Learning Go - 2nd Edition (2023) EPUB", model.NewNullContentType(model.ContentTypeEbook), ""},
		{"Adobe.Photoshop.2024.x64.Portable", model.NewNullContentType(model.ContentTypeSoftware), ""},
		// names ending in something extension-like get a synthetic zero-size file upstream:
		{"Adobe.Photoshop.x64.Portable", model.NewNullContentType(model.ContentTypeSoftware), ""},
		{"Some.Movie.Title.2019.1080p.BluRay.x264.mkv", movie, "Some Movie Title"},
		{"Some.Show.S02E05.720p.WEB.x264.mkv", tv, "Some Show"},
		{"A Book About Things (2021).epub", model.NewNullContentType(model.ContentTypeEbook), ""},
		{"JacquieEtMichelTV.20.07.29.Eva.Enjoys.Anal.FRENCH.XXX.1080p.MP4-TRASHBIN", xxx, ""},
		{"CumLouder - Susana Alcala 27.07.2020 mp4", xxx, ""},
		{"RKPrime - Adriana Maya - Backyard Maya 29.07.2020 mp4", xxx, ""},
		{"Hot Japanese girls (NSFW)", xxx, ""},
		{"some random words with nothing useful", unknown, ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mocks := newTestClassifierMocks(t)

			source, err := yamlSourceProvider{rawSourceProvider: coreSourceProvider{}}.source()
			require.NoError(t, err)

			workflow, err := mocks.compiler.Compile(source)
			require.NoError(t, err)

			torrent := model.Torrent{
				Name:        tc.name,
				FilesStatus: model.FilesStatusNoInfo,
			}

			result, err := workflow.Run(context.Background(), "default", Flags{
				"local_search_enabled": false,
				"apis_enabled":         false,
			}, torrent)
			require.NoError(t, err)

			assert.Equal(t, tc.expectedType, result.ContentType, "content type")

			if tc.expectedTitle != "" {
				assert.Equal(t, tc.expectedTitle, result.BaseTitle.String, "base title")
			}
		})
	}
}
