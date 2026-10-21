package main

import (
	"strings"

	"github.com/domino14/macondo/board"
)

// Empty-bag move ranking - the baseline every bot follows once the bag is
// completely empty (not the pre-endgame): Wally, the rank-based bots, the
// Sandbox's custom bots, personas and 90s Wally (whose rules switch off with
// an empty bag) all rank moves by this instead of score + leave. Leave values
// mean nothing when there's nothing left to draw.
//
// It's exactly Quackle's static evaluator for an empty bag
// (CatchallEvaluator::endgameResult in quackle's catchall.cpp) - the regular
// move choice, not the endgame solver:
//   - a play that goes out: score + 2 x the points on the opponent's rack;
//   - any other play: score - 8 - 2.61 x the points left on your own rack.
//
// It only changes how moves are ranked (their equity); each play's real score
// is unchanged, and the end-of-game rack adjustment is still applied by the
// game's normal end-of-game scoring.

const (
	emptyBagNotOutPenalty      = 8.0
	emptyBagLeftoverMultiplier = 2.61
)

// emptyBagEquity is a word play's equity with the bag empty. leave is what
// stays on the player's rack ("" when the play goes out).
func emptyBagEquity(score int, leave string, opponentRackPoints int) float64 {
	if leave == "" {
		return float64(score) + 2*float64(opponentRackPoints)
	}
	return float64(score) - emptyBagNotOutPenalty - emptyBagLeftoverMultiplier*float64(rackPointValue(leave))
}

// unseenTiles is the standard tile set minus the tiles on the board (a blank
// on the board counts as "?") and the given rack.
func unseenTiles(bd *board.GameBoard, rack string) string {
	var onBoard strings.Builder
	for r := 0; r < boardSize; r++ {
		for c := 0; c < boardSize; c++ {
			ml := bd.GetLetter(r, c)
			if ml == 0 {
				continue
			}
			if ml.IsBlanked() {
				onBoard.WriteString("?")
			} else {
				onBoard.WriteString(strings.ToUpper(alph.Letter(ml)))
			}
		}
	}
	return removeRackFromPool(removeRackFromPool(standardTilePool, onBoard.String()), strings.ToUpper(rack))
}

// emptyBagFromBoard: with the board and the player's rack, is the bag empty,
// and how many points are on the opponent's rack? When the bag has tiles the
// opponent holds 7 and the bag at least 1, so 8+ tiles are unseen; with 7 or
// fewer unseen, the bag is empty and the unseen tiles are the opponent's rack.
// For endpoints that don't know the bag (or may not be told its size).
func emptyBagFromBoard(bd *board.GameBoard, rack string) (bool, int) {
	unseen := unseenTiles(bd, rack)
	if len([]rune(unseen)) > 7 {
		return false, 0
	}
	return true, rackPointValue(unseen)
}
