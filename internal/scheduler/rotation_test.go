package scheduler

import (
	"fmt"
	"testing"
	"time"

	"gemsub/internal/parser"
)

func makeCandidate(link string) parser.Candidate {
	cand, err := parser.Parse(link)
	if err != nil {
		return parser.Candidate{Link: link}
	}
	return cand
}

func linksOf(candidates []parser.Candidate) []string {
	res := make([]string, len(candidates))
	for i, c := range candidates {
		res[i] = c.Link
	}
	return res
}

func TestRotator_EmptyCandidates(t *testing.T) {
	rot := newCandidateRotator()
	sel := rot.SelectCandidates(nil, 5)
	if sel != nil {
		t.Fatalf("expected nil for empty candidates, got %v", sel)
	}

	sel2 := rot.SelectCandidates([]parser.Candidate{}, 0)
	if sel2 != nil {
		t.Fatalf("expected nil for empty candidates, got %v", sel2)
	}
}

func TestRotator_ProbeLimitZeroAndLarger(t *testing.T) {
	rot := newCandidateRotator()
	cands := []parser.Candidate{
		makeCandidate("vless://user@host1.com:443#A"),
		makeCandidate("vless://user@host2.com:443#B"),
		makeCandidate("vless://user@host3.com:443#C"),
	}

	// ProbeLimit = 0: All selected
	sel0 := rot.SelectCandidates(cands, 0)
	if len(sel0) != 3 {
		t.Fatalf("expected 3 candidates when ProbeLimit=0, got %d", len(sel0))
	}

	// ProbeLimit >= len(cands): All selected
	sel5 := rot.SelectCandidates(cands, 5)
	if len(sel5) != 3 {
		t.Fatalf("expected 3 candidates when ProbeLimit=5, got %d", len(sel5))
	}

	// ProbeLimit == len(cands): All selected
	sel3 := rot.SelectCandidates(cands, 3)
	if len(sel3) != 3 {
		t.Fatalf("expected 3 candidates when ProbeLimit=3, got %d", len(sel3))
	}
}

func TestRotator_FairRotationCycles(t *testing.T) {
	rot := newCandidateRotator()
	cands := []parser.Candidate{
		makeCandidate("vless://user@host1.com:443#A"),
		makeCandidate("vless://user@host2.com:443#B"),
		makeCandidate("vless://user@host3.com:443#C"),
		makeCandidate("vless://user@host4.com:443#D"),
		makeCandidate("vless://user@host5.com:443#E"),
		makeCandidate("vless://user@host6.com:443#F"),
	}
	k := 2

	now := time.Now()

	// Cycle 1: should select A, B
	c1 := rot.SelectCandidates(cands, k)
	if len(c1) != 2 || c1[0].Link != cands[0].Link || c1[1].Link != cands[1].Link {
		t.Fatalf("Cycle 1: expected [A, B], got %v", linksOf(c1))
	}
	// Simulate completed probes for A, B
	rot.RecordTested(c1[0].Link, now.Add(1*time.Second))
	rot.RecordTested(c1[1].Link, now.Add(2*time.Second))

	// Cycle 2: should select C, D
	c2 := rot.SelectCandidates(cands, k)
	if len(c2) != 2 || c2[0].Link != cands[2].Link || c2[1].Link != cands[3].Link {
		t.Fatalf("Cycle 2: expected [C, D], got %v", linksOf(c2))
	}
	rot.RecordTested(c2[0].Link, now.Add(3*time.Second))
	rot.RecordTested(c2[1].Link, now.Add(4*time.Second))

	// Cycle 3: should select E, F
	c3 := rot.SelectCandidates(cands, k)
	if len(c3) != 2 || c3[0].Link != cands[4].Link || c3[1].Link != cands[5].Link {
		t.Fatalf("Cycle 3: expected [E, F], got %v", linksOf(c3))
	}
	rot.RecordTested(c3[0].Link, now.Add(5*time.Second))
	rot.RecordTested(c3[1].Link, now.Add(6*time.Second))

	// Cycle 4: should wrap around and select A, B
	c4 := rot.SelectCandidates(cands, k)
	if len(c4) != 2 || c4[0].Link != cands[0].Link || c4[1].Link != cands[1].Link {
		t.Fatalf("Cycle 4: expected [A, B], got %v", linksOf(c4))
	}
}

