package main

// Full-game simulator for any matchup of "static" bots - ones that pick a
// fixed rank from a score+leaveValue-ranked candidate list every turn, with
// no per-move opponent simulation (that's Tess, which stays client-side).
// Theo is just rank 1; "Nth static" is any other rank 1-15 the app's UI
// offers. Additive to main-for-scrabble.go: reuses its package-level
// gd/alph/ld, its DetailedMove/MoveTile types, and its
// toDetailedMove/drawRandomTiles/removeRackFromPool helpers rather than
// duplicating them, but does not modify any existing handler.
//
// Ranking is by score + leaveValue, where leaveValue comes from a static,
// context-free lookup table keyed by the sorted leave string (leaves.json,
// embedded below). That table - not whatever internal equity Macondo's own
// GenAll ordering might use - is what decides every move here, so rank 1
// matches the app's existing Theo bot rather than some other
// Macondo-internal notion of "best."
//
// Note: getTopMoves.js (the JS equivalent of this endpoint) discards the Go
// service's own Move.Leave() string and recomputes the leave itself from the
// known rack minus used tiles instead of trusting that formatting. This file
// does the same for the same reason - safer to derive it from data we
// already trust (the rack string, the DetailedMove tiles) than from a
// Macondo-formatted string whose blank/casing convention hasn't been
// independently verified.

import (
	_ "embed"
	"encoding/json"
	"math/rand"
	"net/http"
	"sort"
	"strings"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	macondomove "github.com/domino14/macondo/move"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/tilemapping"
)

//go:embed leaves.json
var leavesJSONData []byte

var leaveValues map[string]float64

func init() {
	if err := json.Unmarshal(leavesJSONData, &leaveValues); err != nil {
		// Fail loudly at startup rather than silently scoring every leave as
		// 0 - a botched leave table would make every simulated game's move
		// choices wrong in a way that's easy to miss otherwise.
		panic("simulate.go: failed to parse embedded leaves.json: " + err.Error())
	}
}

const standardTilePool = "AAAAAAAAABBCCDDDDEEEEEEEEEEEEFFGGGHHIIIIIIIIIJKLLLLMMNNNNNNOOOOOOOOPPQRRRRRRSSSSTTTTTTUUUUVVWWXYYZ??"

// Standard Scrabble letter values, used only for the end-of-game remaining-
// tile score adjustment - same table as sandboxGameFunctions.js's
// TILE_VALUES on the JS side.
var letterPointValues = map[rune]int{
	'A': 1, 'E': 1, 'I': 1, 'O': 1, 'U': 1, 'L': 1, 'N': 1, 'S': 1, 'T': 1, 'R': 1,
	'D': 2, 'G': 2,
	'B': 3, 'C': 3, 'M': 3, 'P': 3,
	'F': 4, 'H': 4, 'V': 4, 'W': 4, 'Y': 4,
	'K': 5,
	'J': 8, 'X': 8,
	'Q': 10, 'Z': 10,
}

func rackPointValue(rack string) int {
	total := 0
	for _, c := range rack {
		if c == '?' {
			continue
		}
		total += letterPointValues[c]
	}
	return total
}

func getLeaveValue(leave string) float64 {
	if v, ok := leaveValues[leave]; ok {
		return v
	}
	return 0
}

// sortLeaveString mirrors the JS app's `rack.sort().join('')` convention:
// plain code-point ordering, so '?' (blank) sorts before letters, matching
// leaves.json's key format (e.g. "?A").
func sortLeaveString(s string) string {
	runes := []rune(s)
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	return string(runes)
}

// allExchangeCandidates enumerates every non-empty subset of rack (up to
// 2^7-1 for a full rack) as its own ranked candidate, valued by the leave it
// would leave behind - mirrors sandboxBotFunctions.js's
// generateExchangeCombinations + calculateExchangeLeave, just done as a
// bitmask scan instead of recursive backtracking. Unlike the single-best
// version this replaced, this returns every candidate so "Nth static" has a
// full combined (word plays + exchanges) list to rank into.
func allExchangeCandidates(rack string) []scoredCandidate {
	tiles := []rune(rack)
	n := len(tiles)
	if n == 0 {
		return nil
	}

	candidates := make([]scoredCandidate, 0, (1<<uint(n))-1)
	for mask := 1; mask < (1 << uint(n)); mask++ {
		var toExchange []rune
		var remaining []rune
		for i := 0; i < n; i++ {
			if mask&(1<<uint(i)) != 0 {
				toExchange = append(toExchange, tiles[i])
			} else {
				remaining = append(remaining, tiles[i])
			}
		}
		leave := sortLeaveString(string(remaining))
		candidates = append(candidates, scoredCandidate{
			isExchange:    true,
			exchangeTiles: string(toExchange),
			leave:         leave,
			total:         getLeaveValue(leave),
		})
	}
	return candidates
}

