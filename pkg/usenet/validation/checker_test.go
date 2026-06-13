package validation

import (
	"strings"
	"testing"

	"seedstream/pkg/media/nzb"
)

func TestSelectValidationFileUsesRequestedEpisode(t *testing.T) {
	nzbData := &nzb.NZB{Files: []nzb.File{
		{Subject: "Show.S01E06.1080p.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 900}}},
		{Subject: "Show.S01E05.1080p.mkv", Segments: []nzb.Segment{{ID: "<b>", Bytes: 500}}},
	}}

	info := selectValidationFile(nzbData, 1, 5)
	if info == nil {
		t.Fatal("expected validation file")
	}
	if !strings.Contains(info.Filename, "S01E05") {
		t.Fatalf("expected episode 5 validation file, got %q", info.Filename)
	}
}

func TestGetSampleArticlesForEpisodeUsesRequestedPackVolume(t *testing.T) {
	checker := &Checker{sampleSize: 2}
	nzbData := &nzb.NZB{Files: []nzb.File{
		{Subject: "Show.S02.COMPLETE.part02.rar", Segments: []nzb.Segment{{ID: "<wrong>", Bytes: 450}}},
		{Subject: "Show.S01.COMPLETE.part01.rar", Segments: []nzb.Segment{{ID: "<wanted-first>", Bytes: 300}, {ID: "<wanted-last>", Bytes: 301}}},
		{Subject: "Show.S01.COMPLETE.part02.rar", Segments: []nzb.Segment{{ID: "<other>", Bytes: 350}}},
	}}

	articles := checker.getSampleArticlesForEpisode(nzbData, 1, 5)
	if len(articles) != 2 {
		t.Fatalf("expected 2 sample articles, got %d", len(articles))
	}
	if articles[0] != "<wanted-first>" || articles[1] != "<wanted-last>" {
		t.Fatalf("expected requested pack volume samples, got %#v", articles)
	}
}

func TestCompressionTypeForValidationUsesRequestedEpisodeGroup(t *testing.T) {
	nzbData := &nzb.NZB{Files: []nzb.File{
		{Subject: "Show.S01E05.1080p.mkv", Segments: []nzb.Segment{{ID: "<a>", Bytes: 300}}},
		{Subject: "Show.S02.COMPLETE.part01.rar", Segments: []nzb.Segment{{ID: "<b>", Bytes: 900}}},
		{Subject: "Show.S02.COMPLETE.part02.rar", Segments: []nzb.Segment{{ID: "<c>", Bytes: 950}}},
	}}

	if ct := compressionTypeForValidation(nzbData, 1, 5); ct != "direct" {
		t.Fatalf("expected direct compression for requested episode, got %q", ct)
	}
}
