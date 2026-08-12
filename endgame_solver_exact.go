package main

// endgame_solver_exact.go implements /solve-endgame-exact - a hybrid
// alternative to /solve-endgame (endgame_solver.go), built to fix a real
// correctness gap in that endpoint's own Candidates table WITHOUT touching
// its logic at all, since that endpoint is deployed and treated as stable.
//
// The problem: endgame_solver.go's endgameSearch uses alpha-beta pruning,
// which is guaranteed to always find the correct BEST move (that's its
// whole purpose), but does NOT guarantee exact VALUES for every other
// candidate it looks at along the way - a candidate searched after a
// stronger sibling has already tightened the alpha/beta window can get cut
// off early, returning only a bound ("no better than X") rather than the
// true resulting spread of playing it out optimally. That endpoint reports
// those bounds directly as each candidate's Spread, which is why its own
// Candidates table can drift from what an exact search (Quackle included)
// would show for anything other than the actual chosen move.
//
// The fix here doesn't touch the pruned search itself (still fast, still
// always finds the correct answer - endgameSearch/evalEndgameCandidate are
// called UNCHANGED, reused straight from endgame_solver.go). It adds a
// SEPARATE, bounded second pass, done exactly once per real-branching ply
// of the FINAL answer (at most endgameDepthBudget of them - see that
// constant, also in endgame_solver.go), that re-evaluates a capped number
// of that position's own top candidates with a fresh, full (-inf,+inf)
// window each - guaranteeing an exact value for every candidate actually
// shown, at a small, constant added cost (endgameExactValueCap extra full
// searches per ply) rather than the cost of disabling pruning across the
// WHOLE tree the pruned search explores and discards while finding the
// answer.

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
	"github.com/domino14/word-golib/kwg"
)

// endgameExactValueCap bounds how many of a position's ranked candidates
// get the expensive, fresh-window exact re-search - separate from
// endgameBranchWidth (endgame_solver.go, 100), which bounds how many get
// considered AT ALL for finding the correct best move in the first place.
// Only needs to cover however many rows the frontend actually displays
// (EndgamePauseBanner.jsx's MAX_VISIBLE_CHOICES, currently 10) plus a
// little headroom in case exact values reorder the pruned ranking's own
// top handful.
const endgameExactValueCap = 15

func solveEndgameExactHandler(w http.ResponseWriter, r *http.Request) {
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

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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

	// No poolSize field is sent - bag-empty is inferred server-side, same
	// as endgame_solver.go's own handler.
	tilesUnaccountedFor := 100 - tilesPlayed - len([]rune(req.MoverRack)) - len([]rune(req.OpponentRack))
	if tilesUnaccountedFor != 0 {
		http.Error(w, "Bag is not empty - the endgame solver only applies once every tile is on the board or in a rack", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Same streamed-progress shape as /solve-endgame - see that handler's
	// own comment for why (a real progress bar needs the server telling the
	// client as it goes, not a single buffered response).
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, canFlush := w.(http.Flusher)
	encoder := json.NewEncoder(w)
	writeLine := func(v any) {
		encoder.Encode(v)
		if canFlush {
			flusher.Flush()
		}
	}
	onProgress := func(current, total int) {
		writeLine(solveEndgameProgressLine{Type: "progress", Current: current, Total: total})
	}

	// The same fast, pruned search endgame_solver.go's own handler calls -
	// reused verbatim, not reimplemented, since it's already correct at
	// finding the right answer (alpha-beta's actual guarantee). Its own
	// entry.siblings field (set internally as a side effect) is simply
	// never read here - this handler computes its own Candidates tables
	// separately, below, from scratch.
	line, finalMoverScore, finalOpponentScore := endgameSearch(
		ctx, gd, bd, req.MoverRack, req.OpponentRack, req.MoverScore, req.OpponentScore, 1, 0, endgameDepthBudget,
		-endgameAlphaBetaInfinity, endgameAlphaBetaInfinity, onProgress)
	incomplete := ctx.Err() != nil

	// Replay the line onto a working board copy, same pattern
	// endgame_solver.go's own handler uses for toDetailedMove's
	// play-through-square rendering - but ALSO tracks moverRack/
	// opponentRack/moverScore/opponentScore/onTurn/depthBudget ply by ply,
	// so exactCandidateTable can be called with the exact position it
	// needs (the state immediately BEFORE that ply's move, matching what
	// the human is actually looking at when deciding it).
	workingBd := bd.Copy()
	moves := make([]DetailedMove, 0, len(line))
	candidates := make([][]EndgameCandidateOption, 0, len(line))
	curMoverRack, curOpponentRack := req.MoverRack, req.OpponentRack
	curMoverScore, curOpponentScore := req.MoverScore, req.OpponentScore
	curOnTurn := 1
	curDepthBudget := endgameDepthBudget

	// advanceTurnState is the SAME bookkeeping evalEndgameCandidate (and
	// endgameSearch's own pass branch) already do internally - duplicated
	// here rather than reused because those two only expose it as a side
	// effect of building a *line*, not as something this replay loop can
	// call standalone with just the fields it needs.
	advanceTurnState := func(detailed *DetailedMove) {
		if detailed != nil {
			usedStr := newlyUsedLettersExact(detailed)
			if curOnTurn == 1 {
				curMoverRack = removeRackFromPool(curMoverRack, usedStr)
				curMoverScore += detailed.Score
			} else {
				curOpponentRack = removeRackFromPool(curOpponentRack, usedStr)
				curOpponentScore += detailed.Score
			}
		}
		curOnTurn = 3 - curOnTurn
		curDepthBudget--
		if curDepthBudget < 0 {
			curDepthBudget = 0
		}
	}

	for i, entry := range line {
		if entry.move == nil {
			moves = append(moves, *detailedMoveForPass())
			candidates = append(candidates, nil)
			advanceTurnState(nil)
			continue
		}

		detailed := toDetailedMove(entry.move, workingBd, alph)
		detailed.IsOutplay = entry.isOutplay
		moves = append(moves, *detailed)

		// Only the first endgameDepthBudget plies are real branching points
		// (endgame_solver.go's own doc comment) - past that, the line is
		// flat greedy (a single candidate, nothing worth an exact table
		// for), so this stays nil there, matching what the frontend
		// already expects (EndgamePauseBanner.jsx only renders a table
		// when candidatesPerPly[lineIndex] has more than one entry).
		if i < endgameDepthBudget {
			candidates = append(candidates, exactCandidateTable(
				ctx, gd, workingBd, curMoverRack, curOpponentRack, curMoverScore, curOpponentScore, curOnTurn, curDepthBudget, entry.move,
			))
		} else {
			candidates = append(candidates, nil)
		}

		advanceTurnState(detailed)

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
			Lexicon:    lexiconName,
			Candidates: candidates,
		},
	})
}