func TestRotator_NewCandidatePriority(t *testing.T) {
	rot := newCandidateRotator()
	candA := makeCandidate("vless://user@host1.com:443#A")
	candB := makeCandidate("vless://user@host2.com:443#B")
	candC := makeCandidate("vless://user@host3.com:443#C")
	candD := makeCandidate("vless://user@host4.com:443#D")
	candX := makeCandidate("vless://user@host9.com:443#X")

	cands1 := []parser.Candidate{candA, candB, candC, candD}
	now := time.Now()

	// Cycle 1: selects A, B
	c1 := rot.SelectCandidates(cands1, 2)
	rot.RecordTested(c1[0].Link, now.Add(1*time.Second))
	rot.RecordTested(c1[1].Link, now.Add(2*time.Second))

	// Cycle 2: Introduce X. Candidates are A, B, C, D, X.
	// Untested: C, D, X (zero timestamp).
	// With limit 2, 2 of the 3 untested must be selected (A and B must NOT be selected).
	cands2 := []parser.Candidate{candA, candB, candC, candD, candX}
	c2 := rot.SelectCandidates(cands2, 2)
	if len(c2) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(c2))
	}

	for _, c := range c2 {
		if c.Link == candA.Link || c.Link == candB.Link {
			t.Errorf("expected untested candidate to be prioritized over tested candidate, got %s", c.Link)
		}
	}

	// Now test all in c2
	rot.RecordTested(c2[0].Link, now.Add(3*time.Second))
	rot.RecordTested(c2[1].Link, now.Add(4*time.Second))

	// Cycle 3: The remaining untested candidate MUST be selected first!
	c3 := rot.SelectCandidates(cands2, 2)
	// One candidate in cands2 has never been tested. It must be at index 0 of c3.
	hasUntested := false
	for _, c := range cands2 {
		if c.Link != c1[0].Link && c.Link != c1[1].Link && c.Link != c2[0].Link && c.Link != c2[1].Link {
			if c3[0].Link != c.Link {
				t.Fatalf("expected remaining untested candidate %s to be selected first, got %s", c.Link, c3[0].Link)
			}
			hasUntested = true
			break
		}
	}
	if !hasUntested {
		t.Fatal("expected an untested candidate")
	}
}

func TestRotator_CandidateRemovalAndPruneStale(t *testing.T) {
	rot := newCandidateRotator()
	candA := makeCandidate("vless://user@host1.com:443#A")
	candB := makeCandidate("vless://user@host2.com:443#B")
	candC := makeCandidate("vless://user@host3.com:443#C")
	candD := makeCandidate("vless://user@host4.com:443#D")

	now := time.Now()
	// Cycle 1: probe A, B, C
	rot.RecordTested(candA.Link, now.Add(1*time.Second))
	rot.RecordTested(candB.Link, now.Add(2*time.Second))
	rot.RecordTested(candC.Link, now.Add(500*time.Millisecond))

	// Candidate C is removed upstream. Active links: A, B, D.
	linkSet := map[string]struct{}{
		candA.Link: {},
		candB.Link: {},
		candD.Link: {},
	}
	rot.PruneStale(linkSet)

	cands2 := []parser.Candidate{candA, candB, candD}
	// D is untested -> prioritized first. A is oldest tested -> prioritized second.
	c2 := rot.SelectCandidates(cands2, 2)
	if len(c2) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(c2))
	}
	if c2[0].Link != candD.Link {
		t.Errorf("expected D (untested) first, got %s", c2[0].Link)
	}
	if c2[1].Link != candA.Link {
		t.Errorf("expected A (oldest tested) second, got %s", c2[1].Link)
	}
}

func TestRotator_FeedReorderingInvariance(t *testing.T) {
	rot := newCandidateRotator()
	candA := makeCandidate("vless://user@host1.com:443#A")
	candB := makeCandidate("vless://user@host2.com:443#B")
	candC := makeCandidate("vless://user@host3.com:443#C")
	candD := makeCandidate("vless://user@host4.com:443#D")

	now := time.Now()
	rot.RecordTested(candA.Link, now.Add(1*time.Second))
	rot.RecordTested(candB.Link, now.Add(2*time.Second))
	rot.RecordTested(candC.Link, now.Add(3*time.Second))
	rot.RecordTested(candD.Link, now.Add(4*time.Second))

	// Reorder feed: [D, C, B, A]
	reordered := []parser.Candidate{candD, candC, candB, candA}
	sel := rot.SelectCandidates(reordered, 2)

	// Oldest tested are A (1s) and B (2s).
	if len(sel) != 2 || sel[0].Link != candA.Link || sel[1].Link != candB.Link {
		t.Fatalf("expected [A, B] regardless of feed reordering, got %v", linksOf(sel))
	}
}

func TestRotator_Determinism(t *testing.T) {
	rot := newCandidateRotator()
	var cands []parser.Candidate
	for i := 0; i < 20; i++ {
		cands = append(cands, makeCandidate(fmt.Sprintf("vless://user@host%d.com:443#Node%02d", i, i)))
	}

	sel1 := rot.SelectCandidates(cands, 5)
	sel2 := rot.SelectCandidates(cands, 5)

	if len(sel1) != len(sel2) {
		t.Fatalf("length mismatch: %d vs %d", len(sel1), len(sel2))
	}
	for i := range sel1 {
		if sel1[i].Link != sel2[i].Link {
			t.Errorf("determinism mismatch at %d: %s vs %s", i, sel1[i].Link, sel2[i].Link)
		}
	}
}

