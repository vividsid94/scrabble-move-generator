package main

import "github.com/domino14/macondo/board"

// Board openness for 90s Wally, the rules bot: how many bingo lanes the board
// has. Pure board geometry - no dictionary, no hook or letter analysis -
// written from scratch (not dynamic_leaves.go's Openness).
//
// A bingo lane is a place a 7-tile play could physically go:
//   - a straight run in a row or column holding exactly 7 empty squares
//     and at most one tile already on the board (an 8-letter play through
//     it) - threading through more would need a 9+ letter word;
//   - the squares just before and after the run are empty (or the edge), so
//     the word doesn't run into another tile;
//   - it connects to the board: a tile inside the run, or one new square
//     with a tile beside it (above/below for a row, left/right for a
//     column);
//   - at most one new square has a tile beside it - a run crowded by
//     neighbouring tiles is closed board, not an open lane.
//
// On an empty board, a run through the centre star counts.
//
// Runs overlap: one long open stretch counts once per starting square,
// so a wide-open area scores higher than a single narrow lane.

const boardSize = 15

func occupied(bd *board.GameBoard, row, col int) bool {
	return bd.GetLetter(row, col) != 0
}

// countBingoLanes counts bingo lanes in both directions.
func countBingoLanes(bd *board.GameBoard) int {
	empty := true
	for r := 0; r < boardSize && empty; r++ {
		for c := 0; c < boardSize; c++ {
			if occupied(bd, r, c) {
				empty = false
				break
			}
		}
	}
	return countLanesInDirection(bd, false, empty) + countLanesInDirection(bd, true, empty)
}

// countLanesInDirection counts lanes along rows (down false) or columns
// (down true). at(line, i) maps a line and a position along it to a square.
func countLanesInDirection(bd *board.GameBoard, down bool, emptyBoard bool) int {
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
			if besideCount > 1 || tilesInside > 1 {
				continue
			}
			if emptyBoard {
				if coversCentre {
					lanes++
				}
				continue
			}
			if tilesInside > 0 || besideCount == 1 {
				lanes++
			}
		}
	}
	return lanes
}
