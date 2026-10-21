package main

import (
	"sort"
	"strconv"
	"strings"

	"github.com/domino14/macondo/board"
)

// Hotspot load for 90s Wally, the rules bot: how much scoring the next player
// can reach on the board, read statically - no simulation. Ported from the
// prototype checked against Wally-vs-Wally games (r ~0.28 with the
// opponent's next non-bingo score, against ~0.12 for bingo openness).
//
// A play shape is a straight stretch in a row or column with 1-6 new tiles
// (it may run through tiles already on the board), connected to the board,
// both ends clear, and every new tile passing its cross-check (the move
// generator's cross-sets - its only use here). The main word itself isn't
// checked. Each shape is valued by the premium squares its new tiles cover
// (hotspot_values_for_rules_bot.go), most detailed key first:
//   - single premium: its type plus two flags - is the square next to a tile
//     already on the board, and is any such tile a vowel;
//   - two or more: the pattern from the first premium to the last along the
//     word (_ a new tile on a plain square, x a board letter played through),
//     the number of tiles played, and the same flags (either-or across the
//     premium squares);
//   - then the pattern without flags, then just the premium types.
//
// The board's hotspot load is the sum over every shape of how much its value
// beats an ordinary play: max(0, value - 25).

const (
	hotspotMaxNewTiles = 6
	hotspotBaseline    = 25.0
)

var hotspotTypeOrder = map[string]int{"TW": 0, "DW": 1, "TL": 2, "DL": 3}

// hotspotPremium is the premium type ("TW", "DW", "TL", "DL") of an empty
// square, or "" for a plain or occupied one.
func hotspotPremium(bd *board.GameBoard, r, c int) string {
	t, ok := premiumTypeAt(r, c, bd)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(t, "S")
}

// hotspotCombo is the premium types in TW/DW/TL/DL order, joined with " + ",
// or "none".
func hotspotCombo(types []string) string {
	if len(types) == 0 {
		return "none"
	}
	sorted := append([]string(nil), types...)
	sort.SliceStable(sorted, func(i, j int) bool { return hotspotTypeOrder[sorted[i]] < hotspotTypeOrder[sorted[j]] })
	return strings.Join(sorted, " + ")
}

// hotspotComboValue looks up the premium types; a combination too rare to
// have a value falls back to its best sub-combination.
func hotspotComboValue(types []string) float64 {
	if v, ok := hotspotComboValues[hotspotCombo(types)]; ok {
		return v
	}
	best := 0.0
	for mask := 1; mask < (1<<len(types))-1; mask++ {
		var sub []string
		for i, t := range types {
			if mask&(1<<i) != 0 {
				sub = append(sub, t)
			}
		}
		if v := hotspotComboValue(sub); v > best {
			best = v
		}
	}
	return best
}

// hotspotSquare is one square of a shape, along the word.
type hotspotSquare struct {
	premium string // for a new tile; "" otherwise
	isNew   bool
}

// hotspotPattern: for 2+ premium squares under new tiles, the stretch from the
// first to the last ("_" plain new tile, "x" board letter), read whichever way
// round sorts first, plus the tiles played. "" for fewer than 2.
func hotspotPattern(seq []hotspotSquare, newTiles int) string {
	var idx []int
	for i, s := range seq {
		if s.isNew && s.premium != "" {
			idx = append(idx, i)
		}
	}
	if len(idx) < 2 {
		return ""
	}
	var span []string
	for _, s := range seq[idx[0] : idx[len(idx)-1]+1] {
		switch {
		case !s.isNew:
			span = append(span, "x")
		case s.premium != "":
			span = append(span, s.premium)
		default:
			span = append(span, "_")
		}
	}
	forward := strings.Join(span, " ")
	reversed := make([]string, len(span))
	for i, s := range span {
		reversed[len(span)-1-i] = s
	}
	backward := strings.Join(reversed, " ")
	if backward < forward {
		forward = backward
	}
	return forward + " (" + strconv.Itoa(newTiles) + " tiles)"
}