type SimTurn struct {
	Player         int              `json:"player"` // 1 or 2
	Type           string           `json:"type"`    // "play" | "exchange" | "pass"
	Word           string           `json:"word,omitempty"`
	Score          int              `json:"score"`
	Position       string           `json:"position,omitempty"`
	Direction      string           `json:"direction,omitempty"`
	Tiles          []MoveTile       `json:"tiles,omitempty"`
	RackBefore     string           `json:"rackBefore"`
	TilesExchanged string           `json:"tilesExchanged,omitempty"`
	RunningTotal   int              `json:"runningTotal"`
}

type SimGameResult struct {
	Turns            []SimTurn `json:"turns"`
	Player1Score     int       `json:"player1Score"`
	Player2Score     int       `json:"player2Score"`
	Winner           int       `json:"winner"` // 0 = tie, 1, or 2
	EndReason        string    `json:"endReason"`
	Player1FinalRack string    `json:"player1FinalRack"`
	Player2FinalRack string    `json:"player2FinalRack"`
	FinalPool        string    `json:"finalPool"`
}

type SimulateSeriesRequest struct {
	Games       int `json:"games,omitempty"`
	Player1Rank int `json:"player1Rank,omitempty"` // 1 = Theo; N = "Nth static"
	Player2Rank int `json:"player2Rank,omitempty"`
}

type SimulateSeriesResponse struct {
	Games []SimGameResult `json:"games"`
}

// scoredCandidate is one ranked option for a turn - either a word play
// (isExchange false, move/detailed set) or an exchange (isExchange true,
// exchangeTiles set). Combining both kinds into one ranked list is what
// makes "Nth static" well-defined: it's the Nth-best option overall,
// matching how the original JS Intermediate bot picked from a single
// word-plays+exchanges list sorted by totalValue.
type scoredCandidate struct {
	isExchange    bool
	move          *macondomove.Move
	detailed      *DetailedMove
	exchangeTiles string
	leave         string
	total         float64
}

