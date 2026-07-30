package main

import (
	_ "embed"
	"strings"
)

// nwl2023sevens.txt/nwl2023eights.txt are copied verbatim from
// scrabbleapp's public/files/ - the same probability-ordered word lists
// its Snakes feature (src/containers/Snakes/snakesData.js) already loads
// client-side for bingo-stem drilling. One word per line, rank = 1-indexed
// line number (line 1 = most probable/drawable).
//
//go:embed nwl2023sevens.txt
var nwl23SevensData []byte

//go:embed nwl2023eights.txt
var nwl23EightsData []byte

// bingoProbabilityRank maps a 7- or 8-letter word to its 1-indexed rank
// within its own length's list. Sevens and eights are ranked
// independently (separate source lists, same convention Snakes uses) -
// since a word's length makes the two key spaces disjoint, both lists
// share one map with no collision risk. A word of any other length (a 9+
// letter bingo formed by hooking onto board tiles) or one simply absent
// from these NWL23 lists has no entry at all - callers must treat "not
// found" as "no rank restriction," not as "worst possible rank."
var bingoProbabilityRank map[string]int

func init() {
	bingoProbabilityRank = make(map[string]int, 25473+31736)
	loadBingoProbabilityRanks(nwl23SevensData)
	loadBingoProbabilityRanks(nwl23EightsData)
}

func loadBingoProbabilityRanks(data []byte) {
	lines := strings.Split(string(data), "\n")
	rank := 0
	for _, line := range lines {
		word := strings.ToUpper(strings.TrimSpace(line))
		if word == "" {
			continue
		}
		rank++
		bingoProbabilityRank[word] = rank
	}
}
