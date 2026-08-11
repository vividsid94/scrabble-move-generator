package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/kwg"
	"github.com/domino14/word-golib/tilemapping"
)

// RulesBot: a "beat Theo without simulation" bot - six hardcoded, additive
// defense heuristics layered on top of the normal score+leave total,
// derived from mining ~148K expert-annotated Scrabble moves (see
// scrabbleapp's scripts/bot-training/retrieval/retrieval-index.jsonl) for
// recurring patterns real strong players describe when they deviate from
// the highest-scoring play. Selected via BotConfig.IsRulesBot in
// simulate.go - this file holds every rule's actual logic; simulate.go
// only has the one switch branch that calls pickRulesBotCandidate.
//
// Each rule is capped at its own "equity sacrifice limit" (ECL) - the most
// it's ever allowed to subtract from a candidate's score+leave total,
// regardless of how badly the rule is violated. Rules 1 and 2 only apply
// on the game's opening play; the rest apply every turn. Rules 3 and 6
// (closeness, lane count) have no natural absolute scale - a "closed"
// board looks completely different on turn 3 than turn 30 - so they're
// scored relative to this turn's OWN candidates instead: whichever
// candidate is most compact/has the fewest lanes gets 0 penalty from that
// rule, the worst gets the full ECL, everything else interpolates.

const (
	rulesBotOpeningDLSVowelECL = 4.0
	rulesBotOpeningStarSECL    = 10.0
	rulesBotClosenessECL       = 8.0
	rulesBotTLSVowelECL        = 5.0
	rulesBotTWSVowelECL        = 10.0
	rulesBotLaneCountECL       = 6.0
)

// Per-type ECL for rule 5 (a hook lands on a premium square) - unlike rule
// 4, this covers all four premium types, not just TLS/TWS.
var rulesBotHookPremiumECL = map[string]float64{
	"DLS": 3.0,
	"TLS": 6.0,
	"DWS": 6.0,
	"TWS": 10.0,
}

// ---- Board geometry ----
//
// Coordinates verified directly against Macondo's own board.CrosswordGameBoard
// layout (github.com/domino14/macondo v0.10.9, board/layouts.go) and its
// character legend (board/board.go: ' = DLS, " = TLS, - = DWS, = = TWS) -
// not re-derived from board.GameBoard's own accessors, so these rules work
// identically regardless of what bonus-square API a future Macondo version
// exposes or doesn't. All 0-indexed (row 0-14, col 0-14).

type rcPos struct{ row, col int }

var starSquare = rcPos{7, 7} // H8, the center/opening square

var twsSquares = map[rcPos]bool{
	{0, 0}: true, {0, 7}: true, {0, 14}: true,
	{7, 0}: true, {7, 14}: true,
	{14, 0}: true, {14, 7}: true, {14, 14}: true,
}

var tlsSquares = map[rcPos]bool{
	{1, 5}: true, {1, 9}: true,
	{5, 1}: true, {5, 5}: true, {5, 9}: true, {5, 13}: true,
	{9, 1}: true, {9, 5}: true, {9, 9}: true, {9, 13}: true,
	{13, 5}: true, {13, 9}: true,
}

var dlsSquares = map[rcPos]bool{
	{0, 3}: true, {0, 11}: true,
	{2, 6}: true, {2, 8}: true,
	{3, 0}: true, {3, 7}: true, {3, 14}: true,
	{6, 2}: true, {6, 6}: true, {6, 8}: true, {6, 12}: true,
	{7, 3}: true, {7, 11}: true,
	{8, 2}: true, {8, 6}: true, {8, 8}: true, {8, 12}: true,
	{11, 0}: true, {11, 7}: true, {11, 14}: true,
	{12, 6}: true, {12, 8}: true,
	{14, 3}: true, {14, 11}: true,
}

