package main

import "github.com/domino14/macondo/board"

// Board openness for 90s Wally, the rules bot: how many 8-letter lanes the board
// has. Pure board geometry - no dictionary, no hook or letter analysis -
// written from scratch (not dynamic_leaves.go's Openness).
//
// An 8-letter lane is a place a 7-tile play could physically go:
//   - a straight run in a row or column holding exactly 7 empty squares
//     and exactly one tile already on the board (an 8-letter play through
//     it) - threading through more would need a 9+ letter word;
//   - the squares just before and after the run are empty (or the edge), so
//     the word doesn't run into another tile;
//   - it connects to the board through that one tile inside the run;
//   - no new square has a tile beside it (above/below for a row,
//     left/right for a column) - a square that would also form a sideways
//     word isn't open board.
//
// On an empty board, a run through the centre star counts.
//
// Runs overlap, so the count is normalized: a floater (the tile inside the
// run) with all 8 of its possible windows open counts as 1, with 7 of 8
// open as 7/8, and so on. Every run has exactly one tile inside, so that's
// simply the number of runs divided by 8.

const boardSize = 15

func occupied(bd *board.GameBoard, row, col int) bool {
	return bd.GetLetter(row, col) != 0
}

// countEightLetterLanes is the board's 8-letter lanes in both directions, normalized
// as above (runs / 8).
func countEightLetterLanes(bd *board.GameBoard) float64 {
	empty := true
	for r := 0; r < boardSize && empty; r++ {
		for c := 0; c < boardSize; c++ {
			if occupied(bd, r, c) {
				empty = false
				break
			}
		}
	}
	return float64(countEightLetterRunsInDirection(bd, false, empty)+countEightLetterRunsInDirection(bd, true, empty)) / 8
}

// countEightLetterRunsInDirection counts runs along rows (down false) or columns
// (down true). at(line, i) maps a line and a position along it to a square.
func countEightLetterRunsInDirection(bd *board.GameBoard, down bool, emptyBoard bool) int {
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

	lanes := 0
	for line := 0; line < boardSize; line++ {
		for start := 0; start < boardSize; start++ {
			if start > 0 {
				r, c := at(line, start-1)
				if occupied(bd, r, c) {
					continue // the word would start earlier
				}
			}
			newSquares, tilesInside, besideCount := 0, 0, 0
			coversCentre := false
			end := start
			for ; end < boardSize && newSquares < 7; end++ {
				r, c := at(line, end)
				if r == 7 && c == 7 {
					coversCentre = true
				}
				if occupied(bd, r, c) {
					tilesInside++
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
				end++
			}
			if besideCount > 0 || tilesInside > 1 {
				continue
			}
			if emptyBoard {
				if coversCentre {
					lanes++
				}
				continue
			}
			if tilesInside == 1 {
				lanes++
			}
		}
	}
	return lanes
}
