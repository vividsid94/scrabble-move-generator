package main

// endgame_solver_explain.go implements /solve-endgame-explain - a straight
// copy of /solve-endgame-exact's own handler (endgame_solver_exact.go),
// with one thing added on top: a small, rule-based set of plain-English
// explanations for why each move in the solved line is the right one.
// Per explicit instruction, this is a COPY, not an edit of either existing
// endgame endpoint - /solve-endgame and /solve-endgame-exact are both
// untouched and still exist, kept stable, exactly like exact's own header
// comment already explains for why it didn't touch plain /solve-endgame.
//
// No AI/LLM text generation here, and no per-tile/per-word specifics either
// (explanations only ever say "this play"/"the best play", never naming
// actual words or squares) - just a fixed set of hardcoded template
// strings, each gated behind a cheap, deterministic check over data this
// solve already computes (IsOutplay, Leave, Spread, and the ranked
// Candidates table) rather than anything newly searched. The actual wording
// is deliberately meant to be iterated on before any voice work starts -
// see ExplanationCode, which gives each template a stable identifier
// independent of its current English text.
import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
)

// ExplanationCode is a stable identifier for one explanation template -
// kept separate from its English Text so the frontend (or a future voice
// layer) can key off the code without caring if the wording changes.
type ExplanationCode string

const (
	ExplainOutplayNow           ExplanationCode = "outplay_now"
	ExplainGoesOutInN           ExplanationCode = "goes_out_in_n"
	ExplainOnlyOutplay          ExplanationCode = "only_outplay"
	ExplainClearlyBest          ExplanationCode = "clearly_best"
	ExplainCloseCall            ExplanationCode = "close_call"
	ExplainTied                 ExplanationCode = "tied"
	ExplainScoreSpreadMismatch  ExplanationCode = "score_spread_mismatch"
	ExplainScoreMatchesSpread   ExplanationCode = "score_matches_spread"
	ExplainPassIsBest           ExplanationCode = "pass_is_best"
	ExplainBothOutplayDifferent ExplanationCode = "both_outplay_different_spread"
	ExplainPreventsOpponentOut  ExplanationCode = "prevents_opponent_outplay"
	ExplainKeepsBigTile         ExplanationCode = "keeps_big_tile"
)

// EndgameExplanation is one applicable template for one ply - a ply can
// have zero, one, or several (e.g. a move can both go out AND be the only
// play that does).
type EndgameExplanation struct {
	Code ExplanationCode `json:"code"`
	Text string          `json:"text"`
}

type SolveEndgameExplainResponse struct {
	SolveEndgameResponse
	// Explanations[i] is whatever applies to Moves[i] - same length as
	// Moves, nil/empty entries where nothing fired (a flat-greedy-tail ply
	// past endgameDepthBudget, or just a ply with nothing notable to say).
	Explanations [][]EndgameExplanation `json:"explanations,omitempty"`
}

type solveEndgameExplainResultLine struct {
	Type string `json:"type"`
	SolveEndgameExplainResponse
}