var dwsSquares = map[rcPos]bool{
	{1, 1}: true, {1, 13}: true,
	{2, 2}: true, {2, 12}: true,
	{3, 3}: true, {3, 11}: true,
	{4, 4}: true, {4, 10}: true,
	{7, 7}: true, // center
	{10, 4}: true, {10, 10}: true,
	{11, 3}: true, {11, 11}: true,
	{12, 2}: true, {12, 12}: true,
	{13, 1}: true, {13, 13}: true,
}

// Rule 1's two specific risk squares: 8G and 8I, directly between center
// and the DLS squares at 7G/7I/9G/9I - a vowel here invites a perpendicular
// hook straight into those DLS squares.
var rulesBotOpeningRiskSquares = map[rcPos]bool{
	{7, 6}: true, // 8G
	{7, 8}: true, // 8I
}

var rulesBotVowels = map[string]bool{"A": true, "E": true, "I": true, "O": true, "U": true}

// ---- Entry point ----

// candidatePenaltyBreakdown is every one of RulesBot's six penalty
// components for one candidate, computed once and shared by both
// pickRulesBotCandidate (which only needs the final adjusted total) and the
// /rulesbot-debug endpoint below (which needs every component individually,
// to answer "why did X beat Y" for one specific decision).
type candidatePenaltyBreakdown struct {
	candidate *scoredCandidate

	openingVowelPenalty float64
	openingStarPenalty  float64
	vowelPremiumPenalty float64
	hookPremiumPenalty  float64
	closenessPenalty    float64
	laneCountPenalty    float64

	boxArea   int
	laneCount int
}

func (b *candidatePenaltyBreakdown) totalPenalty() float64 {
	return b.openingVowelPenalty + b.openingStarPenalty + b.vowelPremiumPenalty +
		b.hookPremiumPenalty + b.closenessPenalty + b.laneCountPenalty
}

// adjustedTotal is the number RulesBot actually ranks candidates by - see
// pickRulesBotCandidate's doc comment for why exchanges are exempt from
// every penalty.
func (b *candidatePenaltyBreakdown) adjustedTotal() float64 {
	if b.candidate.isExchange {
		return b.candidate.total
	}
	return b.candidate.total - b.totalPenalty()
}

// computeRulesBotBreakdowns runs all six rules against every candidate and
// returns each one's full penalty breakdown, including rules 3 and 6's
// relative-to-this-turn scaling (see relativeScaledPenalty). This is the one
// place the per-candidate board-copy work happens - everything downstream
// (the actual pick, its RulesBotImpact, or a /rulesbot-debug response) just
// reads numbers back out of the result.
func computeRulesBotBreakdowns(gd *kwg.KWG, candidates []scoredCandidate, bd *board.GameBoard, isOpeningPlay bool) []candidatePenaltyBreakdown {
	breakdowns := make([]candidatePenaltyBreakdown, len(candidates))
	minBoxArea, maxBoxArea := -1, -1
	minLanes, maxLanes := -1, -1

	for i := range candidates {
		c := &candidates[i]
		breakdowns[i].candidate = c
		if c.isExchange {
			continue
		}

		candidateBd := bd.Copy()
		candidateBd.PlayMove(c.move)

		b := &breakdowns[i]
		if isOpeningPlay {
			b.openingVowelPenalty = openingDLSVowelPenalty(c.detailed)
			b.openingStarPenalty = openingStarSPenalty(c.detailed)
		}
		b.vowelPremiumPenalty = tlsTwsVowelPenalty(c.detailed, candidateBd)
		b.hookPremiumPenalty = hookOnPremiumPenalty(gd, c.detailed, candidateBd)
		b.boxArea = boardBoundingBoxArea(candidateBd)
		b.laneCount = countCurrentBingoLanes(candidateBd)

		if minBoxArea == -1 || b.boxArea < minBoxArea {
			minBoxArea = b.boxArea
		}
		if b.boxArea > maxBoxArea {
			maxBoxArea = b.boxArea
		}
		if minLanes == -1 || b.laneCount < minLanes {
			minLanes = b.laneCount
		}
		if b.laneCount > maxLanes {
			maxLanes = b.laneCount
		}
	}

	for i := range breakdowns {
		b := &breakdowns[i]
		if b.candidate.isExchange {
			continue
		}
		b.closenessPenalty = relativeScaledPenalty(b.boxArea, minBoxArea, maxBoxArea, rulesBotClosenessECL)
		b.laneCountPenalty = relativeScaledPenalty(b.laneCount, minLanes, maxLanes, rulesBotLaneCountECL)
	}

	return breakdowns
}

