package main

import (
	"strings"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/word-golib/kwg"
)

// Board openness for 90s Wally, the rules bot: how open the board is to
// bingos, read statically from the board - no simulation. It's 7-letter
// openness (seven_letter_openness_for_rules_bot.go) plus 8-letter openness
// (eight_letter_openness_for_rules_bot.go), added with equal weight: tested
// against every split from all-8s to all-7s, equal was as good as any.
//
// This file also holds the board helpers both halves share.

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

// boardOpenness is 7-letter + 8-letter openness. bd's cross-sets must be up
// to date.
func boardOpenness(bd *board.GameBoard) float64 {
	return sevenLetterOpenness(bd) + eightLetterOpenness(bd)
}

// boardAfterMove is a copy of bd with candidate c played and its cross-sets
// updated, for measuring the board the opponent will face. An exchange
// leaves the board as it is, so it returns bd itself (don't modify it).
func boardAfterMove(bd *board.GameBoard, gd *kwg.KWG, c *scoredCandidate) *board.GameBoard {
	if c.isExchange {
		return bd
	}
	after := bd.Copy()
	after.PlayMove(c.move)
	cross_set.UpdateCrossSetsForMove(after, c.move, gd, ld)
	return after
}
