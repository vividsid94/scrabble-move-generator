package main

// endgame_solver.go implements /solve-endgame: a Quackle-style bounded
// greedy-with-recursion endgame solver, used once the tile bag is empty and
// both racks are therefore fully known. Deliberately NOT an exhaustive
// search - an earlier attempt wired up Macondo's own endgame/negamax
// solver directly, which turned out to have a shared global mutable
// transposition table, an unresolved upstream panic (packed move
// reconstruction failing against the wrong rack), and effectively
// unbounded worst-case cost - a bad fit for this small service. That
// attempt was fully reverted; nothing here touches Macondo's negamax
// package.
//
// Instead: generate the top-K legal candidates for whoever's on turn and
// recurse on each (real branching, plain minimax, no alpha-beta needed at
// this scale) for a small fixed number of plies; beyond that depth, fall
// back to a single best-move-only line (no branching) straight to game
// over. Cost is bounded by depth x branch width regardless of how long the
// underlying endgame actually runs - matching Quackle's own "a few
// seconds, not perfect but good enough" endgame solver rather than an
// exhaustive one.
//
// Every primitive used here (movegen.NewGordonGenerator, board.Copy/
// PlayMove/UpdateCrossSetsForMove, scoredCandidate/getLeaveValue/
// removeRackFromPool/rackPointValue, the "emptied"/"sixPasses" end-game
// scoring rules) is already exercised elsewhere in this service
// (bulkMoveGenHandler, simulateOneGame) rather than being new, unproven
// surface.

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	macondomove "github.com/domino14/macondo/move"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/tilemapping"
)

// endgameBranchWidth caps how many candidates get real recursive
// consideration at each of endgameDepthBudget plies - total explored nodes
// is roughly endgameBranchWidth^endgameDepthBudget (50^3 = 125000), not
// exponential in however long the actual endgame runs. Past that depth,
// only the single best-scoring move is considered (flat greedy) for the
// rest of the line, which is what keeps total cost bounded no matter how
// many tiles are left on either rack. Set generously on purpose - a real
// endgame position (small racks, mostly-full board) rarely has anywhere
// near this many legal moves, so in practice this is "consider everything"
// rather than an actual truncation; it only starts clipping in unusually
// open positions, trading some speed for not silently discarding the
// actual best move the way a tight cap did.
const endgameBranchWidth = 50

// endgameDepthBudget is how many plies get real branching (mover, their
// reply, mover again) before falling back to flat greedy for the remainder
// of the line.
const endgameDepthBudget = 3

type SolveEndgameRequest struct {
	Board         [][]string `json:"board"`
	MoverRack     string     `json:"moverRack"`
	OpponentRack  string     `json:"opponentRack"`
	MoverScore    int        `json:"moverScore"`
	OpponentScore int        `json:"opponentScore"`
}

type SolveEndgameResponse struct {
	Moves      []DetailedMove `json:"moves"`
	Spread     int            `json:"spread"`
	Plies      int            `json:"plies"`
	Incomplete bool           `json:"incomplete"`
}

