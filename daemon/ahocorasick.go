package main

// A compact Aho-Corasick multi-pattern matcher, used by the literal credential
// scrubber (credscrub.go) to find any of many credential forms in one pass over
// text that can be 512 KiB per turn. Implemented here rather than pulled in as a
// dependency: the algorithm is small and well understood, and proxy/ aside this
// repo keeps its dependency surface deliberately narrow.
//
// Byte-oriented, so it is exact over arbitrary key material (base64, hex,
// %-escapes) with no notion of case or Unicode -- the credential forms are
// matched as the exact bytes they are. Case variants that matter (upper-hex) are
// registered as separate patterns by the caller.

type acNode struct {
	next   map[byte]int // goto edges
	fail   int          // failure link
	outLen []int        // lengths of patterns ending at this node (for match spans)
}

type ahoCorasick struct {
	nodes []acNode
}

type acMatch struct {
	start, end int // text[start:end] is the matched pattern
}

// buildAhoCorasick compiles patterns into an automaton. Empty patterns are
// ignored; the caller (buildCredScrubber) already drops anything shorter than a
// window.
func buildAhoCorasick(patterns []string) *ahoCorasick {
	ac := &ahoCorasick{nodes: []acNode{{next: map[byte]int{}}}} // root = node 0
	for _, p := range patterns {
		if p == "" {
			continue
		}
		cur := 0
		for i := 0; i < len(p); i++ {
			b := p[i]
			nxt, ok := ac.nodes[cur].next[b]
			if !ok {
				nxt = len(ac.nodes)
				ac.nodes = append(ac.nodes, acNode{next: map[byte]int{}})
				ac.nodes[cur].next[b] = nxt
			}
			cur = nxt
		}
		ac.nodes[cur].outLen = append(ac.nodes[cur].outLen, len(p))
	}
	ac.buildFailLinks()
	return ac
}

// buildFailLinks sets every node's failure link by BFS, and copies outputs along
// failure links so a node reports every pattern that ends at it OR at any node
// its failure chain passes through (the standard output-link flattening).
func (ac *ahoCorasick) buildFailLinks() {
	queue := make([]int, 0, len(ac.nodes))
	for _, child := range ac.nodes[0].next {
		ac.nodes[child].fail = 0
		queue = append(queue, child)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for b, child := range ac.nodes[cur].next {
			// Failure link of child: follow cur's failure chain for an edge on b.
			f := ac.nodes[cur].fail
			for f != 0 {
				if _, ok := ac.nodes[f].next[b]; ok {
					break
				}
				f = ac.nodes[f].fail
			}
			if nxt, ok := ac.nodes[f].next[b]; ok && nxt != child {
				ac.nodes[child].fail = nxt
			} else {
				ac.nodes[child].fail = 0
			}
			ac.nodes[child].outLen = append(ac.nodes[child].outLen, ac.nodes[ac.nodes[child].fail].outLen...)
			queue = append(queue, child)
		}
	}
}

// step advances one byte from state and returns the new state, following
// failure links until an edge exists or the root is reached.
func (ac *ahoCorasick) step(state int, b byte) int {
	for {
		if nxt, ok := ac.nodes[state].next[b]; ok {
			return nxt
		}
		if state == 0 {
			return 0
		}
		state = ac.nodes[state].fail
	}
}

// matches returns every pattern occurrence in text as a byte span. A position
// that ends several patterns (an exact form and the fragment windows inside it)
// yields one acMatch each; the caller merges them.
func (ac *ahoCorasick) matches(text string) []acMatch {
	var out []acMatch
	state := 0
	for i := 0; i < len(text); i++ {
		state = ac.step(state, text[i])
		for _, l := range ac.nodes[state].outLen {
			out = append(out, acMatch{start: i - l + 1, end: i + 1})
		}
	}
	return out
}
