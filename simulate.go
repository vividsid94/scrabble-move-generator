package main

// Full-game simulator for any matchup of "static" bots - ones that pick a
// fixed rank from a score+leaveValue-ranked candidate list every turn, with
// no per-move opponent simulation (that's Tess, which stays client-side).
// Theo is just rank 1; "Nth static" is any other rank 1-15 the app's UI
// offers. Additive to main-for-scrabble.go: reuses its package-level
// gd/alph/ld, its DetailedMove/MoveTile types, and its
// toDetailedMove/drawRandomTiles/removeRackFromPool helpers rather than
// duplicating them. generateMovesHandler and bulkMoveGenHandler now use this
// same score+leaveValue infrastructure too (getLeaveValue/sortLeaveString/
// allExchangeCandidates below), so this file is no longer the only place
// leave-aware ranking happens - it remains the only one that plays full games.
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

// vowelRunes classifies A/E/I/O/U as vowels for vowelCount/consonantCount
// rules - Y is deliberately excluded (treated as a consonant), matching
// common Scrabble rack-balance convention rather than linguistic rules.
var vowelRunes = map[rune]bool{'A': true, 'E': true, 'I': true, 'O': true, 'U': true}

func countRune(leave string, target rune) int {
	n := 0
	for _, c := range leave {
		if c == target {
			n++
		}
	}
	return n
}

func countVowels(leave string) int {
	n := 0
	for _, c := range leave {
		if vowelRunes[c] {
			n++
		}
	}
	return n
}

func countConsonants(leave string) int {
	n := 0
	for _, c := range leave {
		if c != '?' && !vowelRunes[c] {
			n++
		}
	}
	return n
}

// compareCount applies a rule's comparator ("gte" is the default when
// unset, plus "lte" and "eq") against an actual count and threshold.
func compareCount(actual int, comparator string, threshold int) bool {
	switch comparator {
	case "lte":
		return actual <= threshold
	case "eq":
		return actual == threshold
	default:
		return actual >= threshold
	}
}

// LeaveRule is one adjustment applied to a leave's base leaves.json value
// before ranking candidates - this is what lets a bot be more than just a
// fixed rank off the standard list: two bots at the same rank with
// different rules genuinely rank candidates differently. Bonus is a flat
// addition (negative for a penalty); Multiplier scales the running total
// at the point the rule appears, so a bot can combine several bonus rules
// and/or stack multiple multipliers for a compounding effect.
//
// Type determines which other fields apply:
//   - "containsLetter": Letter present anywhere in the leave -> Bonus
//   - "containsAny": any letter in Letters present -> Bonus
//   - "containsAll": every letter in Letters present -> Bonus
//   - "containsCount": count of Letter compared (Comparator) to Count -> Bonus
//   - "vowelCount": count of vowels (AEIOU) compared to Count -> Bonus
//   - "consonantCount": count of consonants (excludes '?') compared to Count -> Bonus
//   - "hasBlank": leave contains '?' -> Bonus
//   - "lengthEquals": leave length == Count -> Bonus
//   - "multiplier": scales the running total by Multiplier
type LeaveRule struct {
	Type       string  `json:"type"`
	Letter     string  `json:"letter,omitempty"`     // single letter, for containsLetter/containsCount
	Letters    string  `json:"letters,omitempty"`    // set of letters, for containsAny/containsAll
	Comparator string  `json:"comparator,omitempty"` // "gte" (default), "lte", "eq"
	Count      int     `json:"count,omitempty"`
	Bonus      float64 `json:"bonus,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty"`
}

