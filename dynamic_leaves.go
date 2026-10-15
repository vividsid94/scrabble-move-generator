package main

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/kwg"
	"github.com/domino14/word-golib/tilemapping"
)

// Dynamic-leave experiment - step 1 only (board-state metrics). The goal:
// getLeaveValue (simulate.go) is a static, context-free lookup, so an S is
// worth the same whether the board is wide open or locked shut. This file
// is the first step toward making it context-aware - pure board metrics,
// nothing wired into getLeaveValue yet, and no adjustment values derived
// yet either. Those need a separate bulk self-play + outcome-logging step
// (not built here) to find out whether a metric actually predicts anything
// beyond what the static table already captures, before any real value
// gets adjusted. This file only answers "can we measure the board," not
// "here's the new S value."
//
// Deliberately NOT reimplementing hook/bingo-lane detection via GADDAG
// internals. ComputeBoardMetrics and findBingoLanePositions below reuse the
// exact same board.MakeBoard / cross_set.GenAllCrossSets /
// movegen.NewGordonGenerator pipeline generateMovesHandler already runs
// (main-for-scrabble.go) - just with synthetic racks (one letter, or seven
// blanks) - so hook/bingo legality is governed by the same dictionary-
// validated generator every real move already goes through, not a second
// implementation that could quietly drift from it.

// BoardMetrics is a pure function of board state - no rack, no move, same
// "board, dictionary, letter distribution only" shape cross_set.GenAllCrossSets
// itself already has.
type BoardMetrics struct {
	// Openness: fraction of empty squares whose cross-set is NOT
	// board.TrivialCrossSet, inverted - 1.0 means no empty square is
	// constrained yet (a fresh board), 0.0 means every empty square already
	// has at least one letter disallowed by an adjacent tile. Deliberately a
	// cross-set-based measure, not a raw "percent of squares filled" one -
	// a board can have few tiles down but already be quite tangled (lots of
	// cross-checks live), or have many tiles down in one dense pocket while
	// the rest of the board is still fully open.
	Openness float64
	// Hooks: for each letter in the `letters` argument passed to
	// ComputeBoardMetrics, every TRUE hook that exists right now - a
	// single new tile added at the front or back of an already-complete
	// existing word (CAT -> SCAT, CAT -> CATS). Deliberately NOT every
	// legal single-tile play: placing a letter below a single pre-existing
	// tile to form a short perpendicular word (e.g. S below A forming
	// "AS") is a parallel play, not a hook of anything, and was inflating
	// this count before the length>=3 filter in findHooks excluded it.
	Hooks map[string][]HookDetail
}

// HookDetail is one true hook's location plus which 1-indexed positions
// (1 through 7) within a REAL bingo word this tile could occupy, if a
// bingo were played perpendicular through it - e.g. for CAT -> CATS, the
// S at the hook square might be playable as the bingo's 1st letter, or its
// 4th, or several positions, depending on what dictionary words actually
// fit in the open space around it. An empty BingoPositions means a real
// hook that's only ever worth a few cheap points right now, not a bingo
// lane - that's the actual signal this whole exercise is after.
type HookDetail struct {
	Row            int   `json:"row"`
	Col            int   `json:"col"`
	BingoPositions []int `json:"bingoPositions"`
}

func isEmptySquare(bd *board.GameBoard, row, col int) bool {
	return bd.GetLetter(row, col) == 0
}

// ComputeOpenness requires GenAllCrossSets to have already run on bd (same
// precondition every other caller of GetCrossSet already has).
func ComputeOpenness(bd *board.GameBoard) float64 {
	dim := bd.Dim()
	emptyCount := 0
	constrainedCount := 0
	for row := 0; row < dim; row++ {
		for col := 0; col < dim; col++ {
			if !isEmptySquare(bd, row, col) {
				continue
			}
			emptyCount++
			h := bd.GetCrossSet(row, col, board.HorizontalDirection)
			v := bd.GetCrossSet(row, col, board.VerticalDirection)
			if h != board.TrivialCrossSet || v != board.TrivialCrossSet {
				constrainedCount++
			}
		}
	}
	if emptyCount == 0 {
		// Fully covered board - reads as maximally "closed" rather than
		// dividing by zero.
		return 0
	}
	return 1 - float64(constrainedCount)/float64(emptyCount)
}

