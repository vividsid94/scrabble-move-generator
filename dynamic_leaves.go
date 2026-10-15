package main

import (
	"encoding/json"
	"net/http"

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
// Deliberately NOT reimplementing hook detection via GADDAG internals.
// countSingleTileMoves below reuses the exact same board.MakeBoard /
// cross_set.GenAllCrossSets / movegen.NewGordonGenerator pipeline
// generateMovesHandler already runs (main-for-scrabble.go) - just with a
// synthetic one-letter rack - so hook legality is governed by the same
// dictionary-validated generator every real move already goes through,
// not a second implementation that could quietly drift from it.

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
	// LiveHooks: for each letter in the `letters` argument passed to
	// ComputeBoardMetrics, how many single-tile moves exist RIGHT NOW that
	// place just that letter - i.e., a real hook onto an existing word,
	// found by literally asking the move generator what a one-tile rack of
	// that letter could do against the current board.
	LiveHooks map[string]int
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

// countSingleTileMoves: generate every legal move for a one-tile rack of
// `letter` against the current board, and count how many of them actually
// place exactly one new tile (TilesPlayed() == 1 is always true for a
// one-tile rack's own plays here, but kept as an explicit check rather than
// just len(moves) in case GenAll ever returns a non-scoring move type like
// a pass for an empty list - belt and suspenders, not load-bearing today).
func countSingleTileMoves(bd *board.GameBoard, gd *kwg.KWG, ld *tilemapping.LetterDistribution, alph *tilemapping.TileMapping, letter string) int {
	rack := tilemapping.RackFromString(letter, alph)
	generator := movegen.NewGordonGenerator(gd, bd, ld)
	moves := generator.GenAll(rack, false)
	count := 0
	for _, m := range moves {
		if m.TilesPlayed() == 1 {
			count++
		}
	}
	return count
}

// ComputeBoardMetrics runs GenAllCrossSets/UpdateAllAnchors once (bd is
// mutated - same expectation every handler that calls GenAllCrossSets
// already has, not a new side effect introduced here), then computes
// openness and, for each requested letter (e.g. []string{"S"} or
// []string{"S", "?"}), its live single-tile hook count.
func ComputeBoardMetrics(bd *board.GameBoard, gd *kwg.KWG, ld *tilemapping.LetterDistribution, alph *tilemapping.TileMapping, letters []string) BoardMetrics {
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	hooks := make(map[string]int, len(letters))
	for _, letter := range letters {
		hooks[letter] = countSingleTileMoves(bd, gd, ld, alph, letter)
	}

	return BoardMetrics{
		Openness:  ComputeOpenness(bd),
		LiveHooks: hooks,
	}
}

// BoardMetricsRequest mirrors GenerateMovesRequest's own board/premiumSquares/
// lexicon shape exactly (main-for-scrabble.go) - no rack, since this is a
// pure board-state question, plus Letters for which hook counts to compute
// (defaults to S and the blank if omitted).
type BoardMetricsRequest struct {
	Board          [][]string      `json:"board"`
	PremiumSquares []PremiumSquare `json:"premiumSquares,omitempty"`
	Lexicon        string          `json:"lexicon,omitempty"`
	Letters        []string        `json:"letters,omitempty"`
}

type BoardMetricsResponse struct {
	Openness float64        `json:"openness"`
	Hooks    map[string]int `json:"hooks"`
	Lexicon  string         `json:"lexicon"`
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

	// "?" (blank) is a real, meaningful entry here, not a placeholder - a
	// one-tile blank rack generates a move for every letter the blank
	// could stand in for at every live hook, so LiveHooks["?"] counts
	// total blank-fillable opportunities, not distinct positions (one
	// position offering 3 different letter choices counts as 3). That's a
	// deliberate first-pass simplification, not a bug - worth revisiting
	// once there's an actual value-deriving pipeline that cares about the
	// distinction.
	letters := req.Letters
	if len(letters) == 0 {
		letters = []string{"S", "?"}
	}

	metrics := ComputeBoardMetrics(bd, gd, ld, alph, letters)

	json.NewEncoder(w).Encode(BoardMetricsResponse{
		Openness: metrics.Openness,
		Hooks:    metrics.LiveHooks,
		Lexicon:  lexiconName,
	})
}
