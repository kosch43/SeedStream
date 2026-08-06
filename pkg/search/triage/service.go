package triage

import (
	"math"
	"sort"
	"time"

	"seedstream/pkg/release"
	"seedstream/pkg/search/parser"
)

type Candidate struct {
	Release     *release.Release
	Metadata    *parser.ParsedRelease
	Group       string
	Score       int
	QuerySource string
}

type Service struct{}

func NewService() *Service {
	return &Service{}
}

func (s *Service) Filter(releases []*release.Release) []Candidate {
	var candidates []Candidate

	for _, rel := range releases {
		if rel == nil {
			continue
		}
		if release.IsFullDiscRelease(rel.Title) {
			continue
		}
		parsed := parser.ParseReleaseTitle(rel.Title)
		group := parsed.ResolutionGroup()
		score := ScoreRelease(rel)

		querySource := rel.QuerySource
		if querySource == "" {
			querySource = "id"
		}
		candidates = append(candidates, Candidate{
			Release:     rel,
			Metadata:    parsed,
			Group:       group,
			Score:       score,
			QuerySource: querySource,
		})
	}

	candidates = deduplicateReleases(candidates)

	sort.Slice(candidates, func(i, j int) bool {
		return moreDesirable(&candidates[i], &candidates[j])
	})

	return candidates
}

func (s *Service) SortCandidates(candidates []Candidate) {
	for i := range candidates {
		rel := candidates[i].Release
		if rel == nil {
			continue
		}
		parsed := parser.ParseReleaseTitle(rel.Title)
		group := parsed.ResolutionGroup()
		score := ScoreRelease(rel)
		querySource := rel.QuerySource
		if querySource == "" {
			querySource = "id"
		}
		candidates[i].Metadata = parsed
		candidates[i].Group = group
		candidates[i].Score = score
		candidates[i].QuerySource = querySource
	}
	sort.Slice(candidates, func(i, j int) bool {
		return moreDesirable(&candidates[i], &candidates[j])
	})
}

// moreDesirable reports whether a should sort before b.
//
// Ordering is: anything that can actually be downloaded first, then score desc,
// then seeders desc as a tiebreaker.
//
// The dead-swarm rule exists because score alone put unplayable torrents at the
// top of the list. basicScore rewards size and recency, and its age term is a
// near-unique number, so exact score ties are rare and the seeder tiebreaker
// almost never fired — which let a freshly-posted 4K remux with zero seeders
// outrank a well-seeded 1080p and stall on playback. Sinking confirmed-dead
// torrents fixes that without disturbing quality ordering among the rest.
func moreDesirable(a, b *Candidate) bool {
	aDead, bDead := isDeadSwarm(a.Release), isDeadSwarm(b.Release)
	if aDead != bDead {
		return bDead // a is playable and b is not, so a comes first
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	as, bs := 0, 0
	if a.Release != nil {
		as = a.Release.Seeders
	}
	if b.Release != nil {
		bs = b.Release.Seeders
	}
	return as > bs
}

// isDeadSwarm reports whether a torrent is known to have nobody seeding it.
// It is deliberately conservative: a release whose indexer did not publish a
// seeder count is never treated as dead, because a missing attribute and a
// genuine zero are indistinguishable in the raw value.
func isDeadSwarm(rel *release.Release) bool {
	return rel != nil && rel.IsTorrent() && rel.SeedersKnown && rel.Seeders <= 0
}

// Swarm health is worth roughly one to four size tiers, so a well-seeded
// release can outrank a marginally larger one that would stall, without letting
// seeder count override quality outright.
const (
	seederScoreWeight = 500.0
	maxSeederScore    = 4000
)

// seederScore rewards a healthy swarm on a logarithmic curve, because the
// difference between one seeder and ten decides whether a stream plays at all
// while the difference between five hundred and six hundred is irrelevant.
//
// Only a count the tracker actually published is scored: an indexer that omits
// the seeders attribute reports zero indistinguishably from a dead swarm, and
// penalising it would push every release from that tracker to the bottom.
func seederScore(rel *release.Release) int {
	if rel == nil || !rel.IsTorrent() || !rel.SeedersKnown || rel.Seeders <= 0 {
		return 0
	}
	s := int(seederScoreWeight * math.Log2(1+float64(rel.Seeders)))
	if s > maxSeederScore {
		s = maxSeederScore
	}
	return s
}

// ScoreRelease is the full desirability score: quality first, then swarm health.
// Exported so the watchdog ranks replacement torrents the same way the stream
// list does, rather than by seeder count alone.
func ScoreRelease(rel *release.Release) int {
	return basicScore(rel) + seederScore(rel)
}

func basicScore(rel *release.Release) int {
	score := 0

	// Size score: larger files score higher
	sizeGB := float64(rel.Size) / (1024 * 1024 * 1024)
	if sizeGB > 100 {
		score += 9000
	} else if sizeGB > 50 {
		score += 8000
	} else if sizeGB > 20 {
		score += 7000
	} else if sizeGB > 10 {
		score += 6000
	} else if sizeGB > 5 {
		score += 5000
	} else if sizeGB > 2 {
		score += 4000
	} else if sizeGB > 1 {
		score += 3000
	} else if sizeGB > 0.5 {
		score += 2000
	} else if sizeGB > 0 {
		score += 1000
	}

	// Age score: newer releases score higher
	if rel.PubDate != "" {
		pubTime, err := time.Parse(time.RFC1123Z, rel.PubDate)
		if err != nil {
			pubTime, err = time.Parse(time.RFC1123, rel.PubDate)
		}
		if err == nil {
			ageHours := time.Since(pubTime).Hours()
			ageScore := int(10000.0 - ageHours)
			if ageScore < 0 {
				ageScore = 0
			}
			score += ageScore
		}
	}

	// Grabs score
	score += rel.Grabs

	return score
}

func deduplicateReleases(candidates []Candidate) []Candidate {
	seen := make(map[string]*Candidate)

	for i := range candidates {
		candidate := &candidates[i]

		normalized := release.NormalizeTitleForDedup(candidate.Release.Title)
		if normalized == "" {
			continue
		}

		existing, exists := seen[normalized]
		if !exists {
			seen[normalized] = candidate
			continue
		}

		if candidate.Score > existing.Score {
			seen[normalized] = candidate
		} else if candidate.Score == existing.Score && candidate.QuerySource == "id" && existing.QuerySource != "id" {
			seen[normalized] = candidate
		}
	}

	result := make([]Candidate, 0, len(seen))
	for _, candidate := range seen {
		result = append(result, *candidate)
	}

	return result
}