// RulesBotImpact answers, per turn, "did the six defense rules actually
// change what got played, and specifically which one(s) mattered" - the
// same question simulate.go already answers for LeaveRules/BingoAversion
// via RuleImpacted/BingoAversionImpacted (a single aggregate flag, since
// those are open-ended user-defined rule lists where individual attribution
// isn't meaningful), generalized here to RulesBot's six fixed, named rules,
// where knowing exactly which rule earned its equity sacrifice on a given
// turn is the whole point of evaluating them.
//
// Impacted/Baseline* mirror SimTurn.RuleImpacted's shape exactly: whether
// the real six-rule pick differs from what a plain score+leave bot (no
// RulesBot rules at all) would have chosen from this identical candidate
// list, and what that plain bot would have played instead.
//
// The six *Impacted fields are more granular: each asks "if ONLY this one
// rule's penalty were removed (the other five still fully applied), would
// the pick have changed?" More than one can be true on the same turn - e.g.
// two different rules might each independently prefer some other candidate
// over the actual winner.
type RulesBotImpact struct {
	Impacted               bool
	BaselineIsExchange     bool
	BaselineExchangeTiles  string
	BaselineWord           string
	BaselineScore          int

	OpeningVowelImpacted bool
	OpeningStarImpacted  bool
	ClosenessImpacted    bool
	VowelPremiumImpacted bool
	HookPremiumImpacted  bool
	LaneCountImpacted    bool
}

// pickRulesBotCandidate mirrors pickTessCandidate's shape (reads the same
// candidate list simulateOneGame already built, never mutates bd) but
// replaces her opponent-simulation with six deterministic, sim-free defense
// heuristics. Exchanges are left untouched by these rules (none of them are
// board-geometry-relevant to a play that never touches the board) but stay
// in the running normally. isOpeningPlay gates rules 1 and 2.
//
// Alongside the actual pick, it returns a RulesBotImpact built from the same
// per-candidate penalty components computed below - every "what would have
// won without rule X" counterfactual is just a different linear combination
// of numbers already on hand, so all eight selection objectives (the real
// one, the no-rules-at-all baseline, and six single-rule-dropped variants)
// are tracked in one extra pass with no additional board copies.
func pickRulesBotCandidate(gd *kwg.KWG, candidates []scoredCandidate, bd *board.GameBoard, isOpeningPlay bool) (*scoredCandidate, *RulesBotImpact) {
	if len(candidates) == 0 {
		return nil, nil
	}

	breakdowns := computeRulesBotBreakdowns(gd, candidates, bd, isOpeningPlay)

	// Each tracker holds the running best candidate for one selection
	// objective. score>t.score (strict) on ties keeps whichever candidate
	// was encountered first, matching the original single-tracker
	// implementation's tie-break (candidates arrive already sorted by
	// plain total, so a tie favors the higher-total candidate).
	type tracker struct {
		best  *scoredCandidate
		score float64
		set   bool
	}
	update := func(t *tracker, c *scoredCandidate, score float64) {
		if !t.set || score > t.score {
			t.best, t.score, t.set = c, score, true
		}
	}

	var real, baseline, noOpeningVowel, noOpeningStar, noCloseness, noVowelPremium, noHookPremium, noLaneCount tracker

	for i := range breakdowns {
		b := &breakdowns[i]
		c := b.candidate
		adjusted := b.adjustedTotal()

		update(&real, c, adjusted)
		update(&baseline, c, c.total)
		update(&noOpeningVowel, c, adjusted+b.openingVowelPenalty)
		update(&noOpeningStar, c, adjusted+b.openingStarPenalty)
		update(&noCloseness, c, adjusted+b.closenessPenalty)
		update(&noVowelPremium, c, adjusted+b.vowelPremiumPenalty)
		update(&noHookPremium, c, adjusted+b.hookPremiumPenalty)
		update(&noLaneCount, c, adjusted+b.laneCountPenalty)
	}

	chosen := real.best

	impact := &RulesBotImpact{
		Impacted:              !sameCandidate(chosen, baseline.best),
		OpeningVowelImpacted:  !sameCandidate(chosen, noOpeningVowel.best),
		OpeningStarImpacted:   !sameCandidate(chosen, noOpeningStar.best),
		ClosenessImpacted:     !sameCandidate(chosen, noCloseness.best),
		VowelPremiumImpacted:  !sameCandidate(chosen, noVowelPremium.best),
		HookPremiumImpacted:   !sameCandidate(chosen, noHookPremium.best),
		LaneCountImpacted:     !sameCandidate(chosen, noLaneCount.best),
	}
	if impact.Impacted {
		bl := baseline.best
		if bl.isExchange {
			impact.BaselineIsExchange = true
			impact.BaselineExchangeTiles = bl.exchangeTiles
		} else {
			impact.BaselineWord = bl.detailed.Word
			impact.BaselineScore = bl.detailed.Score
		}
	}

	return chosen, impact
}

