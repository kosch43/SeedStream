package stremio

import (
	"seedstream/pkg/release"
	"seedstream/pkg/search/parser"
)

type StreamResponse struct {
	Streams []Stream `json:"streams"`
}

type Stream struct {
	FailoverID string `json:"failoverId,omitempty"`

	URL string `json:"url,omitempty"`

	ExternalUrl string `json:"externalUrl,omitempty"`

	Name string `json:"name,omitempty"`

	Score int `json:"-"`

	ParsedMetadata *parser.ParsedRelease `json:"-"`

	Release *release.Release `json:"-"`

	Title         string         `json:"title,omitempty"`
	Description   string         `json:"description,omitempty"`
	BehaviorHints *BehaviorHints `json:"behaviorHints,omitempty"`
	StreamType    string         `json:"streamType,omitempty"`
}

type BehaviorHints struct {
	NotWebReady      bool     `json:"notWebReady,omitempty"`
	BingeGroup       string   `json:"bingeGroup,omitempty"`
	CountryWhitelist []string `json:"countryWhitelist,omitempty"`
	VideoSize        int64    `json:"videoSize,omitempty"`
	Filename         string   `json:"filename,omitempty"`

	Cached *bool `json:"cached,omitempty"`
}

type SearchReleasesResponse struct {
	Releases []SearchReleaseTag `json:"releases"`
}

type SearchReleaseTag struct {
	Title        string `json:"title"`
	Link         string `json:"link"`
	DetailsURL   string `json:"details_url"`
	Size         int64  `json:"size"`
	Indexer      string `json:"indexer"`
}
