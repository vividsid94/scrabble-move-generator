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
	"runtime"
	"sort"
	"strings"
	"sync"

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
	// IsTess selects a completely different selection algorithm from Rank -
	// see pickTessCandidate. Rank AND LeaveRules are both ignored when this
	// is true - Tess always evaluates on the plain leaves.json value
	// (candidate.baselineTotal), matching her original client-side
	// behavior exactly regardless of what LeaveRules a caller sends.
	IsTess bool `json:"isTess,omitempty"`
	// BingoAversion, if set, makes this bot (comedically) reluctant to
	// play bingos - see BingoAversionRule. Applies uniformly to whichever
	// selection mechanism is in play (rank-based or Tess), since it
	// filters the shared candidate pool before either one sees it.
	BingoAversion *BingoAversionRule `json:"bingoAversion,omitempty"`
	// SpecialSelection, if set to "longestWord" or "mostTiles", overrides
	// every other selection mechanism entirely - see
	// pickLongestOrMostTilesCandidate. Takes absolute precedence: Rank,
	// IsTess, LeaveRules, and BingoAversion are all ignored when this is
	// set (checked first in simulateOneGame's turn loop, and
	// BingoAversion's pool filtering is skipped outright) - deliberately
	// no conjunction with any other mode, so there's nothing to reconcile
	// between "play the longest word" and e.g. "but also avoid bingos."
	SpecialSelection string `json:"specialSelection,omitempty"` // "" | "longestWord" | "mostTiles"
}

// BingoAversionRule excludes bingo candidates (word plays using all 7 rack
// tiles) from a bot's candidate pool before ranking, via two independent,
// composable mechanisms:
//
//   - Probability (0-1): a per-turn coin flip - this fraction of the time,
//     ALL bingo candidates get excluded together. Checked once per turn
//     (not once per bingo candidate), so a partial value reads as "how
//     often does she chicken out," not "which specific bingo does she
//     skip." 0 or omitted means this mechanism never triggers - callers
//     must send 1 explicitly for "always never bingo," there's no
//     "presence implies always" default anymore now that this field can
//     be used independently of MaxProbabilityRank.
//   - MaxProbabilityRank: deterministically excludes any INDIVIDUAL bingo
//     candidate whose word - if exactly 7 or 8 letters - ranks worse
//     (numerically higher, less probable) than this cutoff in its own
//     length's NWL23 probability-order list (see bingoprobability.go;
//     rank 1 = most probable). A 9+ letter bingo (formed by hooking onto
//     board tiles) or a word simply absent from these lists is never
//     excluded by this - only Probability can filter those. 0 or omitted
//     disables this mechanism.
//
// Both apply every turn a bingo is on offer, in order: MaxProbabilityRank
// first (per-candidate), then Probability's coin flip on whatever bingo
// candidates remain (per-turn, all-or-nothing) - e.g. "she doesn't know
// sufficiently obscure words at all, and even for ones she knows,
// sometimes chickens out anyway."
type BingoAversionRule struct {
	Probability        float64 `json:"probability,omitempty"`
	MaxProbabilityRank int     `json:"maxProbabilityRank,omitempty"`
}

// tessCandidateCount/tessSimIterations mirror sandboxBotFunctions.js's Tess
// algorithm (top-15 candidates, N simulated opponent replies each) but with
// far fewer iterations per candidate - the client's 100 was calibrated for
// a single live HTTP decision; here the same decision gets made on every
// turn of every game in a series, so 100 would make a multi-game Tess
// series impractically slow. 20 is a starting guess, not a measured value -
// worth revisiting once this can actually be timed against a deployment.
const (
	tessCandidateCount = 15
	tessSimIterations  = 20
)