// relativeScaledPenalty linearly interpolates value's penalty between 0 (at
// min, the best/lowest value this turn) and ecl (at max, the worst) - used
// for rules 3 and 6, which have no meaningful absolute scale on their own.
func relativeScaledPenalty(value, min, max int, ecl float64) float64 {
	if max <= min {
		return 0
	}
	return ecl * float64(value-min) / float64(max-min)
}

// ---- Rule 1: opening play, no vowel exposing the near-center DLS squares ----

func openingDLSVowelPenalty(detailed *DetailedMove) float64 {
	for _, t := range detailed.Tiles {
		if t.IsNew && rulesBotVowels[t.Letter] && rulesBotOpeningRiskSquares[rcPos{t.Row, t.Col}] {
			return rulesBotOpeningDLSVowelECL
		}
	}
	return 0
}

// ---- Rule 2: opening play, no S directly on the star ----

func openingStarSPenalty(detailed *DetailedMove) float64 {
	for _, t := range detailed.Tiles {
		if t.IsNew && t.Letter == "S" && t.Row == starSquare.row && t.Col == starSquare.col {
			return rulesBotOpeningStarSECL
		}
	}
	return 0
}

// ---- Rule 4: every turn, no vowel exposing an empty TLS/TWS square ----
//
// Each violation (one per exposed vowel/premium-square pair) is summed, not
// capped to a single occurrence - several new vowels each opening their own
// premium hook is worse than just one.

func tlsTwsVowelPenalty(detailed *DetailedMove, candidateBd *board.GameBoard) float64 {
	penalty := 0.0
	deltas := [4][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}}
	for _, t := range detailed.Tiles {
		if !t.IsNew || !rulesBotVowels[t.Letter] {
			continue
		}
		for _, d := range deltas {
			nr, nc := t.Row+d[0], t.Col+d[1]
			if nr < 0 || nr >= 15 || nc < 0 || nc >= 15 {
				continue
			}
			if candidateBd.GetLetter(nr, nc) != 0 {
				continue // already occupied post-move - no exposure left
			}
			pos := rcPos{nr, nc}
			if tlsSquares[pos] {
				penalty += rulesBotTLSVowelECL
			}
			if twsSquares[pos] {
				penalty += rulesBotTWSVowelECL
			}
		}
	}
	return penalty
}

// ---- Rule 5: every turn, no dictionary-valid hook landing on a premium square ----
//
// Uses kwg.FindHooks directly (a dedicated dictionary lookup) rather than
// running move generation to check word validity - the latter is what
// /validate-word does and it's far too expensive to call per-candidate,
// per-hook inside this loop. Scoped to the main played word's two ends only
// (not every cross-word this play also touches) - a reasonable first pass,
// not exhaustive.