// applyLeaveRules starts from the base leaves.json value for `leave` and
// applies each rule in order.
func applyLeaveRules(leave string, rules []LeaveRule) float64 {
	value := getLeaveValue(leave)

	for _, rule := range rules {
		switch rule.Type {
		case "containsLetter":
			if rule.Letter != "" && countRune(leave, []rune(rule.Letter)[0]) > 0 {
				value += rule.Bonus
			}
		case "containsAny":
			for _, l := range rule.Letters {
				if countRune(leave, l) > 0 {
					value += rule.Bonus
					break
				}
			}
		case "containsAll":
			if rule.Letters == "" {
				break
			}
			all := true
			for _, l := range rule.Letters {
				if countRune(leave, l) == 0 {
					all = false
					break
				}
			}
			if all {
				value += rule.Bonus
			}
		case "containsCount":
			if rule.Letter != "" && compareCount(countRune(leave, []rune(rule.Letter)[0]), rule.Comparator, rule.Count) {
				value += rule.Bonus
			}
		case "vowelCount":
			if compareCount(countVowels(leave), rule.Comparator, rule.Count) {
				value += rule.Bonus
			}
		case "consonantCount":
			if compareCount(countConsonants(leave), rule.Comparator, rule.Count) {
				value += rule.Bonus
			}
		case "hasBlank":
			if countRune(leave, '?') > 0 {
				value += rule.Bonus
			}
		case "lengthEquals":
			if len([]rune(leave)) == rule.Count {
				value += rule.Bonus
			}
		case "multiplier":
			if rule.Multiplier != 0 {
				value *= rule.Multiplier
			}
		}
	}

	return value
}

// BotConfig is one player's simulate-series configuration: a rank into the
// leave-value-ranked candidate list (1 = Theo/best; N = "Nth static"), plus
// an optional list of rules that adjust each leave's value before ranking.
type BotConfig struct {
	Rank       int         `json:"rank,omitempty"`
	LeaveRules []LeaveRule `json:"leaveRules,omitempty"`
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
	Player         int        `json:"player"` // 1 or 2
	Type           string     `json:"type"`   // "play" | "exchange" | "pass"
	Word           string     `json:"word,omitempty"`
	Score          int        `json:"score"`
	Position       string     `json:"position,omitempty"`
	Direction      string     `json:"direction,omitempty"`
	Tiles          []MoveTile `json:"tiles,omitempty"`
	RackBefore     string     `json:"rackBefore"`
	TilesExchanged string     `json:"tilesExchanged,omitempty"`
	RunningTotal   int        `json:"runningTotal"`

	// Only set when this player's bot has LeaveRules AND they actually
	// changed the outcome this turn - i.e. re-ranking this exact same
	// candidate list (same board, same rack, no RNG involved) by the plain
	// no-rule leave value would have picked something else. Lets the
	// frontend show which specific plays a custom rule actually affected,
	// vs. turns where it happened to agree with the plain baseline anyway.
	RuleImpacted           bool   `json:"ruleImpacted,omitempty"`
	BaselineType           string `json:"baselineType,omitempty"` // "play" | "exchange"
	BaselineWord           string `json:"baselineWord,omitempty"`
	BaselineScore          int    `json:"baselineScore,omitempty"`
	BaselineTilesExchanged string `json:"baselineTilesExchanged,omitempty"`
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
	Games       int        `json:"games,omitempty"`
	Player1Rank int        `json:"player1Rank,omitempty"` // legacy: rank-only, no rules. Ignored if Player1Bot is set.
	Player2Rank int        `json:"player2Rank,omitempty"`
	Player1Bot  *BotConfig `json:"player1Bot,omitempty"` // rank + optional leave rules; takes precedence over Player1Rank
	Player2Bot  *BotConfig `json:"player2Bot,omitempty"`
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
	// baselineTotal is the same candidate's score/leave value with NO
	// custom LeaveRules applied - i.e. applyLeaveRules(leave, nil), which
	// is just the plain leaves.json lookup. Always populated (cheap to
	// compute alongside total) so a second ranking pass can determine what
	// a rule-free bot at the same rank would have picked from this exact
	// same candidate list, without re-simulating anything.
	baselineTotal float64
}

// sameCandidate reports whether two candidates represent the same actual
// move - used to tell whether a custom leave rule actually changed this
// turn's outcome, not just its score.
func sameCandidate(a, b *scoredCandidate) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.isExchange != b.isExchange {
		return false
	}
	if a.isExchange {
		return a.exchangeTiles == b.exchangeTiles
	}
	return a.detailed.Word == b.detailed.Word &&
		a.detailed.StartPosition == b.detailed.StartPosition &&
		a.detailed.Direction == b.detailed.Direction
}