func solveEndgameExplainHandler(w http.ResponseWriter, r *http.Request) {
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

	tilesUnaccountedFor := 100 - tilesPlayed - len([]rune(req.MoverRack)) - len([]rune(req.OpponentRack))
	if tilesUnaccountedFor != 0 {
		http.Error(w, "Bag is not empty - the endgame solver only applies once every tile is on the board or in a rack", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

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

	// Same fast, pruned search every endgame endpoint shares - see
	// endgame_solver_exact.go's own comment for why this is reused verbatim
	// rather than reimplemented.
	line, finalMoverScore, finalOpponentScore := endgameSearch(
		ctx, gd, bd, req.MoverRack, req.OpponentRack, req.MoverScore, req.OpponentScore, 1, 0, endgameDepthBudget,
		-endgameAlphaBetaInfinity, endgameAlphaBetaInfinity, onProgress)
	incomplete := ctx.Err() != nil

	workingBd := bd.Copy()
	moves := make([]DetailedMove, 0, len(line))
	candidates := make([][]EndgameCandidateOption, 0, len(line))
	// rackLenBeforePly[i] is however many tiles the player on turn at ply i
	// actually holds immediately BEFORE their move there - real data from
	// this same replay, not inferred from the move itself (a move's own
	// tile count can't tell you how big the rack it was drawn from was).
	// Used by explainLine's "prevents the opponent from going out" check.
	rackLenBeforePly := make([]int, 0, len(line))
	curMoverRack, curOpponentRack := req.MoverRack, req.OpponentRack
	curMoverScore, curOpponentScore := req.MoverScore, req.OpponentScore
	curOnTurn := 1
	curDepthBudget := endgameDepthBudget

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
		if curOnTurn == 1 {
			rackLenBeforePly = append(rackLenBeforePly, len([]rune(curMoverRack)))
		} else {
			rackLenBeforePly = append(rackLenBeforePly, len([]rune(curOpponentRack)))
		}

		if entry.move == nil {
			moves = append(moves, *detailedMoveForPass())
			candidates = append(candidates, nil)
			advanceTurnState(nil)
			continue
		}

		detailed := toDetailedMove(entry.move, workingBd, alph)
		detailed.IsOutplay = entry.isOutplay
		moves = append(moves, *detailed)

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

	explanations := explainLine(moves, candidates, rackLenBeforePly)

	writeLine(solveEndgameExplainResultLine{
		Type: "result",
		SolveEndgameExplainResponse: SolveEndgameExplainResponse{
			SolveEndgameResponse: SolveEndgameResponse{
				Moves:      moves,
				Spread:     finalMoverScore - finalOpponentScore,
				Plies:      len(moves),
				Incomplete: incomplete,
				Lexicon:    lexiconName,
				Candidates: candidates,
			},
			Explanations: explanations,
		},
	})
}

// sameMoveShape compares two DetailedMoves by what actually identifies a
// play on the board (word/position/direction) - not by Score/Tiles/etc.,
// and not by Go struct equality, since two candidate lookups of "the same"
// move are never guaranteed to be the identical object.
func sameMoveShape(a, b DetailedMove) bool {
	return a.Word == b.Word && a.StartPosition == b.StartPosition && a.Direction == b.Direction
}

// explainLine computes 0+ explanation templates for every ply in the
// solved line - moves[i]/candidates[i] are the same parallel arrays
// solveEndgameExplainHandler already builds. Deliberately ply-at-a-time,
// each check standalone and independent of the others firing - a ply can
// (and often will) end up with more than one applicable explanation.
func explainLine(moves []DetailedMove, candidates [][]EndgameCandidateOption, rackLenBeforePly []int) [][]EndgameExplanation {
	result := make([][]EndgameExplanation, len(moves))

	for i, move := range moves {
		var out []EndgameExplanation
		cands := candidates[i]
		// isMaximizing: even plies are the original mover (maximizing
		// moverScore-opponentScore), odd are the opponent (minimizing it) -
		// same parity convention endgame_solver.go's own endgameSearch and
		// EndgamePauseBanner.jsx's isMoverPly already rely on.
		isMaximizing := i%2 == 0

		if move.IsOutplay {
			out = append(out, EndgameExplanation{ExplainOutplayNow, "This play empties the rack - the game ends right now."})
		} else {
			// How many of THIS SAME player's own future turns (same parity)
			// until the solved line shows them going out - only worth saying
			// for a short, concrete number; an outplay found many turns deep
			// in the flat-greedy tail isn't a real "plan," just wherever a
			// single-best-guess line happened to end up.
			for j := i + 2; j < len(moves) && j <= i+8; j += 2 {
				if moves[j].IsOutplay {
					n := (j-i)/2 + 1
					if n >= 2 && n <= 4 {
						out = append(out, EndgameExplanation{ExplainGoesOutInN, goesOutInNText(n)})
					}
					break
				}
			}
		}

		if len(cands) > 1 {
			chosen := cands[0]

			// Only play that actually goes out - chosen has no tiles left
			// over and nothing else shown does either.
			if chosen.Leave == "" {
				onlyOutplay := true
				sawAnotherOutplay := false
				for _, c := range cands[1:] {
					if c.Leave == "" {
						onlyOutplay = false
						sawAnotherOutplay = true
						break
					}
				}
				if onlyOutplay {
					out = append(out, EndgameExplanation{ExplainOnlyOutplay, "This is the only play here that actually goes out."})
				} else if sawAnotherOutplay {
					// Two different words both empty the rack, but the scores
					// (and so the final spreads) genuinely differ - otherwise
					// identical "both go out" plays would tie, not split.
					for _, c := range cands[1:] {
						if c.Leave == "" && c.Spread != chosen.Spread {
							out = append(out, EndgameExplanation{ExplainBothOutplayDifferent, "Both plays go out, but this one leaves the opponent worse off."})
							break
						}
					}
				}
			}

			// Margin over the next-best shown alternative, oriented so it's
			// always >= 0 regardless of which side is on turn (mirrors
			// EndgamePauseBanner.jsx's own valuationFor sign convention).
			rawDiff := cands[0].Spread - cands[1].Spread
			margin := rawDiff
			if !isMaximizing {
				margin = -rawDiff
			}
			switch {
			case margin == 0:
				out = append(out, EndgameExplanation{ExplainTied, "This is tied with another play here - either works equally well."})
			case margin >= 15:
				out = append(out, EndgameExplanation{ExplainClearlyBest, "This play is clearly the best choice - every alternative loses significant ground."})
			case margin > 0 && margin <= 5:
				out = append(out, EndgameExplanation{ExplainCloseCall, "This is a close call - only a small edge over the next-best option."})
			}

			// Highest RAW SCORE among shown candidates vs. the actual best
			// (by searched spread) - these can differ once the bag is empty,
			// since the biggest score right now isn't always the move that
			// wins the race to go out.
			best := cands[0]
			highestScoring := cands[0]
			for _, c := range cands[1:] {
				if c.Move.Score > highestScoring.Move.Score {
					highestScoring = c
				}
			}
			if !sameMoveShape(highestScoring.Move, best.Move) {
				out = append(out, EndgameExplanation{ExplainScoreSpreadMismatch, "The highest-scoring play isn't the best one here - this sets up a better finish."})
			} else {
				out = append(out, EndgameExplanation{ExplainScoreMatchesSpread, "This is both the highest-scoring play and the best one."})
			}
		}

		if move.Word == "Pass" {
			out = append(out, EndgameExplanation{ExplainPassIsBest, "Passing is actually the right move here."})
		}

		// Opponent's own best shown reply right after this move, if that
		// node was itself solved with real candidates - restates something
		// already in THIS SAME response (candidates[i+1][0]), not a new
		// search. Only said when it's a genuine near-miss (short opponent
		// rack - the real rack length at that ply, not an estimate), not
		// every ordinary ply, so it doesn't fire constantly.
		if i+1 < len(moves) && len(candidates[i+1]) > 0 && !moves[i+1].IsOutplay {
			opponentRackLen := rackLenBeforePly[i+1]
			if opponentRackLen > 0 && opponentRackLen <= 4 {
				out = append(out, EndgameExplanation{ExplainPreventsOpponentOut, "This play leaves the opponent with no way to go out next turn."})
			}
		}

		// Weak/optional signal (flagged as lower-confidence when this list
		// was first drafted) - checked last and only when nothing stronger
		// already fired for this ply, so a real reason never gets crowded
		// out by this weaker one.
		if len(out) == 0 && len(cands) > 0 {
			leave := cands[0].Leave
			if leave != "" && containsAnyBigTile(leave) {
				out = append(out, EndgameExplanation{ExplainKeepsBigTile, "This play keeps a big tile in reserve for later."})
			}
		}

		result[i] = out
	}

	return result
}

func goesOutInNText(n int) string {
	switch n {
	case 2:
		return "This sets up going out in two."
	case 3:
		return "This sets up going out in three."
	default:
		return "This sets up going out in four."
	}
}

func containsAnyBigTile(leave string) bool {
	for _, c := range leave {
		switch c {
		case 'Q', 'Z', 'X', 'J':
			return true
		}
	}
	return false
}