func TestRotator_RecordTested_ZeroTimestampIgnored(t *testing.T) {
	rot := newCandidateRotator()
	candA := makeCandidate("vless://user@host1.com:443#A")
	candB := makeCandidate("vless://user@host2.com:443#B")

	validTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	// Record candA with zero timestamp (representing uncompleted / missing timestamp)
	rot.RecordTested(candA.Link, time.Time{})

	// Record candB with valid completion timestamp
	rot.RecordTested(candB.Link, validTime)

	// Candidate A must remain unrecorded (absent from lastTested, timestamp 0)
	cands := []parser.Candidate{candB, candA}
	sel := rot.SelectCandidates(cands, 1)

	if len(sel) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(sel))
	}
	// Untested candidate A must be selected ahead of tested candidate B
	if sel[0].Link != candA.Link {
		t.Errorf("expected candidate A (with zero timestamp) to be prioritized over candidate B, got %s", sel[0].Link)
	}
}

func TestRotator_Fairness_TableDrivenProperties(t *testing.T) {
	cases := []struct {
		name        string
		n           int
		k           int
		epochs      int
		reorderFeed bool
	}{
		{name: "N=7_K=3_Remainder", n: 7, k: 3, epochs: 4, reorderFeed: false},
		{name: "N=7_K=3_FeedReordered", n: 7, k: 3, epochs: 4, reorderFeed: true},
		{name: "N=5_K=1_Sequential", n: 5, k: 1, epochs: 3, reorderFeed: false},
		{name: "N=4_K=4_KEqualsN", n: 4, k: 4, epochs: 3, reorderFeed: false},
		{name: "N=4_K=10_KGreaterThanN", n: 4, k: 10, epochs: 3, reorderFeed: false},
		{name: "N=10_K=3_FeedReordered", n: 10, k: 3, epochs: 4, reorderFeed: true},
		{name: "N=6_K=2_ExactMultiple", n: 6, k: 2, epochs: 4, reorderFeed: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rot := newCandidateRotator()
			cands := make([]parser.Candidate, tc.n)
			selectionCounts := make(map[string]int, tc.n)

			for i := 0; i < tc.n; i++ {
				link := fmt.Sprintf("vless://user@host%02d.com:443#Node%02d", i, i)
				cands[i] = makeCandidate(link)
				selectionCounts[link] = 0
			}

			// Epoch size M = ceil(N / min(N, K))
			kEff := tc.k
			if kEff > tc.n {
				kEff = tc.n
			}
			cyclesPerEpoch := (tc.n + kEff - 1) / kEff
			totalCycles := tc.epochs * cyclesPerEpoch

			simTime := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

			for cycle := 1; cycle <= totalCycles; cycle++ {
				// Prepare input feed
				input := make([]parser.Candidate, len(cands))
				copy(input, cands)

				if tc.reorderFeed {
					// Permute feed order by rotating it by cycle count to ensure feed order does not affect fairness
					shift := cycle % len(input)
					input = append(input[shift:], input[:shift]...)
				}

				selected := rot.SelectCandidates(input, tc.k)

				// Invariant 3: Per-cycle selected count never exceeds K (and equals min(N, K))
				expectedSelectedCount := kEff
				if len(selected) != expectedSelectedCount {
					t.Fatalf("cycle %d: expected %d selected candidates, got %d", cycle, expectedSelectedCount, len(selected))
				}

				// Account for completed probes
				for _, sel := range selected {
					selectionCounts[sel.Link]++
					simTime = simTime.Add(time.Second)
					rot.RecordTested(sel.Link, simTime)
				}

				// Invariant 2: At every single cycle, max difference between any two candidates' selection counts is <= 1
				minCount, maxCount := -1, -1
				for _, count := range selectionCounts {
					if minCount == -1 || count < minCount {
						minCount = count
					}
					if maxCount == -1 || count > maxCount {
						maxCount = count
					}
				}
				if maxCount-minCount > 1 {
					t.Fatalf("cycle %d: fairness violation, max count (%d) - min count (%d) > 1; distribution: %v",
						cycle, maxCount, minCount, selectionCounts)
				}
			}

			// Invariant 1: Every candidate has been selected at least tc.epochs times
			for link, count := range selectionCounts {
				if count < tc.epochs {
					t.Errorf("candidate %s starved: selected only %d times in %d epochs", link, count, tc.epochs)
				}
			}
		})
	}
}