// simulateOneGame plays one complete game start to finish between two
// "static" bots (player1Bot/player2Bot - Theo is rank 1, no rules) and
// returns its full turn-by-turn history plus final scoring. Every move
// decision builds the full candidate list (word plays + exchanges) and
// ranks it by score+leaveValue itself (rather than trusting whatever order
// GenAll returns moves in), so rank 1 matches the app's Theo definition by
// construction, and rank N just indexes further into the same list. Each
// bot's LeaveRules adjust every leave's value (via applyLeaveRules) before
// ranking, so two bots at the same rank can genuinely play differently.
func simulateOneGame(player1Bot, player2Bot BotConfig) SimGameResult {
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
		currentBot := player1Bot
		if currentPlayer == 2 {
			currentRack = rack2
			currentBot = player2Bot
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
			baseLeaveValue := getLeaveValue(leave)
			total := float64(detailed.Score) + applyLeaveRules(leave, currentBot.LeaveRules)
			baselineTotal := float64(detailed.Score) + baseLeaveValue

			candidates = append(candidates, scoredCandidate{move: m, detailed: detailed, leave: leave, total: total, baselineTotal: baselineTotal})
		}

		canExchange := len(pool) >= 7
		if canExchange {
			// allExchangeCandidates scores against the plain leaves.json table
			// (it's shared with endpoints that have no concept of per-bot
			// rules) - that plain value IS the baseline, so stash it before
			// overwriting total with this bot's rule-adjusted value.
			for _, ex := range allExchangeCandidates(currentRack) {
				ex.baselineTotal = ex.total
				ex.total = applyLeaveRules(ex.leave, currentBot.LeaveRules)
				candidates = append(candidates, ex)
			}
		}

		// Stable sort: candidates were appended word-plays-first, so on an
		// exact tie in total, a word play keeps its position ahead of a
		// tied exchange, and two tied word plays keep GenAll's original
		// relative order - the same tie-breaking a single-pass max-scan
		// would give rank 1, just generalized to rank N.
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].total > candidates[j].total })

		rank := currentBot.Rank
		if rank < 1 {
			rank = 1
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

		// Rule-impact check: what would a plain (no custom rules) bot at the
		// same rank have picked from this EXACT same candidate list - same
		// board, same rack, no RNG involved, so this is a clean A/B on the
		// rule alone. Only worth computing when this bot actually has rules;
		// a rule-free bot can never diverge from itself.
		var ruleImpacted bool
		var baselineChosen *scoredCandidate
		if len(currentBot.LeaveRules) > 0 && len(candidates) > 0 {
			baseline := make([]scoredCandidate, len(candidates))
			copy(baseline, candidates)
			sort.SliceStable(baseline, func(i, j int) bool { return baseline[i].baselineTotal > baseline[j].baselineTotal })
			bIdx := rank - 1
			if bIdx < 0 || bIdx >= len(baseline) {
				bIdx = 0
			}
			baselineChosen = &baseline[bIdx]
			ruleImpacted = !sameCandidate(chosen, baselineChosen)
		}

		currentScoreBefore := score1
		if currentPlayer == 2 {
			currentScoreBefore = score2
		}

		turn := SimTurn{Player: currentPlayer, RackBefore: currentRack, RuleImpacted: ruleImpacted}
		if ruleImpacted {
			if baselineChosen.isExchange {
				turn.BaselineType = "exchange"
				turn.BaselineTilesExchanged = baselineChosen.exchangeTiles
			} else {
				turn.BaselineType = "play"
				turn.BaselineWord = baselineChosen.detailed.Word
				turn.BaselineScore = baselineChosen.detailed.Score
			}
		}
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

	// Resolve each player into a full BotConfig - an explicit player*Bot
	// object (rank + optional leave rules) takes precedence; otherwise fall
	// back to the legacy rank-only field for backward compatibility with
	// existing callers that only ever sent player1Rank/player2Rank.
	player1Bot := BotConfig{Rank: 1}
	if req.Player1Bot != nil {
		player1Bot = *req.Player1Bot
	} else if req.Player1Rank >= 1 {
		player1Bot.Rank = req.Player1Rank
	}
	if player1Bot.Rank < 1 {
		player1Bot.Rank = 1 // default/fallback: Theo
	}

	player2Bot := BotConfig{Rank: 1}
	if req.Player2Bot != nil {
		player2Bot = *req.Player2Bot
	} else if req.Player2Rank >= 1 {
		player2Bot.Rank = req.Player2Rank
	}
	if player2Bot.Rank < 1 {
		player2Bot.Rank = 1
	}

	results := make([]SimGameResult, 0, games)
	for i := 0; i < games; i++ {
		results = append(results, simulateOneGame(player1Bot, player2Bot))
	}

	resp := SimulateSeriesResponse{Games: results}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
