package search

import "seedstream/pkg/release"

func dedupeReleaseKey(rel *release.Release) string {
	if rel == nil {
		return ""
	}
	if rel.DetailsURL != "" {
		return "details:" + rel.DetailsURL
	}
	if rel.GUID != "" {
		return "guid:" + rel.GUID
	}
	return ""
}

func MergeAndDedupeSearchResults(releases []*release.Release) []*release.Release {
	seen := make(map[string]bool)
	var result []*release.Release
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		key := dedupeReleaseKey(rel)
		if key != "" && seen[key] {
			continue
		}
		if key != "" {
			seen[key] = true
		}
		result = append(result, rel)
	}
	return result
}