// pickTessCandidate mirrors sandboxBotFunctions.js's pickBotMove Tess
// branch: from the top tessCandidateCount candidates by plain score+leave
// value (baselineTotal - Tess ignores LeaveRules entirely, unlike the
// rank-based bots, so any custom rules a caller attaches to a Tess
// BotConfig have zero effect on her), simulate tessSimIterations random
// opponent replies for each - drawing a random rack from the shared bag
// (pool already excludes both players' actual racks, so this is the same
// "unseen tiles" proxy the client version uses, not the specific known
// opponent rack) and scoring their best generic score+plain-leave reply via
// pickBestCandidate (the same logic /bulk-move-gen itself uses for its own
// opponent-simulation) - then picks whichever candidate maximizes
// baselineTotal - 2*avgOpponentReply. Unlike the client's version (15
// separate HTTP calls to /bulk-move-gen, 100 iterations each), this runs
// entirely in-process against the board/pool state simulateOneGame already
// holds in memory - it never mutates bd or pool, only reads them (board
// copies for word-play candidates are made and discarded internally).
func pickTessCandidate(candidates []scoredCandidate, bd *board.GameBoard, alph *tilemapping.TileMapping, pool string) *scoredCandidate {
	if len(candidates) == 0 {
		return nil
	}

	// Re-sort by baselineTotal rather than trusting the incoming order -
	// candidates arrive already sorted by total, which may be rule-adjusted
	// for a rank-based bot on the other side of the board; Tess's own pool
	// selection must never be influenced by LeaveRules either.
	sorted := make([]scoredCandidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].baselineTotal > sorted[j].baselineTotal })

	topN := sorted
	if len(topN) > tessCandidateCount {
		topN = topN[:tessCandidateCount]
	}

	// No unseen tiles left to simulate an opponent reply from - fall back
	// to the plain best-by-total candidate, matching the client's own
	// "empty pool -> no simulated threat" fallback.
	if len(pool) == 0 {
		return &topN[0]
	}

	var best *scoredCandidate
	bestAdjusted := 0.0

	for i := range topN {
		candidate := &topN[i]

		candidateBd := bd
		if !candidate.isExchange {
			candidateBd = bd.Copy()
			candidateBd.PlayMove(candidate.move)
			cross_set.UpdateCrossSetsForMove(candidateBd, candidate.move, gd, ld)
		}

		totalOpponentScore := 0
		validIterations := 0
		for j := 0; j < tessSimIterations; j++ {
			opponentRackStr, opponentRack := generateRandomRack(pool, 7, alph)
			if opponentRack == nil {
				continue
			}
			remainingPool := removeRackFromPool(pool, opponentRackStr)
			opponentGen := movegen.NewGordonGenerator(gd, candidateBd, ld)
			opponentRawMoves := opponentGen.GenAll(opponentRack, false)
			opponentChosen := pickBestCandidate(opponentRawMoves, candidateBd, alph, opponentRackStr, remainingPool)

			score := 0
			if opponentChosen != nil && !opponentChosen.isExchange {
				score = opponentChosen.detailed.Score
			}
			totalOpponentScore += score
			validIterations++
		}

		avgOpponentScore := 0.0
		if validIterations > 0 {
			avgOpponentScore = float64(totalOpponentScore) / float64(validIterations)
		}

		adjusted := candidate.baselineTotal - 2*avgOpponentScore
		if best == nil || adjusted > bestAdjusted {
			best = candidate
			bestAdjusted = adjusted
		}
	}

	return best
}