func hookOnPremiumPenalty(gd *kwg.KWG, detailed *DetailedMove, candidateBd *board.GameBoard) float64 {
	if len(detailed.Tiles) == 0 {
		return 0
	}
	horizontal := detailed.Direction == "right"
	first := detailed.Tiles[0]
	last := detailed.Tiles[len(detailed.Tiles)-1]

	wordLetters := make([]tilemapping.MachineLetter, 0, len(detailed.Tiles))
	for _, t := range detailed.Tiles {
		ml, err := alph.Val(t.Letter)
		if err != nil {
			return 0 // shouldn't happen for a legal play - bail safely
		}
		wordLetters = append(wordLetters, ml)
	}

	penalty := 0.0

	beforeRow, beforeCol := first.Row, first.Col
	if horizontal {
		beforeCol--
	} else {
		beforeRow--
	}
	if squareType, ok := premiumTypeAt(beforeRow, beforeCol, candidateBd); ok {
		if len(kwg.FindHooks(gd, wordLetters, kwg.FrontHooks)) > 0 {
			penalty += rulesBotHookPremiumECL[squareType]
		}
	}

	afterRow, afterCol := last.Row, last.Col
	if horizontal {
		afterCol++
	} else {
		afterRow++
	}
	if squareType, ok := premiumTypeAt(afterRow, afterCol, candidateBd); ok {
		if len(kwg.FindHooks(gd, wordLetters, kwg.BackHooks)) > 0 {
			penalty += rulesBotHookPremiumECL[squareType]
		}
	}

	return penalty
}

// premiumTypeAt returns the premium-square type at (r,c) and whether it's
// currently empty and in-bounds - only an EMPTY premium square is an actual
// hook risk; an already-occupied one has nothing left to expose.
func premiumTypeAt(r, c int, bd *board.GameBoard) (string, bool) {
	if r < 0 || r >= 15 || c < 0 || c >= 15 {
		return "", false
	}
	if bd.GetLetter(r, c) != 0 {
		return "", false
	}
	pos := rcPos{r, c}
	switch {
	case twsSquares[pos]:
		return "TWS", true
	case dwsSquares[pos]:
		return "DWS", true
	case tlsSquares[pos]:
		return "TLS", true
	case dlsSquares[pos]:
		return "DLS", true
	}
	return "", false
}

// ---- Rule 3: prefer keeping tiles close together ----

// boardBoundingBoxArea is the area of the smallest rectangle containing
// every occupied square - a cheap, simulation-free proxy for "how spread
// out is play so far."
func boardBoundingBoxArea(bd *board.GameBoard) int {
	minRow, minCol, maxRow, maxCol := 15, 15, -1, -1
	for r := 0; r < 15; r++ {
		for c := 0; c < 15; c++ {
			if bd.GetLetter(r, c) != 0 {
				if r < minRow {
					minRow = r
				}
				if r > maxRow {
					maxRow = r
				}
				if c < minCol {
					minCol = c
				}
				if c > maxCol {
					maxCol = c
				}
			}
		}
	}
	if maxRow == -1 {
		return 0 // no tiles at all - shouldn't happen post-move, stay safe
	}
	return (maxRow - minRow + 1) * (maxCol - minCol + 1)
}

// ---- Rule 6: prefer fewer CURRENT bingo lanes ----

