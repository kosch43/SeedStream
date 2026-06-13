package indexer

import "testing"

func TestParseCapsXMLParsesSupportedParams(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <limits max="500" default="250"/>
  <searching>
    <search available="yes" supportedParams="q"/>
    <tv-search available="yes" supportedParams="q,rid,tvdbid,tmdbid,imdbid,season,ep"/>
    <movie-search available="yes" supportedParams="q,imdbid,tmdbid,year"/>
  </searching>
  <categories></categories>
</caps>`)

	caps, err := ParseCapsXML(data)
	if err != nil {
		t.Fatalf("ParseCapsXML() error = %v", err)
	}

	if !caps.Searching.TVSearchSupportedParams["tvdbid"] {
		t.Fatal("expected tv-search supported params to include tvdbid")
	}
	if !caps.Searching.TVSearchSupportedParams["tmdbid"] {
		t.Fatal("expected tv-search supported params to include tmdbid")
	}
	if !caps.Searching.TVSearchSupportedParams["imdbid"] {
		t.Fatal("expected tv-search supported params to include imdbid")
	}
	if !caps.Searching.MovieSearchSupportedParams["tmdbid"] {
		t.Fatal("expected movie-search supported params to include tmdbid")
	}
	if !caps.Searching.SearchSupportedParams["q"] {
		t.Fatal("expected search supported params to include q")
	}
}
