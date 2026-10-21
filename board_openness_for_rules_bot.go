package main

import (
	"strings"

	"github.com/domino14/macondo/board"
)

// Board openness for 90s Wally, the rules bot: how many 8-letter lanes the
// board has, weighted by the NWL23 dictionary (eight_letter_counts_for_rules_bot.go).
// Written from scratch (not dynamic_leaves.go's Openness).
//
// An 8-letter lane is a place an 8-letter play through one tile could go:
//   - a straight run in a row or column holding exactly 7 empty squares
//     and exactly one tile already on the board (the floater) - threading
//     through more would need a 9+ letter word;
//   - the squares just before and after the run are empty (or the edge), so
//     the word doesn't run into another tile;
//   - no new square has a tile beside it (above/below for a row,
//     left/right for a column) - a square that would also form a sideways
//     word isn't open board.
//
// Each lane is worth the share of NWL23 8-letter words with the floater's
// letter in the floater's position: a lane where an S is the 8th letter is
// worth 10898/31736, one where a J is the 5th letter 33/31736. The board's
// total is the sum. Only the dictionary matters, not the tiles left in the
// bag. A word with the floater's letter in two open positions counts twice,
// so a total slightly overstates the share; that doesn't affect comparisons.
// An empty board has no 8-letter lanes.

const boardSize = 15

func occupied(bd *board.GameBoard, row, col int) bool {
	return bd.GetLetter(row, col) != 0
}

// letterIndex is 0 for A ... 25 for Z (a blank counts as the letter it
// stands for), or -1 for anything else.
func letterIndex(bd *board.GameBoard, row, col int) int {
	ml := bd.GetLetter(row, col)
	if ml == 0 {
		return -1
	}
	letter := strings.ToUpper(alph.Letter(ml.Unblank()))
	if len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z' {
		return -1
	}
	return int(letter[0] - 'A')
}

// countEightLetterLanes is the board's 8-letter lanes in both directions,
// weighted as above.
func countEightLetterLanes(bd *board.GameBoard) float64 {
	words := eightLetterLaneWords(bd, false) + eightLetterLaneWords(bd, true)
	return float64(words) / nwl23EightLetterWords
}

// eightLetterLaneWords adds up NWL23 word counts over the lanes along rows
// (down false) or columns (down true). at(line, i) maps a line and a
// position along it to a square.
func eightLetterLaneWords(bd *board.GameBoard, down bool) int {
	at := func(line, i int) (int, int) {
		if down {
			return i, line
		}
		return line, i
	}
	// besideOccupied: a tile next to (line, i) across the run's direction.
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

	words := 0
	for line := 0; line < boardSize; line++ {
		for start := 0; start < boardSize; start++ {
			if start > 0 {
				r, c := at(line, start-1)
				if occupied(bd, r, c) {
					continue // the word would start earlier
				}
			}
			newSquares, tilesInside, besideCount := 0, 0, 0
			floaterAt := -1 // the floater's position along the line
			end := start
			for ; end < boardSize && newSquares < 7; end++ {
				r, c := at(line, end)
				if occupied(bd, r, c) {
					tilesInside++
					floaterAt = end
					continue
				}
				newSquares++
				if besideOccupied(line, end) {
					besideCount++
				}
			}
			if newSquares < 7 {
				break // not enough room left on this line
			}
			// Tiles right after the 7th new square join the word.
			for end < boardSize {
				r, c := at(line, end)
				if !occupied(bd, r, c) {
					break
				}
				tilesInside++
				floaterAt = end
				end++
			}
			if besideCount > 0 || tilesInside != 1 {
				continue
			}
			r, c := at(line, floaterAt)
			if letter := letterIndex(bd, r, c); letter >= 0 {
				words += nwl23EightLetterCounts[letter][floaterAt-start]
			}
		}
	}
	return words
}