// findBingoLanePositions: for every square touched by at least one real
// 7-tile bingo placement right now (an all-blank rack search -
// dictionary-validated the same way every other check in this file is,
// not a raw empty-space count), record which 1-indexed position (1-7)
// within that bingo word the square occupies - e.g. a square that's the
// 3rd letter of one valid bingo and the 6th letter of a different valid
// bingo gets both 3 and 6 recorded. A square can only ever be reached by a
// bingo whose start is within 6 squares of it in that bingo's own
// direction (a 7-tile word spans at most 6 squares past wherever the hook
// tile sits), so this inherently only ever looks at the 13-square window
// centered on each square - 6 either side plus itself - without needing a
// separate explicit scan for that window.
//
// Split by the BINGO's OWN direction, not the hook's: a hook only "opens a
// bingo lane" through a PERPENDICULAR play (the hook tile becomes a fresh
// cross-anchor for a word running the other way), so horizontal and
// vertical bingo positions have to stay in separate maps rather than one
// combined set; a hook gets checked against whichever map runs the
// opposite direction from the word it extends (see ComputeBoardMetrics).
func findBingoLanePositions(bd *board.GameBoard, gd *kwg.KWG, ld *tilemapping.LetterDistribution, alph *tilemapping.TileMapping) (horizontal map[[2]int]map[int]bool, vertical map[[2]int]map[int]bool) {
	rack := tilemapping.RackFromString("???????", alph)
	generator := movegen.NewGordonGenerator(gd, bd, ld)
	moves := generator.GenAll(rack, false)
	horizontal = make(map[[2]int]map[int]bool)
	vertical = make(map[[2]int]map[int]bool)
	for _, m := range moves {
		if m.TilesPlayed() != 7 {
			continue
		}
		row, col, isVertical := m.CoordsAndVertical()
		length := len(m.Tiles())
		target := horizontal
		if isVertical {
			target = vertical
		}
		for i := 0; i < length; i++ {
			sq := [2]int{row, col + i}
			if isVertical {
				sq = [2]int{row + i, col}
			}
			if target[sq] == nil {
				target[sq] = make(map[int]bool)
			}
			target[sq][i+1] = true // 1-indexed position within the bingo word
		}
	}
	return horizontal, vertical
}

// sortedPositions converts a position set into a deterministic sorted
// slice for JSON output (map iteration order isn't stable) - nil, not an
// empty slice, when there's nothing, so an uninvolved hook serializes as
// "bingoPositions":null rather than [] (cheap, honest signal of "checked,
// found nothing" either way, but nil is what a zero-value map lookup
// naturally produces here without extra code).
func sortedPositions(positions map[int]bool) []int {
	if len(positions) == 0 {
		return nil
	}
	result := make([]int, 0, len(positions))
	for p := range positions {
		result = append(result, p)
	}
	sort.Ints(result)
	return result
}

