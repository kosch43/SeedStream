package indexer

import "testing"

// TestSeedersKnownDistinguishesMissingFromZero is the root of the whole feature:
// a missing "seeders" attribute and a reported "seeders=0" both read as 0, but
// only the second means nobody is seeding. Acting on the first would penalise
// every result from a tracker that does not publish swarm health.
func TestSeedersKnownDistinguishesMissingFromZero(t *testing.T) {
	cases := []struct {
		name      string
		attrs     []Attribute
		wantCount int
		wantKnown bool
	}{
		{
			name:      "reported zero",
			attrs:     []Attribute{{Name: "seeders", Value: "0"}},
			wantCount: 0,
			wantKnown: true,
		},
		{
			name:      "reported positive",
			attrs:     []Attribute{{Name: "seeders", Value: "42"}},
			wantCount: 42,
			wantKnown: true,
		},
		{
			name:      "attribute absent",
			attrs:     []Attribute{{Name: "grabs", Value: "7"}},
			wantCount: 0,
			wantKnown: false,
		},
		{
			name:      "attribute present but empty",
			attrs:     []Attribute{{Name: "seeders", Value: ""}},
			wantCount: 0,
			wantKnown: false,
		},
		{
			name:      "attribute present but not a number",
			attrs:     []Attribute{{Name: "seeders", Value: "n/a"}},
			wantCount: 0,
			wantKnown: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := &Item{Title: "Some.Release.1080p", Attributes: tc.attrs}
			rel := item.ToRelease()
			if rel.Seeders != tc.wantCount {
				t.Errorf("Seeders = %d, want %d", rel.Seeders, tc.wantCount)
			}
			if rel.SeedersKnown != tc.wantKnown {
				t.Errorf("SeedersKnown = %v, want %v", rel.SeedersKnown, tc.wantKnown)
			}
		})
	}
}