// simulateOneGame plays one complete game start to finish between two
// "static" bots (player1Rank/player2Rank - Theo is rank 1) and returns its
// full turn-by-turn history plus final scoring. Every move decision builds
// the full candidate list (word plays + exchanges) and ranks it by
// score+leaveValue itself (rather than trusting whatever order GenAll
// returns moves in), so rank 1 matches the app's Theo definition by
// construction, not by coincidence, and rank N just indexes further into
// the same list.
func simulateOneGame(player1Rank, player2Rank int) SimGameResult {
	bd := board.MakeBoard(board.CrosswordGameBoard)
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	pool := standardTilePool
	rack1 := drawRandomTiles(pool, 7)
	pool = removeRackFromPool(pool, rack1)
	rack2 := drawRandomTiles(pool, 7)
	pool = removeRackFromPool(pool, rack2)

	score1, score2 := 0, 0
	consecutiveScoreless := 0
	currentPlayer := 1
	if rand.Float64() < 0.5 {
		currentPlayer = 2
	}

	var turns []SimTurn
	endReason := ""

	for {
		currentRack := rack1
		if currentPlayer == 2 {
			currentRack = rack2
		}

		rack := tilemapping.RackFromString(currentRack, alph)
		generator := movegen.NewGordonGenerator(gd, bd, ld)
		rawMoves := generator.GenAll(rack, false)

		// Word-play candidates first, exchange candidates after - order
		// matters for the stable sort below (see comment there).
		var candidates []scoredCandidate
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
			leave := sortLeaveString(removeRackFromPool(currentRack, strings.Join(used, "")))
			total := float64(detailed.Score) + getLeaveValue(leave)

			candidates = append(candidates, scoredCandidate{move: m, detailed: detailed, leave: leave, total: total})
		}

		canExchange := len(pool) >= 7
		if canExchange {
			candidates = append(candidates, allExchangeCandidates(currentRack)...)
		}

		// Stable sort: candidates were appended word-plays-first, so on an
		// exact tie in total, a word play keeps its position ahead of a
		// tied exchange, and two tied word plays keep GenAll's original
		// relative order - the same tie-breaking a single-pass max-scan
		// would give rank 1, just generalized to rank N.
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].total > candidates[j].total })

		rank := player1Rank
		if currentPlayer == 2 {
			rank = player2Rank
		}
		idx := rank - 1
		if idx < 0 || idx >= len(candidates) {
			// Requested rank exceeds how many legal options exist this turn
			// (common late-game) - fall back to the best available, matching
			// the original client-side Intermediate bot's same fallback.
			idx = 0
		}

		var chosen *scoredCandidate
		if len(candidates) > 0 {
			chosen = &candidates[idx]
		}

		currentScoreBefore := score1
		if currentPlayer == 2 {
			currentScoreBefore = score2
		}

		turn := SimTurn{Player: currentPlayer, RackBefore: currentRack}
		var newRack string

		switch {
		case chosen == nil:
			turn.Type = "pass"
			turn.Score = 0
			turn.RunningTotal = currentScoreBefore
			consecutiveScoreless++
			newRack = currentRack

		case chosen.isExchange:
			turn.Type = "exchange"
			turn.TilesExchanged = chosen.exchangeTiles
			turn.Score = 0
			turn.RunningTotal = currentScoreBefore
			consecutiveScoreless++

			rackAfterRemoval := removeRackFromPool(currentRack, chosen.exchangeTiles)
			needed := 7 - len([]rune(rackAfterRemoval))
			drawn := drawRandomTiles(pool, needed)
			pool = removeRackFromPool(pool, drawn)
			pool = pool + chosen.exchangeTiles
			newRack = sortLeaveString(rackAfterRemoval + drawn)

		default: // word play
			turn.Type = "play"
			turn.Word = chosen.detailed.Word
			turn.Score = chosen.detailed.Score
			turn.Position = chosen.detailed.StartPosition
			turn.Direction = chosen.detailed.Direction
			turn.Tiles = chosen.detailed.Tiles
			turn.RunningTotal = currentScoreBefore + chosen.detailed.Score
			consecutiveScoreless = 0

			bd.PlayMove(chosen.move)
			cross_set.UpdateCrossSetsForMove(bd, chosen.move, gd, ld)

			var used []string
			for _, t := range chosen.detailed.Tiles {
				if t.IsNew {
					if t.IsBlank {
						used = append(used, "?")
					} else {
						used = append(used, t.Letter)
					}
				}
			}
			rackAfterRemoval := removeRackFromPool(currentRack, strings.Join(used, ""))
			needed := 7 - len([]rune(rackAfterRemoval))
			drawn := drawRandomTiles(pool, needed)
			pool = removeRackFromPool(pool, drawn)
			newRack = sortLeaveString(rackAfterRemoval + drawn)

			if currentPlayer == 1 {
				score1 = turn.RunningTotal
			} else {
				score2 = turn.RunningTotal
			}
		}

		turns = append(turns, turn)

		if currentPlayer == 1 {
			rack1 = newRack
		} else {
			rack2 = newRack
		}

		if len(newRack) == 0 && len(pool) == 0 {
			endReason = "emptied"
			break
		}
		if consecutiveScoreless >= 6 {
			endReason = "sixPasses"
			break
		}

		currentPlayer = 3 - currentPlayer
	}

	// Final score adjustment - same two end-game rules as
	// sandboxGameFunctions.js's computeFinalScores: the rack-empty ending
	// gives the player who went out 2x the opponent's remaining rack value;
	// the six-scoreless-turns ending deducts each player's own remaining
	// rack value from their own score.
	if endReason == "emptied" {
		if len(rack1) == 0 {
			score1 += rackPointValue(rack2) * 2
		} else {
			score2 += rackPointValue(rack1) * 2
		}
	} else if endReason == "sixPasses" {
		score1 -= rackPointValue(rack1)
		score2 -= rackPointValue(rack2)
	}

	winner := 0
	if score1 > score2 {
		winner = 1
	} else if score2 > score1 {
		winner = 2
	}

	return SimGameResult{
		Turns:            turns,
		Player1Score:     score1,
		Player2Score:     score2,
		Winner:           winner,
		EndReason:        endReason,
		Player1FinalRack: rack1,
		Player2FinalRack: rack2,
		FinalPool:        pool,
	}
}

func simulateSeriesHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req SimulateSeriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	games := req.Games
	if games <= 0 {
		games = 1
	}
	if games > 500 {
		games = 500 // sane cap so one request can't run unbounded - matches the
		// app's own static-bot UI cap (500); Tess-involving matchups never
		// reach this endpoint at all, they stay on the client-side loop
	}

	player1Rank := req.Player1Rank
	if player1Rank < 1 {
		player1Rank = 1 // default/fallback: Theo
	}
	player2Rank := req.Player2Rank
	if player2Rank < 1 {
		player2Rank = 1
	}

	results := make([]SimGameResult, 0, games)
	for i := 0; i < games; i++ {
		results = append(results, simulateOneGame(player1Rank, player2Rank))
	}

	resp := SimulateSeriesResponse{Games: results}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