// The two line shapes streamed on /solve-endgame's response - see
// solveEndgameHandler's own comment on why this is a stream, not a single
// JSON blob. Embedding SolveEndgameResponse in the result line keeps its
// fields exactly what the frontend already expected from the old
// single-response shape, just with "type":"result" alongside them.
type solveEndgameProgressLine struct {
	Type    string `json:"type"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
}

type solveEndgameResultLine struct {
	Type string `json:"type"`
	SolveEndgameResponse
}

// endgameLineEntry is one turn in a solved line - move is nil for a pass.
// player is 1 for the mover (the player this solver is finding the best
// move for) or 2 for the opponent, regardless of the app's own global
// player numbering - the request itself is already reoriented to
// mover/opponent, so the solver never needs to know which "real" player is
// which.
type endgameLineEntry struct {
	player int
	move   *macondomove.Move
}

func solveEndgameHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SolveEndgameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.MoverRack == "" || req.OpponentRack == "" {
		// An empty opponent rack means they already went out - the game is
		// already over, not an endgame left to solve.
		http.Error(w, "Both racks are required", http.StatusBadRequest)
		return
	}
	if len([]rune(req.MoverRack)) > 7 || len([]rune(req.OpponentRack)) > 7 {
		http.Error(w, "Racks cannot exceed 7 tiles", http.StatusBadRequest)
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

	bd := board.MakeBoard(board.CrosswordGameBoard)
	tilesPlayed := 0
	for row := 0; row < 15; row++ {
		for col := 0; col < 15; col++ {
			tile := req.Board[row][col]
			if tile == "" {
				continue
			}
			if ml, err := alph.Val(tile); err == nil {
				bd.SetLetter(row, col, ml)
				tilesPlayed++
			}
		}
	}
	bd.TestSetTilesPlayed(tilesPlayed)
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	// No poolSize field is sent - bag-empty is inferred server-side from the
	// known 100-tile English set (the only distribution this service loads)
	// minus what's on the board and in both racks. Blanks count as 1 tile
	// each via rune length, same as everywhere else in this file.
	tilesUnaccountedFor := 100 - tilesPlayed - len([]rune(req.MoverRack)) - len([]rune(req.OpponentRack))
	if tilesUnaccountedFor != 0 {
		http.Error(w, "Bag is not empty - the endgame solver only applies once every tile is on the board or in a rack", http.StatusBadRequest)
		return
	}

	// Bounded by design (depth x branch width), not by racing this clock -
	// this is a safety net, not the primary mechanism. A blown deadline
	// mid-search just forces flat greedy for whatever's left rather than
	// erroring out. 30s (not 10s) gives real headroom to tell "the search
	// is genuinely still running and will finish" apart from "it's actually
	// stuck" while testing/tuning endgameBranchWidth - the bounded design
	// means a healthy solve essentially never needs this long, so raising
	// it doesn't change typical latency, just the safety margin.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Streamed as newline-delimited JSON rather than one final blob -
	// {"type":"progress",...} lines as each top-level candidate (the move
	// actually being solved for) finishes, then one {"type":"result",...}
	// line with the real SolveEndgameResponse fields. This is what makes a
	// REAL progress bar possible client-side - the frontend has no way to
	// know how far along an opaque server-side search is without the
	// server actually telling it as it goes, and a single buffered
	// request/response can't do that. Root-level progress specifically
	// (not per-node) - each of the root's endgameBranchWidth candidates
	// triggers a roughly similarly-sized subtree, so "candidate N of M
	// explored" is a reasonable, cheap-to-compute proxy for overall
	// progress without needing to know the exact total node count up
	// front.
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, canFlush := w.(http.Flusher)
	encoder := json.NewEncoder(w)
	writeLine := func(v any) {
		encoder.Encode(v) // Encode already appends its own trailing newline
		if canFlush {
			flusher.Flush()
		}
	}
	onProgress := func(current, total int) {
		writeLine(solveEndgameProgressLine{Type: "progress", Current: current, Total: total})
	}

	line, finalMoverScore, finalOpponentScore := endgameSearch(
		ctx, bd, req.MoverRack, req.OpponentRack, req.MoverScore, req.OpponentScore, 1, 0, endgameDepthBudget, onProgress)
	incomplete := ctx.Err() != nil

	// Replay the line onto a working board copy to convert each entry to
	// the client-facing DetailedMove shape - toDetailedMove needs the board
	// as it stood immediately BEFORE each move for correct play-through-
	// square rendering, same pattern bulkMoveGenHandler's own replay uses.
	workingBd := bd.Copy()
	moves := make([]DetailedMove, 0, len(line))
	for _, entry := range line {
		if entry.move == nil {
			moves = append(moves, *detailedMoveForPass())
			continue
		}
		detailed := toDetailedMove(entry.move, workingBd, alph)
		moves = append(moves, *detailed)
		workingBd.PlayMove(entry.move)
		cross_set.UpdateCrossSetsForMove(workingBd, entry.move, gd, ld)
	}

	writeLine(solveEndgameResultLine{
		Type: "result",
		SolveEndgameResponse: SolveEndgameResponse{
			Moves:      moves,
			Spread:     finalMoverScore - finalOpponentScore,
			Plies:      len(moves),
			Incomplete: incomplete,
		},
	})
}

func detailedMoveForPass() *DetailedMove {
	return &DetailedMove{Word: "Pass", Direction: "pass", Tiles: []MoveTile{}}
}

// rankEndgameCandidates scores every legal word play for currentRack on bd
// and returns them sorted best-first, for picking the top endgameBranchWidth
// to actually recurse on. Deliberately ranked by raw score alone (NOT
// score+leaveValue the way pickBestCandidate/simulateOneGame rank mid-game
// candidates - leaveValue estimates how good your remaining tiles are for
// FUTURE DRAWS FROM THE BAG, which is meaningless once the bag is empty),
// PLUS an exact outplay bonus: a move that empties currentRack ends the
// game immediately and is worth its score plus 2x otherRack's value (same
// "emptied" rule applyEmptiedAdjustment uses below) - not a fuzzy estimate,
// since both racks are fully known in an endgame. This bonus alone isn't a
// hard guarantee, though - when otherRack (the bonus's basis) happens to be
// small, a low-immediate-score outplay can still rank below
// endgameBranchWidth other, merely higher-scoring candidates; endgameSearch
// itself adds a second, unconditional guarantee on top of this ranking
// (see its own comment) for exactly that reason - together they're what
// stops a real game-ending reply from silently never reaching the
// recursion at all. No exchange candidates either - exchanging is never
// legal once the bag is empty.
func rankEndgameCandidates(bd *board.GameBoard, currentRack, otherRack string) []scoredCandidate {
	rack := tilemapping.RackFromString(currentRack, alph)
	generator := movegen.NewGordonGenerator(gd, bd, ld)
	rawMoves := generator.GenAll(rack, false)

	otherRackValue := rackPointValue(otherRack)

	candidates := make([]scoredCandidate, 0, len(rawMoves))
	for _, m := range rawMoves {
		if !strings.Contains(m.String(), "play word:") {
			continue
		}
		detailed := toDetailedMove(m, bd, alph)

		var used []string
		for _, t := range detailed.Tiles {
			if t.IsNew {
				if t.IsBlank {
					used = append(used, "?")
				} else {
					used = append(used, t.Letter)
				}
			}
		}
		// leave is still computed (cheap, and part of the shared
		// scoredCandidate shape) but deliberately left out of total beyond
		// the outplay check below - see this function's own comment for why
		// leaveValue doesn't apply once the bag is empty.
		leave := sortLeaveString(removeRackFromPool(currentRack, strings.Join(used, "")))
		total := float64(detailed.Score)
		if leave == "" {
			total += float64(otherRackValue) * 2
		}

		candidates = append(candidates, scoredCandidate{move: m, detailed: detailed, leave: leave, total: total})
	}

	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].total > candidates[j].total })
	return candidates
}

// endgameSearch returns the sequence of moves played from this position to
// game over, plus final scores for both sides. onTurn is 1 (mover) or 2
// (opponent); player 1 maximizes moverScore-opponentScore at every
// branching decision, player 2 minimizes it - a plain minimax, no
// alpha-beta pruning, since endgameDepthBudget/endgameBranchWidth already
// keep the tree small enough not to need it.
//
// depthBudget plies get real branching (top endgameBranchWidth candidates,
// recurse on each); once it reaches 0, only the single best-scoring
// candidate is considered from here to the end of the game - a flat greedy
// tail that keeps total cost bounded no matter how many turns remain.
// Entering this function, both racks are always non-empty - the only way a
// rack becomes empty is via the move just played in the PARENT call, which
// is checked immediately (see below) rather than by recursing into an
// already-ended position.
//
// onProgress, if non-nil, is called once per candidate explored at THIS
// call only - every recursive call this function makes always passes nil
// onward, regardless of depth, so in practice only the root call (the one
// solveEndgameHandler makes directly) ever reports progress. Reporting from
// every nested node would mean thousands of tiny, expensive-to-flush writes
// for no real benefit; each of the root's candidates triggers a roughly
// similarly-sized subtree, so root-level "candidate N of M" is already a
// reasonable proxy for overall progress.
func endgameSearch(ctx context.Context, bd *board.GameBoard, moverRack, opponentRack string, moverScore, opponentScore, onTurn, consecutiveScoreless, depthBudget int, onProgress func(current, total int)) ([]endgameLineEntry, int, int) {
	// A blown deadline forces flat greedy for the rest of the line rather
	// than aborting outright - always a complete, valid answer, just
	// possibly lower-quality past this point. See solveEndgameHandler's
	// Incomplete flag, which this drives.
	if ctx.Err() != nil {
		depthBudget = 0
	}

	if consecutiveScoreless >= 6 {
		fs1, fs2 := applySixPassesAdjustment(moverScore, opponentScore, moverRack, opponentRack)
		return nil, fs1, fs2
	}

	currentRack := moverRack
	otherRack := opponentRack
	if onTurn == 2 {
		currentRack = opponentRack
		otherRack = moverRack
	}
	candidates := rankEndgameCandidates(bd, currentRack, otherRack)

	branchWidth := 1
	if depthBudget > 0 {
		branchWidth = endgameBranchWidth
	}
	if branchWidth > len(candidates) {
		branchWidth = len(candidates)
	}

	if branchWidth == 0 {
		// No legal play - pass. Consumes a ply of depth budget just like a
		// real move would, so recursion depth stays bounded either way.
		entry := endgameLineEntry{player: onTurn, move: nil}
		newConsecutive := consecutiveScoreless + 1
		if newConsecutive >= 6 {
			fs1, fs2 := applySixPassesAdjustment(moverScore, opponentScore, moverRack, opponentRack)
			return []endgameLineEntry{entry}, fs1, fs2
		}
		childDepth := depthBudget - 1
		if childDepth < 0 {
			childDepth = 0
		}
		restLine, fs1, fs2 := endgameSearch(ctx, bd, moverRack, opponentRack, moverScore, opponentScore, 3-onTurn, newConsecutive, childDepth, nil)
		return append([]endgameLineEntry{entry}, restLine...), fs1, fs2
	}

	// toExplore is the score-ranked top branchWidth slice, PLUS any outplay
	// candidate (empties currentRack, ending the game right now) that
	// didn't already make that cut. The additive outplay bonus in
	// rankEndgameCandidates helps but isn't a hard guarantee - when the
	// OTHER player's remaining rack (the bonus's basis) happens to be
	// small, a real game-ending reply can still rank below branchWidth
	// other, merely higher-scoring options and never reach the recursion
	// at all. Copied into a fresh slice rather than appended onto
	// candidates[:branchWidth] directly, which would alias the same
	// backing array as the candidates[branchWidth:] loop below and
	// silently corrupt it mid-iteration.
	toExplore := make([]scoredCandidate, 0, branchWidth+1)
	toExplore = append(toExplore, candidates[:branchWidth]...)
	for i := branchWidth; i < len(candidates); i++ {
		if candidates[i].leave == "" {
			toExplore = append(toExplore, candidates[i])
		}
	}

	isMaximizing := onTurn == 1
	var bestLine []endgameLineEntry
	var bestMoverScore, bestOpponentScore int
	haveBest := false

	for i, cand := range toExplore {
		childBd := bd.Copy()
		childBd.PlayMove(cand.move)
		cross_set.UpdateCrossSetsForMove(childBd, cand.move, gd, ld)

		var used []string
		for _, t := range cand.detailed.Tiles {
			if t.IsNew {
				if t.IsBlank {
					used = append(used, "?")
				} else {
					used = append(used, t.Letter)
				}
			}
		}
		usedStr := strings.Join(used, "")

		newMoverRack, newOpponentRack := moverRack, opponentRack
		newMoverScore, newOpponentScore := moverScore, opponentScore
		if onTurn == 1 {
			newMoverRack = removeRackFromPool(moverRack, usedStr)
			newMoverScore = moverScore + cand.detailed.Score
		} else {
			newOpponentRack = removeRackFromPool(opponentRack, usedStr)
			newOpponentScore = opponentScore + cand.detailed.Score
		}

		entry := endgameLineEntry{player: onTurn, move: cand.move}

		var line []endgameLineEntry
		var fs1, fs2 int
		if newMoverRack == "" || newOpponentRack == "" {
			// Whoever just moved went out - game over immediately, no more
			// recursion needed.
			fs1, fs2 = applyEmptiedAdjustment(newMoverScore, newOpponentScore, newMoverRack, newOpponentRack)
			line = []endgameLineEntry{entry}
		} else {
			childDepth := depthBudget - 1
			if childDepth < 0 {
				childDepth = 0
			}
			restLine, restFs1, restFs2 := endgameSearch(
				ctx, childBd, newMoverRack, newOpponentRack, newMoverScore, newOpponentScore, 3-onTurn, 0, childDepth, nil)
			line = append([]endgameLineEntry{entry}, restLine...)
			fs1, fs2 = restFs1, restFs2
		}

		spread := fs1 - fs2
		if !haveBest || (isMaximizing && spread > bestMoverScore-bestOpponentScore) || (!isMaximizing && spread < bestMoverScore-bestOpponentScore) {
			haveBest = true
			bestLine = line
			bestMoverScore, bestOpponentScore = fs1, fs2
		}

		// Reported AFTER this candidate's whole subtree is actually
		// evaluated, not before it starts - firing "N of Total" up front
		// would hit 100% the instant the LAST candidate begins exploring,
		// well before that exploration (a full nested search, not
		// instantaneous) is actually done, misleadingly showing a
		// "finished" bar while the request is still genuinely running.
		if onProgress != nil {
			onProgress(i+1, len(toExplore))
		}
	}

	return bestLine, bestMoverScore, bestOpponentScore
}

// applyEmptiedAdjustment mirrors simulateOneGame's own "emptied" ending -
// the player who went out gets 2x the opponent's remaining rack value added
// to their score, same convention as sandboxGameFunctions.js's
// computeFinalScores on the frontend.
func applyEmptiedAdjustment(moverScore, opponentScore int, moverRack, opponentRack string) (int, int) {
	if moverRack == "" {
		return moverScore + rackPointValue(opponentRack)*2, opponentScore
	}
	return moverScore, opponentScore + rackPointValue(moverRack)*2
}

// applySixPassesAdjustment mirrors simulateOneGame's own "sixPasses" ending
// - each side loses their own remaining rack value.
func applySixPassesAdjustment(moverScore, opponentScore int, moverRack, opponentRack string) (int, int) {
	return moverScore - rackPointValue(moverRack), opponentScore - rackPointValue(opponentRack)
}