// countCurrentBingoLanes counts window positions (length 7 and 8, both
// orientations) that could accommodate a legal single-turn play RIGHT NOW -
// not "empty space that might be useful eventually," but places where a
// word of exactly that length could be built today, given the rack-size
// cap (at most 7 new tiles/turn) and standard connectivity (mirrors
// scrabbleapp's src/functions/analysisBoardFunctions.js's
// validateLaneSelection: touches an existing tile, or - only when the
// board is entirely empty - covers the center star). On an empty board
// this returns exactly the seven 7-tile windows through center and zero
// 8-tile windows (8 new tiles always exceeds the 7-tile cap, regardless of
// connectivity).
func countCurrentBingoLanes(bd *board.GameBoard) int {
	boardEmpty := isBoardEmpty(bd)
	count := 0
	for _, length := range []int{7, 8} {
		for r := 0; r < 15; r++ {
			for c := 0; c <= 15-length; c++ {
				if laneWindowIsLive(bd, r, c, length, true, boardEmpty) {
					count++
				}
			}
		}
		for c := 0; c < 15; c++ {
			for r := 0; r <= 15-length; r++ {
				if laneWindowIsLive(bd, r, c, length, false, boardEmpty) {
					count++
				}
			}
		}
	}
	return count
}

func isBoardEmpty(bd *board.GameBoard) bool {
	for r := 0; r < 15; r++ {
		for c := 0; c < 15; c++ {
			if bd.GetLetter(r, c) != 0 {
				return false
			}
		}
	}
	return true
}

func laneWindowIsLive(bd *board.GameBoard, startRow, startCol, length int, horizontal bool, boardEmpty bool) bool {
	occupied := 0
	touchesExisting := false
	coversStar := false
	deltas := [4][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}}

	for i := 0; i < length; i++ {
		r, c := startRow, startCol
		if horizontal {
			c += i
		} else {
			r += i
		}
		if bd.GetLetter(r, c) != 0 {
			occupied++
			touchesExisting = true
		}
		if r == starSquare.row && c == starSquare.col {
			coversStar = true
		}
		for _, d := range deltas {
			nr, nc := r+d[0], c+d[1]
			if nr >= 0 && nr < 15 && nc >= 0 && nc < 15 && bd.GetLetter(nr, nc) != 0 {
				touchesExisting = true
			}
		}
	}

	newTilesNeeded := length - occupied
	if newTilesNeeded < 1 || newTilesNeeded > 7 {
		return false
	}

	if boardEmpty {
		return coversStar
	}
	return touchesExisting
}

// ---- Debug/analysis endpoint ----
//
// /rulesbot-debug answers "why did RulesBot pick X over Y" for an arbitrary
// rack+board, without playing a full game. simulate.go's per-turn
// RulesBotImpacted/*Impacted flags (see RulesBotImpact above) tell you WHICH
// rule(s) were decisive during an actual series, but not by how much, and
// not what every other candidate scored - this exposes
// computeRulesBotBreakdowns' full output directly for one turn: every
// word-play candidate's raw score+leave, all six penalty components, and
// the adjustedTotal RulesBot actually ranks by. Sorted by adjustedTotal, so
// the top entry is exactly what RulesBot would play from this rack/board.
// Registered in main-for-scrabble.go's route list - see this file's other
// exported behavior for why the logic itself lives here instead.

type RulesBotDebugRequest struct {
	Rack    string     `json:"rack"`
	Board   [][]string `json:"board"` // 15x15, empty cells as ""
	TopN    int        `json:"topN,omitempty"`
	Lexicon string     `json:"lexicon,omitempty"`
}

type RulesBotDebugCandidate struct {
	IsExchange    bool   `json:"isExchange,omitempty"`
	ExchangeTiles string `json:"exchangeTiles,omitempty"`
	Word          string `json:"word,omitempty"`
	Position      string `json:"position,omitempty"`
	Score         int    `json:"score,omitempty"`
	Leave         string `json:"leave"`
	// RawTotal is score + plain leave value - what a rule-free bot at this
	// rack would be ranking by, i.e. RulesBotImpact's "baseline".
	RawTotal float64 `json:"rawTotal"`

	OpeningVowelPenalty float64 `json:"openingVowelPenalty,omitempty"`
	OpeningStarPenalty  float64 `json:"openingStarPenalty,omitempty"`
	ClosenessPenalty    float64 `json:"closenessPenalty,omitempty"`
	VowelPremiumPenalty float64 `json:"vowelPremiumPenalty,omitempty"`
	HookPremiumPenalty  float64 `json:"hookPremiumPenalty,omitempty"`
	LaneCountPenalty    float64 `json:"laneCountPenalty,omitempty"`
	TotalPenalty        float64 `json:"totalPenalty"`

	// AdjustedTotal is what RulesBot actually ranks candidates by -
	// RawTotal minus TotalPenalty (exchanges are exempt from every penalty,
	// so their AdjustedTotal always equals RawTotal).
	AdjustedTotal float64 `json:"adjustedTotal"`
	// BoxArea/LaneCount are rules 3/6's raw inputs before ECL scaling -
	// included so it's clear WHY one candidate's closeness/lane-count
	// penalty came out higher than another's, not just by how much.
	BoxArea   int `json:"boxArea,omitempty"`
	LaneCount int `json:"laneCount,omitempty"`
}