// hotspotFlags: is any of the squares next to a tile on the board, and is any
// such tile a vowel.
func hotspotFlags(bd *board.GameBoard, squares [][2]int) string {
	adjacent, vowel := false, false
	for _, sq := range squares {
		for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			r, c := sq[0]+d[0], sq[1]+d[1]
			if r < 0 || r >= boardSize || c < 0 || c >= boardSize || !occupied(bd, r, c) {
				continue
			}
			adjacent = true
			switch letterIndex(bd, r, c) {
			case 0, 4, 8, 14, 20: // A E I O U
				vowel = true
			}
		}
	}
	switch {
	case !adjacent:
		return "not next to a tile"
	case vowel:
		return "next to a tile · vowel"
	default:
		return "next to a tile · no vowel"
	}
}

// hotspotShapeValue: the most detailed value that exists for a shape.
func hotspotShapeValue(bd *board.GameBoard, seq []hotspotSquare, types []string, premiumSquares [][2]int, newTiles int) float64 {
	pattern := hotspotPattern(seq, newTiles)
	base := pattern
	if base == "" && len(types) == 1 {
		base = types[0]
	}
	if base != "" {
		if v, ok := hotspotFlaggedValues[base+" · "+hotspotFlags(bd, premiumSquares)]; ok {
			return v
		}
	}
	if pattern != "" {
		if v, ok := hotspotPatternValues[pattern]; ok {
			return v
		}
	}
	return hotspotComboValue(types)
}

// hotspotLoad is the board's hotspot load for the next player. bd's
// cross-sets must be up to date.
func hotspotLoad(bd *board.GameBoard) float64 {
	empty := true
	for r := 0; r < boardSize && empty; r++ {
		for c := 0; c < boardSize; c++ {
			if occupied(bd, r, c) {
				empty = false
				break
			}
		}
	}
	return hotspotLoadInDirection(bd, false, empty) + hotspotLoadInDirection(bd, true, empty)
}

// On an empty board, a shape through the centre star counts as connected.
func hotspotLoadInDirection(bd *board.GameBoard, down, emptyBoard bool) float64 {
	at := func(line, i int) (int, int) {
		if down {
			return i, line
		}
		return line, i
	}
	// Plays across are checked against up/down words (VerticalDirection
	// cross-sets), plays down against left/right ones.
	crossDir := board.VerticalDirection
	if down {
		crossDir = board.HorizontalDirection
	}
	touches := func(line, i int) bool {
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

	load := 0.0
	for line := 0; line < boardSize; line++ {
		for s := 0; s < boardSize; s++ {
			if s > 0 {
				if r, c := at(line, s-1); occupied(bd, r, c) {
					continue // starts mid-word
				}
			}
			newTiles, tilesIn, connected := 0, 0, false
			var seq []hotspotSquare
			var types []string
			var premiumSquares [][2]int
			for e := s; e < boardSize; e++ {
				r, c := at(line, e)
				if occupied(bd, r, c) {
					tilesIn++
					connected = true
					seq = append(seq, hotspotSquare{})
				} else {
					newTiles++
					if newTiles > hotspotMaxNewTiles {
						break
					}
					if bd.GetCrossSet(r, c, crossDir) == 0 {
						break // no letter fits here
					}
					if touches(line, e) || (emptyBoard && r == 7 && c == 7) {
						connected = true
					}
					premium := hotspotPremium(bd, r, c)
					seq = append(seq, hotspotSquare{premium: premium, isNew: true})
					if premium != "" {
						types = append(types, premium)
						premiumSquares = append(premiumSquares, [2]int{r, c})
					}
				}
				if e+1 < boardSize {
					if nr, nc := at(line, e+1); occupied(bd, nr, nc) {
						continue // the word runs on into the next tile
					}
				}
				if !connected || newTiles == 0 || newTiles+tilesIn < 2 {
					continue
				}
				if v := hotspotShapeValue(bd, seq, types, premiumSquares, newTiles); v > hotspotBaseline {
					load += v - hotspotBaseline
				}
			}
		}
	}
	return load
}
