package ice

import "sort"

// Preference orders CandidateTypes from most to least preferred.
func (t CandidateType) lessThan(o CandidateType) bool {
	return t.Preference() > o.Preference()
}

// Rank returns the candidates sorted from most to least preferred. Preference is
// by type weight (host > srflx > prflx > relay); ties are broken by descending
// priority, then by network and address for a stable, deterministic order.
func Rank(cands []Candidate) []Candidate {
	out := make([]Candidate, len(cands))
	copy(out, cands)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Type != b.Type {
			return a.Type.lessThan(b.Type)
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.NetworkLower() != b.NetworkLower() {
			return a.NetworkLower() < b.NetworkLower()
		}
		return a.Key() < b.Key()
	})
	return out
}

// Preferred returns the single most-preferred candidate, or the zero Candidate
// when cands is empty.
func Preferred(cands []Candidate) Candidate {
	if len(cands) == 0 {
		return Candidate{}
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if c.Type.lessThan(best.Type) ||
			(c.Type == best.Type && c.Priority > best.Priority) {
			best = c
		}
	}
	return best
}

// ByType groups candidates by type, preserving input order within each group.
func ByType(cands []Candidate) map[CandidateType][]Candidate {
	out := make(map[CandidateType][]Candidate)
	for _, c := range cands {
		out[c.Type] = append(out[c.Type], c)
	}
	return out
}
