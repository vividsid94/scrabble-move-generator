package main

import (
	"strings"

	"github.com/domino14/macondo/board"
	"github.com/domino14/word-golib/tilemapping"
)

// 7-letter lanes for 90s Wally, the rules bot: places a 7-tile play could go
// by hooking a word already on the board. The move generator's cross-checks
// are used only to find anchors; everything else is board geometry, written
// from scratch, then weighted by the NWL23 dictionary
// (seven_letter_counts_for_rules_bot.go).
//
// An anchor is an empty square next to a tile where at least one letter
// would form a valid word with it (its cross-check isn't empty). A 7-letter
// play through an anchor runs perpendicular to the word it hooks: across
// for a tile above or below the anchor, down for a tile left or right.
//
// A lane is 7 squares along that line containing the anchor, where:
//   - all 7 squares are empty (a tile inside would make it an 8);
//   - the squares just before and after are empty (or the edge), so the
//     word doesn't run into another tile;
//   - no square other than the anchor has a tile beside it - a second
//     touch would be a parallel play, which counts as closed.
//
// Each lane is worth the share of NWL23 7-letter words that have, in the
// anchor's spot, any letter the anchor's cross-check allows: a lane where
// the anchor is the 7th letter and only takes S is worth 8346/25473. The
// board's 7-letter openness is the sum over every lane. Only the dictionary
// matters, not the tiles left in the bag.

// sevenLetterOpenness is the board's 7-letter openness in both
// directions. bd's cross-sets must be up to date.
func sevenLetterOpenness(bd *board.GameBoard) float64 {
	words := sevenLetterLaneWords(bd, false) + sevenLetterLaneWords(bd, true)
	return float64(words) / nwl23SevenLetterWords
}

// anchorWords: how many NWL23 7-letter words have a letter the cross-check
// allows at position spot (0-6).
func anchorWords(cs board.CrossSet, spot int) int {
	words := 0
	for ml := tilemapping.MachineLetter(1); ml <= 26; ml++ {
		if !cs.Allowed(ml) {
			continue
		}
		letter := strings.ToUpper(alph.Letter(ml))
		if len(letter) == 1 && letter[0] >= 'A' && letter[0] <= 'Z' {
			words += nwl23SevenLetterCounts[letter[0]-'A'][spot]
		}
	}
	return words
}

// sevenLetterLaneWords adds up NWL23 word counts over the lanes for plays
// across (down false) or down (down true). at(line, i) maps a line and a
// position along it to a square.
func sevenLetterLaneWords(bd *board.GameBoard, down bool) int {
	at := func(line, i int) (int, int) {
		if down {
			return i, line
		}
		return line, i
	}
	// besideOccupied: a tile next to (line, i) across the play's direction.
	besideOccupied := func(line, i int) bool {
		for _, d := range []int{-1, 1} {
			if l := line + d; l >= 0 && l < boardSize {
				r, c := at(l, i)
				if occupied(bd, r, c) {
					return true
				}
			}
		}
		return false
	}
	// A play across is checked against up/down words, which macondo keeps
	// as the VerticalDirection cross-set (and the reverse for a play down;
	// see dynamic_leaves.go's bingoWindowIsOpen).
	crossDir := board.VerticalDirection
	if down {
		crossDir = board.HorizontalDirection
	}

	words := 0
	for line := 0; line < boardSize; line++ {
		for anchor := 0; anchor < boardSize; anchor++ {
			r, c := at(line, anchor)
			if occupied(bd, r, c) || !besideOccupied(line, anchor) {
				continue
			}
			cs := bd.GetCrossSet(r, c, crossDir)
			if cs == 0 {
				continue
			}
			for start := anchor - 6; start <= anchor; start++ {
				end := start + 6
				if start < 0 || end >= boardSize {
					continue
				}
				if start > 0 {
					if br, bc := at(line, start-1); occupied(bd, br, bc) {
						continue
					}
				}
				if end < boardSize-1 {
					if ar, ac := at(line, end+1); occupied(bd, ar, ac) {
						continue
					}
				}
				free := true
				for i := start; i <= end && free; i++ {
					sr, sc := at(line, i)
					if occupied(bd, sr, sc) || (i != anchor && besideOccupied(line, i)) {
						free = false
					}
				}
				if free {
					words += anchorWords(cs, anchor-start)
				}
			}
		}
	}
	return words
}