// pickLongestOrMostTilesCandidate implements the "Longest word" / "Most
// tiles played" Speedy override modes: a pure static tiebreak chain, no
// leave-value ranking and no simulation. mode "mostTiles" ranks by count of
// new (rack) tiles placed; anything else ranks by the resulting word's
// letter count (which can exceed 7 via a hook - a different thing than
// tiles placed from the rack). Either way, ties break by score, then by
// plain leaves.json leave value (deliberately getLeaveValue directly, never
// applyLeaveRules - these modes ignore LeaveRules entirely, not just
// de-prioritize them), then randomly among whatever's still tied.
// Exchanges are never considered (0 length, 0 tiles placed, so they could
// never win this comparison anyway) - if there's no legal word play at
// all, this returns nil, the same "pass" fallback every other selection
// mode uses.
func pickLongestOrMostTilesCandidate(candidates []scoredCandidate, mode string) *scoredCandidate {
	var wordPlays []scoredCandidate
	for _, c := range candidates {
		if !c.isExchange {
			wordPlays = append(wordPlays, c)
		}
	}
	if len(wordPlays) == 0 {
		return nil
	}

	metric := func(c *scoredCandidate) int {
		if mode == "mostTiles" {
			n := 0
			for _, t := range c.detailed.Tiles {
				if t.IsNew {
					n++
				}
			}
			return n
		}
		return len([]rune(c.detailed.Word))
	}

	sort.SliceStable(wordPlays, func(i, j int) bool {
		if mi, mj := metric(&wordPlays[i]), metric(&wordPlays[j]); mi != mj {
			return mi > mj
		}
		if wordPlays[i].detailed.Score != wordPlays[j].detailed.Score {
			return wordPlays[i].detailed.Score > wordPlays[j].detailed.Score
		}
		return getLeaveValue(wordPlays[i].leave) > getLeaveValue(wordPlays[j].leave)
	})

	// Ties are contiguous at the front after that stable descending sort -
	// collect every candidate matching the top of the sort on all three
	// criteria, then break the final tie randomly rather than silently
	// taking GenAll's arbitrary internal ordering.
	best := wordPlays[0]
	bestMetric, bestScore, bestLeave := metric(&best), best.detailed.Score, getLeaveValue(best.leave)
	tied := []scoredCandidate{best}
	for i := 1; i < len(wordPlays); i++ {
		c := wordPlays[i]
		if metric(&c) != bestMetric || c.detailed.Score != bestScore || getLeaveValue(c.leave) != bestLeave {
			break
		}
		tied = append(tied, c)
	}
	chosen := tied[rand.Intn(len(tied))]
	return &chosen
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

	// Same idea as RuleImpacted/Baseline* above, but for BingoAversion: only
	// set when this player's bot has it configured AND it actually removed
	// what would otherwise have been played this turn - i.e. re-running the
	// same selection over the candidate list from BEFORE bingo filtering
	// would have picked something else (almost always a bingo that got
	// excluded). Like RuleImpacted, only computed for rank-based bots - see
	// the comment at its computation site for why Tess is out of scope.
	BingoAversionImpacted         bool   `json:"bingoAversionImpacted,omitempty"`
	WithoutAversionType           string `json:"withoutAversionType,omitempty"` // "play" | "exchange"
	WithoutAversionWord           string `json:"withoutAversionWord,omitempty"`
	WithoutAversionScore          int    `json:"withoutAversionScore,omitempty"`
	WithoutAversionTilesExchanged string `json:"withoutAversionTilesExchanged,omitempty"`
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
	// isBingo is true for a word play that used all 7 of the tiles in the
	// rack it was generated from - always false for exchanges. Used by
	// BingoAversionRule to filter the candidate pool before ranking.
	isBingo bool
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

// simulateOneGame plays one complete game start to finish between two bots
// (player1Bot/player2Bot) and returns its full turn-by-turn history plus
// final scoring. Every move decision builds the full candidate list (word
// plays + exchanges), scored by score+leaveValue (rather than trusting
// whatever order GenAll returns moves in). A "static" bot (Theo = rank 1,
// or a user-chosen Nth rank) just indexes into that list sorted by total,
// which its own LeaveRules adjust (via applyLeaveRules) - so two static
// bots at the same rank can genuinely play differently. A Tess bot
// (IsTess true) instead runs pickTessCandidate's opponent-simulation
// selection, which ignores LeaveRules entirely and always uses the plain
// baselineTotal - see that function's comment for why.
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

			candidates = append(candidates, scoredCandidate{
				move: m, detailed: detailed, leave: leave, total: total, baselineTotal: baselineTotal,
				isBingo: len(used) == 7,
			})
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

		// Snapshot the pool before bingo-filtering, purely so the
		// BingoAversionImpacted comparison below (rank-based bots only) can
		// later ask "what would this bot have picked with no aversion at
		// all" - cheap to copy, skipped entirely when it'd never be used.
		var unfilteredForBingoCompare []scoredCandidate
		if currentBot.SpecialSelection == "" && currentBot.BingoAversion != nil && !currentBot.IsTess {
			unfilteredForBingoCompare = make([]scoredCandidate, len(candidates))
			copy(unfilteredForBingoCompare, candidates)
		}

		// Bingo aversion filters the pool before anything else sees it, so
		// both the rank-based total sort below and Tess's own re-sort by
		// baselineTotal are already working from the same reduced list -
		// no special-casing needed downstream for either selection mode.
		// Skipped entirely under SpecialSelection - those modes exist to
		// actively seek out bingos, the opposite of what aversion is for,
		// so there's no conjunction to reconcile between the two.
		if ba := currentBot.BingoAversion; ba != nil && currentBot.SpecialSelection == "" {
			// Per-candidate: deterministically drop any individual bingo
			// whose word isn't well-known enough. Words with no rank at
			// all (9+ letters via a hook, or absent from the NWL23 lists)
			// are never excluded here - they're simply not restricted by
			// this mechanism.
			if ba.MaxProbabilityRank > 0 {
				withinKnownRank := make([]scoredCandidate, 0, len(candidates))
				for _, c := range candidates {
					if c.isBingo {
						if rank, ok := bingoProbabilityRank[c.detailed.Word]; ok && rank > ba.MaxProbabilityRank {
							continue // too obscure - drop this one candidate
						}
					}
					withinKnownRank = append(withinKnownRank, c)
				}
				candidates = withinKnownRank
			}

			// Per-turn: a coin flip that, when it lands, excludes every
			// remaining bingo candidate together - layered on top of
			// whatever the rank filter above already left her.
			if ba.Probability > 0 && rand.Float64() < ba.Probability {
				withoutBingos := make([]scoredCandidate, 0, len(candidates))
				for _, c := range candidates {
					if !c.isBingo {
						withoutBingos = append(withoutBingos, c)
					}
				}
				candidates = withoutBingos
			}
		}

		// Stable sort: candidates were appended word-plays-first, so on an
		// exact tie in total, a word play keeps its position ahead of a
		// tied exchange, and two tied word plays keep GenAll's original
		// relative order - the same tie-breaking a single-pass max-scan
		// would give rank 1, just generalized to rank N.
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].total > candidates[j].total })

		var chosen *scoredCandidate
		// rank stays 0 for Tess and SpecialSelection (meaningless for
		// either) - only used below for the rank-based rule-impact
		// baseline.
		var rank int
		switch {
		case currentBot.SpecialSelection != "":
			// Absolute override - takes precedence over everything else,
			// by construction rather than by checking flags in combination
			// (see BotConfig.SpecialSelection's comment).
			chosen = pickLongestOrMostTilesCandidate(candidates, currentBot.SpecialSelection)
		case currentBot.IsTess:
			chosen = pickTessCandidate(candidates, bd, alph, pool)
		default:
			rank = currentBot.Rank
			if rank < 1 {
				rank = 1
			}
			idx := rank - 1
			if idx < 0 || idx >= len(candidates) {
				// Requested rank exceeds how many legal options exist this
				// turn (common late-game) - fall back to the best available,
				// matching the original client-side Intermediate bot's same
				// fallback.
				idx = 0
			}
			if len(candidates) > 0 {
				chosen = &candidates[idx]
			}
		}

		// Rule-impact check: what would a plain (no custom rules) bot at the
		// same rank have picked from this EXACT same candidate list - same
		// board, same rack, no RNG involved, so this is a clean A/B on the
		// rule alone. Only computed for rank-based bots for now - Tess's
		// selection isn't rank-based (it's a 15-candidate x N-iteration
		// opponent simulation), so a fair baseline would mean re-running
		// that whole simulation a second time; left out of scope here to
		// keep her already-heavier per-turn cost in check. Also skipped
		// under SpecialSelection, which ignores LeaveRules entirely.
		var ruleImpacted bool
		var baselineChosen *scoredCandidate
		if currentBot.SpecialSelection == "" && !currentBot.IsTess && len(currentBot.LeaveRules) > 0 && len(candidates) > 0 {
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

		// Bingo-aversion-impact check: what would this bot have picked from
		// the candidate list from BEFORE bingo filtering - same rank, same
		// board/rack, no RNG - i.e. a clean A/B on the aversion alone.
		// unfilteredForBingoCompare is only non-nil when it's worth doing
		// (BingoAversion set, rank-based bot) - see where it's populated.
		var bingoAversionImpacted bool
		var withoutAversionChosen *scoredCandidate
		if unfilteredForBingoCompare != nil && len(unfilteredForBingoCompare) > 0 {
			sort.SliceStable(unfilteredForBingoCompare, func(i, j int) bool {
				return unfilteredForBingoCompare[i].total > unfilteredForBingoCompare[j].total
			})
			wIdx := rank - 1
			if wIdx < 0 || wIdx >= len(unfilteredForBingoCompare) {
				wIdx = 0
			}
			withoutAversionChosen = &unfilteredForBingoCompare[wIdx]
			bingoAversionImpacted = !sameCandidate(chosen, withoutAversionChosen)
		}

		currentScoreBefore := score1
		if currentPlayer == 2 {
			currentScoreBefore = score2
		}

		turn := SimTurn{
			Player: currentPlayer, RackBefore: currentRack,
			RuleImpacted: ruleImpacted, BingoAversionImpacted: bingoAversionImpacted,
		}
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
		if bingoAversionImpacted {
			if withoutAversionChosen.isExchange {
				turn.WithoutAversionType = "exchange"
				turn.WithoutAversionTilesExchanged = withoutAversionChosen.exchangeTiles
			} else {
				turn.WithoutAversionType = "play"
				turn.WithoutAversionWord = withoutAversionChosen.detailed.Word
				turn.WithoutAversionScore = withoutAversionChosen.detailed.Score
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
		// Sane cap so one request can't run unbounded - matches the app's
		// static-bot UI cap. NOTE: this cap was sized for cheap rank-based
		// bots. A Tess bot (IsTess) is ~tessCandidateCount*tessSimIterations
		// (300x) more expensive per turn - the frontend is expected to send
		// a much lower games count whenever either bot is Tess (see
		// sandboxStore.js's getMaxGamesForBots), but this handler itself
		// doesn't enforce a lower cap for that case yet.
		games = 500
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

	// Every game is fully independent (simulateOneGame touches no shared
	// mutable state - see the package-level vars' comments; gd/alph/ld/
	// leaveValues/bingoProbabilityRank are all write-once-at-startup,
	// read-only from here on), so games run concurrently across a bounded
	// worker pool instead of one at a time. Each worker owns a distinct
	// index into results (handed out via the jobs channel, never shared
	// between workers), so no two goroutines ever touch the same slot -
	// safe without a mutex on the results slice itself. Bounded rather
	// than one-goroutine-per-game so a 500-game series doesn't spin up 500
	// concurrent goroutines all competing for the same handful of CPU
	// cores - more workers than cores just adds scheduling overhead here,
	// it doesn't run more of them at once than the hardware actually has.
	results := make([]SimGameResult, games)
	numWorkers := runtime.GOMAXPROCS(0)
	if numWorkers > games {
		numWorkers = games
	}

	jobs := make(chan int, games)
	for i := 0; i < games; i++ {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for gameIndex := range jobs {
				results[gameIndex] = simulateOneGame(player1Bot, player2Bot)
			}
		}()
	}
	wg.Wait()

	resp := SimulateSeriesResponse{Games: results}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