// newlyUsedLettersExact extracts the letters (blanks as "?") a move newly
// placed, in IsNew tile order - a small local copy of the same snippet
// endgame_solver.go's own rankEndgameCandidates/evalEndgameCandidate each
// already build inline, duplicated here (not extracted into a shared
// helper) so this file makes zero changes to endgame_solver.go.
func newlyUsedLettersExact(detailed *DetailedMove) string {
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
	return strings.Join(used, "")
}

// exactCandidateTable computes a Quackle-style table of ranked alternatives
// at ONE specific real position - every candidate returned here gets its
// own fresh, full-window (-inf,+inf) search (evalEndgameCandidate, reused
// unchanged from endgame_solver.go), so every Spread is genuinely exact,
// never a pruned bound. Only called from solveEndgameExactHandler's own
// replay loop, AFTER the main pruned search above has already found the
// real answer, and only for the few positions actually ALONG that answer's
// own line - not for every node the pruned search happened to visit and
// discard while finding it. That scoping is what keeps this affordable: at
// most endgameDepthBudget positions, endgameExactValueCap extra full
// searches each, regardless of how wide endgameBranchWidth is.
//
// chosenMove is the move actually played at this ply in the final line -
// rankEndgameCandidates' own top-endgameExactValueCap slice is the natural
// pool to re-search, but the "extra outplay candidate" it can append past
// its own top cut (see that function's own comment) means the actual
// winner can occasionally fall outside that slice; explicitly unioning it
// in guarantees the table's own row 0 (EndgamePauseBanner.jsx's own
// isChosen convention) always matches the move that was actually played -
// its exact spread is provably >= every other candidate's here (the
// pruned search already proved it's the true best move), so it will
// always sort to row 0 on its own merits once every row's own value is
// exact, this union just guarantees it's IN the list to begin with.
func exactCandidateTable(ctx context.Context, gd *kwg.KWG, bd *board.GameBoard, moverRack, opponentRack string, moverScore, opponentScore, onTurn, depthBudget int, chosenMove *macondomove.Move) []EndgameCandidateOption {
	currentRack := moverRack
	otherRack := opponentRack
	if onTurn == 2 {
		currentRack = opponentRack
		otherRack = moverRack
	}
	ranked := rankEndgameCandidates(gd, bd, currentRack, otherRack)

	count := endgameExactValueCap
	if count > len(ranked) {
		count = len(ranked)
	}
	toRefine := ranked[:count]

	haveChosen := false
	for _, c := range toRefine {
		if c.move == chosenMove {
			haveChosen = true
			break
		}
	}
	if !haveChosen {
		for _, c := range ranked[count:] {
			if c.move == chosenMove {
				toRefine = append(append([]scoredCandidate{}, toRefine...), c)
				break
			}
		}
	}

	result := make([]EndgameCandidateOption, 0, len(toRefine))
	for _, cand := range toRefine {
		_, fs1, fs2 := evalEndgameCandidate(
			ctx, gd, bd, moverRack, opponentRack, moverScore, opponentScore, onTurn, depthBudget,
			-endgameAlphaBetaInfinity, endgameAlphaBetaInfinity, cand,
		)
		result = append(result, EndgameCandidateOption{
			Move:   *cand.detailed,
			Leave:  cand.leave,
			Spread: fs1 - fs2,
		})
	}

	isMaximizing := onTurn == 1
	sort.SliceStable(result, func(i, j int) bool {
		if isMaximizing {
			return result[i].Spread > result[j].Spread
		}
		return result[i].Spread < result[j].Spread
	})
	return result
}