// ComputeBoardMetrics runs GenAllCrossSets/UpdateAllAnchors once (bd is
// mutated - same expectation every handler that calls GenAllCrossSets
// already has, not a new side effect introduced here), then computes
// openness and, for each requested letter (e.g. []string{"S"}), every true
// hook along with which bingo positions (if any) it could fill. The blank
// isn't a meaningful letter to pass here - its dynamic value is driven by
// Openness alone, not a hook count (a blank "hooking" is really just "some
// letter can hook," which isn't a property of the blank specifically) -
// see boardMetricsHandler's own default.
func ComputeBoardMetrics(bd *board.GameBoard, gd *kwg.KWG, ld *tilemapping.LetterDistribution, alph *tilemapping.TileMapping, letters []string) BoardMetrics {
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	horizontalBingoPositions, verticalBingoPositions := findBingoLanePositions(bd, gd, ld, alph)

	hooks := make(map[string][]HookDetail, len(letters))
	for _, letter := range letters {
		rack := tilemapping.RackFromString(letter, alph)
		generator := movegen.NewGordonGenerator(gd, bd, ld)
		moves := generator.GenAll(rack, false)

		var found []HookDetail
		for _, m := range moves {
			// TilesPlayed()==1 + word length>=3 is the TRUE-hook filter:
			// any run of 2+ adjacent same-direction tiles already on the
			// board must have been validated as a real word when it was
			// placed (Scrabble enforces that on every play), so "1 new
			// tile + a 2-letter-or-longer existing stem" reliably means
			// "extending an actual word." Length exactly 2 means the
			// "stem" was just a single pre-existing letter - not a word,
			// just a perpendicular 2-letter parallel play (e.g. S below a
			// lone A forming "AS") - and a single new tile can never land
			// in the MIDDLE of an existing word (every board square is
			// permanent once filled), so front/back is the only way
			// length>=3 happens.
			if m.TilesPlayed() != 1 || len(m.Tiles()) < 3 {
				continue
			}
			// CoordsAndVertical() returns the START of the whole resulting
			// word, not the new tile's own square - for a front hook
			// (SCAT, new tile first) those are the same square, but for a
			// back hook (CATS, new tile last) they're not: this would
			// otherwise report the pre-existing C's position instead of
			// the actual new S. Tiles() marks played-through squares as 0
			// and the one genuinely new tile as its real (non-zero) letter
			// value, so its index IS the offset from the word's start to
			// the actual hook square.
			startRow, startCol, vertical := m.CoordsAndVertical()
			tiles := m.Tiles()
			newOffset := -1
			for i, t := range tiles {
				if t != 0 {
					newOffset = i
					break
				}
			}
			if newOffset == -1 {
				continue // shouldn't happen given TilesPlayed()==1, but don't report a wrong square if it ever does
			}
			row, col := startRow, startCol
			if vertical {
				row += newOffset
			} else {
				col += newOffset
			}
			// Hook extends a horizontal word (vertical=false) -> the
			// bingo lane it opens runs vertically, and vice versa.
			perpendicular := verticalBingoPositions
			if vertical {
				perpendicular = horizontalBingoPositions
			}
			found = append(found, HookDetail{
				Row:            row,
				Col:            col,
				BingoPositions: sortedPositions(perpendicular[[2]int{row, col}]),
			})
		}
		hooks[letter] = found
	}

	return BoardMetrics{
		Openness: ComputeOpenness(bd),
		Hooks:    hooks,
	}
}

// BoardMetricsRequest mirrors GenerateMovesRequest's own board/premiumSquares/
// lexicon shape exactly (main-for-scrabble.go) - no rack, since this is a
// pure board-state question, plus Letters for which hook counts to compute
// (defaults to just S if omitted - see ComputeBoardMetrics on why the
// blank isn't a sensible entry here).
type BoardMetricsRequest struct {
	Board          [][]string      `json:"board"`
	PremiumSquares []PremiumSquare `json:"premiumSquares,omitempty"`
	Lexicon        string          `json:"lexicon,omitempty"`
	Letters        []string        `json:"letters,omitempty"`
}

type BoardMetricsResponse struct {
	Openness float64                 `json:"openness"`
	Hooks    map[string][]HookDetail `json:"hooks"`
	Lexicon  string                  `json:"lexicon"`
}

// boardMetricsHandler - a standalone way to sanity-check ComputeBoardMetrics
// against a real position (paste a board, see the numbers) before any bulk
// self-play/logging work gets built on top of it. Board parsing deliberately
// copies generateMovesHandler's own loop byte-for-byte rather than factoring
// out a shared helper - see this file's own top comment on not wanting a
// second, potentially-diverging implementation; the one risk worth taking
// on here is duplication, not behavioral drift in how a request's tiles get
// read onto the board.
func boardMetricsHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req BoardMetricsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.Board) != 15 {
		http.Error(w, "Board must have 15 rows", http.StatusBadRequest)
		return
	}
	for i := range req.Board {
		if len(req.Board[i]) != 15 {
			http.Error(w, "Each board row must have 15 columns", http.StatusBadRequest)
			return
		}
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	boardLayout := createCustomBoardLayout(req.PremiumSquares)
	bd := board.MakeBoard(boardLayout)

	tilesPlayed := 0
	for row := 0; row < 15; row++ {
		for col := 0; col < 15; col++ {
			tile := req.Board[row][col]
			if tile != "" {
				if ml, err := alph.Val(tile); err == nil {
					bd.SetLetter(row, col, ml)
					tilesPlayed++
				}
			}
		}
	}
	bd.TestSetTilesPlayed(tilesPlayed)

	letters := req.Letters
	if len(letters) == 0 {
		letters = []string{"S"}
	}

	metrics := ComputeBoardMetrics(bd, gd, ld, alph, letters)

	json.NewEncoder(w).Encode(BoardMetricsResponse{
		Openness: metrics.Openness,
		Hooks:    metrics.Hooks,
		Lexicon:  lexiconName,
	})
}