type RulesBotDebugResponse struct {
	IsOpeningPlay bool                     `json:"isOpeningPlay"`
	Candidates    []RulesBotDebugCandidate `json:"candidates"`
	Lexicon       string                   `json:"lexicon,omitempty"`
}

func rulesBotDebugHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RulesBotDebugRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Rack == "" {
		http.Error(w, "Rack is required", http.StatusBadRequest)
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
	if req.TopN <= 0 {
		req.TopN = 20
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Standard board only - RulesBot's own geometry (twsSquares etc.) is
	// hardcoded to it, so a custom-premium-square board would silently
	// mismatch those maps. Not a real limitation in practice: RulesBot
	// itself makes the same assumption everywhere else in this file.
	bd := board.MakeBoard(board.CrosswordGameBoard)
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
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	isOpeningPlay := isBoardEmpty(bd)

	rack := tilemapping.RackFromString(req.Rack, alph)
	generator := movegen.NewGordonGenerator(gd, bd, ld)
	rawMoves := generator.GenAll(rack, false)

	// Plain score+leave, no LeaveRules - this endpoint is for isolating
	// RulesBot's own six rules, independent of whatever a live game's bot
	// config might otherwise be composing them with.
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
		leave := sortLeaveString(removeRackFromPool(req.Rack, strings.Join(used, "")))
		total := float64(detailed.Score) + getLeaveValue(leave)
		candidates = append(candidates, scoredCandidate{
			move: m, detailed: detailed, leave: leave, total: total,
			isBingo: len(used) == 7,
		})
	}
	candidates = append(candidates, allExchangeCandidates(req.Rack)...)

	breakdowns := computeRulesBotBreakdowns(gd, candidates, bd, isOpeningPlay)
	sort.SliceStable(breakdowns, func(i, j int) bool { return breakdowns[i].adjustedTotal() > breakdowns[j].adjustedTotal() })
	if len(breakdowns) > req.TopN {
		breakdowns = breakdowns[:req.TopN]
	}

	respCandidates := make([]RulesBotDebugCandidate, 0, len(breakdowns))
	for _, b := range breakdowns {
		c := b.candidate
		rc := RulesBotDebugCandidate{
			Leave: c.leave, RawTotal: c.total,
			OpeningVowelPenalty: b.openingVowelPenalty, OpeningStarPenalty: b.openingStarPenalty,
			ClosenessPenalty: b.closenessPenalty, VowelPremiumPenalty: b.vowelPremiumPenalty,
			HookPremiumPenalty: b.hookPremiumPenalty, LaneCountPenalty: b.laneCountPenalty,
			TotalPenalty: b.totalPenalty(), AdjustedTotal: b.adjustedTotal(),
			BoxArea: b.boxArea, LaneCount: b.laneCount,
		}
		if c.isExchange {
			rc.IsExchange = true
			rc.ExchangeTiles = c.exchangeTiles
		} else {
			rc.Word = c.detailed.Word
			rc.Position = c.move.BoardCoords()
			rc.Score = c.detailed.Score
		}
		respCandidates = append(respCandidates, rc)
	}

	resp := RulesBotDebugResponse{IsOpeningPlay: isOpeningPlay, Candidates: respCandidates, Lexicon: lexiconName}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
