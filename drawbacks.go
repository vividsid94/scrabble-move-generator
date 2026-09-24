package main

import (
	"sort"
	"strings"

	"github.com/domino14/macondo/board"
)

// Drawback Scrabble: each player can be assigned a "drawback" - a
// constraint on which candidates they're allowed to play. This file holds
// the shared rule vocabulary (DrawbackRule), the registry of currently-
// implemented drawbacks (drawbackByID, identified by the same numbering
// the game-design doc uses - see whiffers' public/docs/drawback-ledger.html),
// and the interpreter that filters simulateOneGame's candidate list before
// ranking/selection, the same insertion point BingoAversion already uses.
//
// Categories A (fits an existing LeaveRule-style primitive), B (new but
// still stateless - no memory needed beyond this one move), and C (same
// stateless shape as B, evaluated backwards - see DrawbackRule.Forcing) are
// implemented here. D (stateful), E (scoring override), and F (deferred
// lose-conditions) all need machinery this file doesn't have yet.
//
// A JS mirror of this same rule vocabulary and registry lives in whiffers
// at src/data/drawbacks.js / src/functions/drawbacks/evaluate.js, for Play
// mode's human-move validation and bot-candidate filtering (not wired up
// yet - Sandbox is first). The two are meant to be kept in sync by hand,
// entry for entry - that's the trade being made instead of a slower,
// single shared per-move JS loop for Sandbox series.

// DrawbackRule is one drawback's actual constraint, in the same flat-
// struct-with-omitempty style as LeaveRule - Type selects which other
// fields apply. See each case in evaluateDrawback below for exactly what
// each field means for that type.
type DrawbackRule struct {
	Type string `json:"type"`

	Comparator string  `json:"comparator,omitempty"` // "gte" | "lte" | "eq", for any *Comparator-driven type
	Value      float64 `json:"value,omitempty"`

	Parity string `json:"parity,omitempty"` // "odd" | "even" - scoreParity, poolParityAfterMove
	In     []int  `json:"in,omitempty"`     // tileCount: legal tile-count values

	Forbid string `json:"forbid,omitempty"` // direction: "vertical" | "horizontal"

	RackLetters string `json:"rackLetters,omitempty"` // rackHasAnyLimitsTileCount
	MaxTiles    int    `json:"maxTiles,omitempty"`

	Letter       string `json:"letter,omitempty"` // letterUseRequiresRackCount
	MinRackCount int    `json:"minRackCount,omitempty"`

	MinVowels int `json:"minVowels,omitempty"` // vowelGateForScoring

	Types []string `json:"types,omitempty"` // forbiddenSquareTypes: "DWS" | "DLS" | "TWS" | "TLS"

	MaxValue int `json:"maxValue,omitempty"` // firstTileValueLimit

	ValueWhenBagEmpty float64 `json:"valueWhenBagEmpty,omitempty"` // wordsFormedCount

	N int `json:"n,omitempty"` // excludeTopNCandidates

	Letters  string `json:"letters,omitempty"` // restrictedTileSetScoreFloor
	MinScore int    `json:"minScore,omitempty"`

	// Forcing (category C - see the ledger) flips filterCandidatesByDrawback's
	// own aggregation policy for this rule: instead of dropping candidates
	// that fail Type's per-move check, it collapses the whole list down to
	// just the ones that PASS - but only when at least one does; with zero
	// qualifying candidates the rule doesn't bite this turn (the original
	// list comes back untouched) rather than forcing a play that doesn't
	// exist. Type itself still just describes the ordinary per-move
	// predicate (evaluateDrawback doesn't need to know about this flag at
	// all) - Forcing only changes what filterCandidatesByDrawback does with
	// the result.
	Forcing bool `json:"forcing,omitempty"`

	// AppliesToExchanges opts a rule into being checked against exchange
	// candidates too, instead of filterCandidatesByDrawback's default of
	// always letting them through untouched. That default is right for a
	// rule about the PLAYED WORD (word length, direction - an exchange has
	// neither), which is why it's the default; it's wrong for a rule about
	// what's KEPT (leaveValue), since an exchange produces a leave just as
	// much as a word play does, and exempting it lets a player dodge the
	// whole constraint by exchanging into a good leave instead of playing
	// into a bad one. evaluateDrawback needs no changes for this - its
	// leaveValue case already reads getLeaveValue(c.leave) generically for
	// any candidate type; this flag only changes whether
	// filterCandidatesByDrawback bothers calling it on an exchange at all.
	AppliesToExchanges bool `json:"appliesToExchanges,omitempty"`

	// AllowEmptyLeave is leaveValue's own bingo exception: a play that
	// empties the rack entirely (a real bingo, or just going out with
	// fewer than 7 left) has no leave to be negative, so "must keep a
	// negative-value leave" doesn't really describe that turn at all -
	// this lets it through as its own carve-out instead of failing it.
	// Never extends to exchanges (checked in evaluateDrawback itself, not
	// here) - exchanging your whole rack away isn't a bingo.
	AllowEmptyLeave bool `json:"allowEmptyLeave,omitempty"`
}

// DrawbackDef pairs one drawback's identity (its game number, name, and
// whether that name is Kevin's own or a placeholder assigned during
// triage) with its actual rule. ID is stable and cross-references the
// design doc's own numbering - never renumbered even though only a subset
// of IDs exist in this registry yet.
type DrawbackDef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// Description is the player-facing instruction text - not consulted by
	// evaluateDrawback (the Rule field is what's actually enforced), kept
	// here only so this file and whiffers' src/data/drawbacks.js stay
	// structurally identical entry for entry.
	Description string       `json:"description"`
	NameSource  string       `json:"nameSource"` // "friend" | "claude"
	Rule        DrawbackRule `json:"rule"`
}

// drawbacks is the current registry - categories A (fits an existing
// primitive), B (new stateless primitive), and C (forcing) only. Kept in
// the same order as the design doc for easy comparison.
var drawbacks = []DrawbackDef{
	// -- Category A --
	{ID: 3, Name: "Hippopotomonstrosesquipedaliophobia", NameSource: "friend",
		Description: "Can't play words longer than 5 letters.",
		Rule:        DrawbackRule{Type: "wordLength", Comparator: "lte", Value: 5}},
	{ID: 15, Name: "Modesty", NameSource: "claude",
		Description: "Can't score more than 35 points on a turn.",
		Rule:        DrawbackRule{Type: "score", Comparator: "lte", Value: 35}},
	{ID: 16, Name: "UV Gotta Be Kidding", NameSource: "friend",
		Description: "Must keep a leave with negative valuation every turn - unless you empty your rack.",
		// AppliesToExchanges: true - confirmed for real against rack
		// ?DEOPRT that without it, the bot exchanges into a strongly
		// positive leave (42.76) instead of playing an actual qualifying
		// word (best available: 21.9) - exchanges are exempt from every
		// OTHER drawback by default, but this one is specifically about
		// the leave, which an exchange produces too.
		//
		// AllowEmptyLeave: true - the bingo exception: emptying the rack
		// entirely has no leave to be negative, so it's a carve-out rather
		// than a failure (doesn't extend to exchanges - see
		// evaluateDrawback's own comment on why).
		Rule: DrawbackRule{Type: "leaveValue", Comparator: "lt", Value: 0, AppliesToExchanges: true, AllowEmptyLeave: true}},
	{ID: 31, Name: "Oddball", NameSource: "claude",
		Description: "Can't score an even amount of points on a turn.",
		Rule:        DrawbackRule{Type: "scoreParity", Parity: "odd"}},
	{ID: 35, Name: "Kingly Sum", NameSource: "friend",
		Description: "Main word's tiles must add up to at least 10.",
		Rule:        DrawbackRule{Type: "tileValueSum", Comparator: "gte", Value: 10}},
	{ID: 37, Name: "Gone Fishing", NameSource: "friend",
		Description: "Can only play 1, 2, or 7 tiles on a turn.",
		Rule:        DrawbackRule{Type: "tileCount", In: []int{1, 2, 7}}},

	// -- Category B --
	{ID: 1, Name: "Fear of Heights", NameSource: "friend",
		Description: "Main words can't be played vertically (1-tile plays exempt).",
		Rule:        DrawbackRule{Type: "direction", Forbid: "vertical"}},
	{ID: 2, Name: "Vertie", NameSource: "friend",
		Description: "Can't play horizontally (1-tile plays exempt).",
		Rule:        DrawbackRule{Type: "direction", Forbid: "horizontal"}},
	{ID: 4, Name: "Plurality", NameSource: "friend",
		Description: "If you hold an S or blank, you can only play one tile.",
		Rule:        DrawbackRule{Type: "rackHasAnyLimitsTileCount", RackLetters: "S?", MaxTiles: 1}},
	{ID: 5, Name: "Strength in Numbers", NameSource: "claude",
		Description: "Can only play an E if you have 3 or more E's on your rack.",
		Rule:        DrawbackRule{Type: "letterUseRequiresRackCount", Letter: "E", MinRackCount: 3}},
	{ID: 6, Name: "Well Balanced", NameSource: "friend",
		Description: "Can only score points if you have 3+ vowels on your rack.",
		Rule:        DrawbackRule{Type: "vowelGateForScoring", MinVowels: 3}},
	{ID: 11, Name: "Double Trouble", NameSource: "friend",
		Description: "Can't play a tile on a DWS or DLS square.",
		Rule:        DrawbackRule{Type: "forbiddenSquareTypes", Types: []string{"DWS", "DLS"}}},
	{ID: 22, Name: "Laureations", NameSource: "friend",
		Description: "Main word can't start with a tile worth more than 1 point.",
		Rule:        DrawbackRule{Type: "firstTileValueLimit", MaxValue: 1}},
	{ID: 26, Name: "Lone Wolf", NameSource: "claude",
		Description: "Your plays can only make one word - no cross-words.",
		Rule:        DrawbackRule{Type: "wordsFormedCount", Comparator: "eq", Value: 1}},
	{ID: 27, Name: "Diamond In The Rough", NameSource: "friend",
		Description: "Can't play either of the top 2 equity plays.",
		Rule:        DrawbackRule{Type: "excludeTopNCandidates", N: 2}},
	{ID: 28, Name: "Feel My Power", NameSource: "friend",
		Description: "Can't play S, J, K, Q, X, Z, or a blank unless you score 50+.",
		Rule:        DrawbackRule{Type: "restrictedTileSetScoreFloor", Letters: "SJKQXZ?", MinScore: 50}},
	{ID: 32, Name: "Parallel Play", NameSource: "friend",
		Description: "Plays must form 3+ words, or 2+ once the bag is empty.",
		Rule:        DrawbackRule{Type: "wordsFormedCount", Comparator: "gte", Value: 3, ValueWhenBagEmpty: 2}},
	{ID: 40, Name: "Even Steven", NameSource: "claude",
		Description: "Can't leave an odd number of tiles in the bag after your play.",
		// AppliesToExchanges: true - an exchange changes pool size exactly
		// the same way a word play does, so exempting it would let a
		// player dodge bad parity by exchanging instead of playing into it
		// (the same exploit shape #16 had). drawnTileCount (not
		// newTileCount, which returns 0 for any exchange - see its own
		// comment) is what makes the count itself correct for an exchange.
		Rule: DrawbackRule{Type: "poolParityAfterMove", Parity: "even", AppliesToExchanges: true}},

	// -- Category C: forcing --
	{ID: 29, Name: "Zyzzyva", NameSource: "friend",
		Description: "If a legal play starts with your rack's last-alphabetical tile, you must make it.",
		Rule:        DrawbackRule{Type: "startsWithLastAlphabeticalRackTile", Forcing: true}},
	{ID: 30, Name: "Fynbos", NameSource: "friend",
		Description: "If you can play exactly 6 tiles, you must.",
		Rule:        DrawbackRule{Type: "tileCount", In: []int{6}, Forcing: true}},
}

var drawbackByID = func() map[int]DrawbackDef {
	m := make(map[int]DrawbackDef, len(drawbacks))
	for _, d := range drawbacks {
		m[d.ID] = d
	}
	return m
}()

func compareFloat(actual float64, comparator string, threshold float64) bool {
	switch comparator {
	case "lte":
		return actual <= threshold
	case "lt":
		return actual < threshold
	case "eq":
		return actual == threshold
	default: // "gte"
		return actual >= threshold
	}
}

// ---- Board geometry ----
//
// Coordinates verified directly against Macondo's own board.CrosswordGameBoard
// layout (github.com/domino14/macondo v0.10.9, board/layouts.go) - all
// 0-indexed (row 0-14, col 0-14). Was originally part of rulesbot.go (a
// RulesBot-only file, since deleted along with RulesBot itself); moved here
// because it turned out to be generic board geometry, not anything
// RulesBot-specific, and this is now its only consumer.

type rcPos struct{ row, col int }

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

// premiumTypeAt returns the premium-square type at (r,c) and whether it's
// currently empty and in-bounds - only an EMPTY premium square is a real
// hit here, but for a NEW tile's own destination square that's always true
// by construction (it can't be a new tile if the square were already
// occupied), so this never actually filters anything out in practice for
// forbiddenSquareTypes - it's kept for safety and because premiumTypeAt is
// the established shape to match if this ever gets a second caller.
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

func newTileCount(c *scoredCandidate) int {
	n := 0
	for _, t := range c.detailed.Tiles {
		if t.IsNew {
			n++
		}
	}
	return n
}

// drawnTileCount is how many tiles this move will draw replacements for -
// newTileCount's own IsNew filter is right for a word play (a pre-existing
// board tile the play merely runs through isn't "new," and costs no draw)
// but wrong for an exchange: every one of an exchange's own tiles comes
// back marked IsNew:false (correctly - nothing's new ON THE BOARD, since
// an exchange never touches it), which would make newTileCount silently
// return 0 for ANY exchange instead of the real count being exchanged.
// Only poolParityAfterMove needs this distinction today (the one other
// rule, besides leaveValue, where an exchange affects the exact thing
// being measured - see that rule's own AppliesToExchanges).
func drawnTileCount(c *scoredCandidate) int {
	if c.isExchange {
		return len(c.detailed.Tiles)
	}
	return newTileCount(c)
}

func tileLetterValue(t MoveTile) int {
	if t.IsBlank || t.Letter == "" {
		return 0
	}
	return letterPointValues[[]rune(t.Letter)[0]]
}

// firstTileOf returns the play's first tile in reading order - leftmost
// for a horizontal play, topmost for a vertical one - which may be a
// pre-existing tile the play merely extends from, not necessarily a new
// one.
func firstTileOf(c *scoredCandidate) MoveTile {
	tiles := c.detailed.Tiles
	first := tiles[0]
	for _, t := range tiles[1:] {
		if c.detailed.Direction == "down" {
			if t.Row < first.Row {
				first = t
			}
		} else if t.Col < first.Col {
			first = t
		}
	}
	return first
}

// countWordsFormed is 1 (the main word) plus one more for every NEW tile
// that has an occupied square immediately before or after it on the
// PERPENDICULAR axis, on the board as it stood before this move - the same
// "does a cross word exist through here" check real Scrabble scoring
// already has to make, recomputed here since neither scoredCandidate nor
// DetailedMove expose a word count directly.
func countWordsFormed(c *scoredCandidate, bd *board.GameBoard) int {
	count := 1
	for _, t := range c.detailed.Tiles {
		if !t.IsNew {
			continue
		}
		var before, after bool
		if c.detailed.Direction == "down" {
			before = t.Col > 0 && bd.GetLetter(t.Row, t.Col-1) != 0
			after = t.Col < 14 && bd.GetLetter(t.Row, t.Col+1) != 0
		} else {
			before = t.Row > 0 && bd.GetLetter(t.Row-1, t.Col) != 0
			after = t.Row < 14 && bd.GetLetter(t.Row+1, t.Col) != 0
		}
		if before || after {
			count++
		}
	}
	return count
}

// evaluateDrawback reports whether candidate c is still allowed under
// rule, given the board as it stood before this move, the acting player's
// rack before this move, and how many tiles remain in the bag before this
// move. Only ever called for word-play candidates - exchanges are always
// left alone by every drawback here, the same way BingoAversion leaves
// them alone.
func evaluateDrawback(rule DrawbackRule, c *scoredCandidate, preMoveRack string, bd *board.GameBoard, poolSizeBefore int) bool {
	switch rule.Type {
	case "wordLength":
		return compareFloat(float64(len(c.detailed.Tiles)), rule.Comparator, rule.Value)

	case "score":
		return compareFloat(float64(c.detailed.Score), rule.Comparator, rule.Value)

	case "scoreParity":
		isEven := c.detailed.Score%2 == 0
		return (rule.Parity == "even") == isEven

	case "leaveValue":
		// AllowEmptyLeave (#16's own bingo exception - see its own struct
		// comment): a play emptying the rack has no leave to be negative
		// OR positive, so it's a carve-out rather than a failure. Excludes
		// exchanges on purpose - emptying your rack via an exchange isn't
		// a bingo, and shouldn't get the same free pass a real one does.
		if rule.AllowEmptyLeave && !c.isExchange && c.leave == "" {
			return true
		}
		return compareFloat(getLeaveValue(c.leave), rule.Comparator, rule.Value)

	case "tileValueSum":
		sum := 0
		for _, t := range c.detailed.Tiles {
			sum += tileLetterValue(t)
		}
		return compareFloat(float64(sum), rule.Comparator, rule.Value)

	case "tileCount":
		n := newTileCount(c)
		for _, v := range rule.In {
			if v == n {
				return true
			}
		}
		return false

	case "direction":
		// Kevin's own exemption: a play that's just one tile long overall
		// (necessarily the game's opening move - anything later must
		// connect to existing tiles, which makes it longer than one tile)
		// has no real direction to forbid.
		if len(c.detailed.Tiles) <= 1 {
			return true
		}
		if rule.Forbid == "vertical" {
			return c.detailed.Direction != "down"
		}
		return c.detailed.Direction != "right"

	case "rackHasAnyLimitsTileCount":
		hasAny := false
		for _, l := range rule.RackLetters {
			if strings.ContainsRune(preMoveRack, l) {
				hasAny = true
				break
			}
		}
		if !hasAny {
			return true
		}
		return newTileCount(c) <= rule.MaxTiles

	case "letterUseRequiresRackCount":
		usesLetter := false
		for _, t := range c.detailed.Tiles {
			if t.IsNew && !t.IsBlank && t.Letter == rule.Letter {
				usesLetter = true
				break
			}
		}
		if !usesLetter {
			return true
		}
		return countRune(preMoveRack, []rune(rule.Letter)[0]) >= rule.MinRackCount

	case "vowelGateForScoring":
		return countVowels(preMoveRack) >= rule.MinVowels

	case "forbiddenSquareTypes":
		for _, t := range c.detailed.Tiles {
			if !t.IsNew {
				continue
			}
			squareType, ok := premiumTypeAt(t.Row, t.Col, bd)
			if !ok {
				continue
			}
			for _, forbidden := range rule.Types {
				if squareType == forbidden {
					return false
				}
			}
		}
		return true

	case "firstTileValueLimit":
		return tileLetterValue(firstTileOf(c)) <= rule.MaxValue

	case "wordsFormedCount":
		threshold := rule.Value
		if poolSizeBefore == 0 && rule.ValueWhenBagEmpty > 0 {
			threshold = rule.ValueWhenBagEmpty
		}
		return compareFloat(float64(countWordsFormed(c, bd)), rule.Comparator, threshold)

	case "restrictedTileSetScoreFloor":
		usesRestricted := false
		for _, t := range c.detailed.Tiles {
			if !t.IsNew {
				continue
			}
			if t.IsBlank {
				if strings.ContainsRune(rule.Letters, '?') {
					usesRestricted = true
					break
				}
				continue
			}
			if strings.ContainsRune(rule.Letters, []rune(t.Letter)[0]) {
				usesRestricted = true
				break
			}
		}
		if !usesRestricted {
			return true
		}
		return c.detailed.Score >= rule.MinScore

	case "poolParityAfterMove":
		drawn := drawnTileCount(c)
		if drawn > poolSizeBefore {
			drawn = poolSizeBefore
		}
		after := poolSizeBefore - drawn
		isEven := after%2 == 0
		return (rule.Parity == "even") == isEven

	// Category C ("Forcing"): still just an ordinary per-move predicate -
	// "does THIS move start with the target letter" - same as every case
	// above. rule.Forcing (checked in filterCandidatesByDrawback, not here)
	// is what turns that into "must play a qualifying move if one exists."
	// Blanks are excluded from "last-alphabetical" - a blank has no
	// inherent letter until placed, so it's not a candidate for "your
	// rack's Z-iest tile"; a rack of nothing but blanks has no target at
	// all (returns false for every move, which correctly means "no
	// qualifying candidate" -> no forcing this turn).
	case "startsWithLastAlphabeticalRackTile":
		var lastAlpha rune
		for _, l := range preMoveRack {
			if l == '?' {
				continue
			}
			if lastAlpha == 0 || l > lastAlpha {
				lastAlpha = l
			}
		}
		if lastAlpha == 0 {
			return false
		}
		first := firstTileOf(c)
		return first.IsNew && !first.IsBlank && first.Letter == string(lastAlpha)

	default:
		// Unknown/not-yet-implemented type - never silently disqualify a
		// player's whole move set over a rule this build doesn't know how
		// to check.
		return true
	}
}

// filterCandidatesByDrawback applies one drawback to a turn's already-
// built candidate list, leaving exchanges untouched (same convention
// BingoAversion uses) and word plays filtered by evaluateDrawback - except
// excludeTopNCandidates, which needs the word plays' relative rank rather
// than a per-candidate check, so it's handled as its own pass.
func filterCandidatesByDrawback(candidates []scoredCandidate, rule DrawbackRule, preMoveRack string, bd *board.GameBoard, poolSizeBefore int) []scoredCandidate {
	if rule.Type == "excludeTopNCandidates" {
		ranked := make([]scoredCandidate, 0, len(candidates))
		for _, c := range candidates {
			if !c.isExchange {
				ranked = append(ranked, c)
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].total > ranked[j].total })
		excluded := ranked
		if len(excluded) > rule.N {
			excluded = excluded[:rule.N]
		}
		isExcluded := func(c *scoredCandidate) bool {
			for i := range excluded {
				if sameCandidate(c, &excluded[i]) {
					return true
				}
			}
			return false
		}

		filtered := make([]scoredCandidate, 0, len(candidates))
		for i := range candidates {
			if candidates[i].isExchange || !isExcluded(&candidates[i]) {
				filtered = append(filtered, candidates[i])
			}
		}
		return filtered
	}

	// Category C ("Forcing"): evaluated backwards from every other rule
	// here (see DrawbackRule.Forcing's own comment). Collect the word-plays
	// that qualify (exchanges are never eligible to satisfy a forcing rule -
	// the player is required to make the qualifying PLAY, not sidestep it
	// with an exchange); if any exist, the whole list collapses to just
	// those, exchanges included in the result set. If none qualify, the
	// rule doesn't bite this turn - return candidates unchanged.
	if rule.Forcing {
		qualifying := make([]scoredCandidate, 0, len(candidates))
		for i := range candidates {
			c := &candidates[i]
			if !c.isExchange && evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore) {
				qualifying = append(qualifying, *c)
			}
		}
		if len(qualifying) > 0 {
			return qualifying
		}
		return candidates
	}

	// See DrawbackRule.AppliesToExchanges' own comment - exchanges are
	// exempt from every drawback by default (right for a rule about the
	// played WORD), but a small number of rules are about the LEAVE, which
	// an exchange produces too, so they opt out of that exemption instead
	// of automatically letting every exchange through.
	if rule.AppliesToExchanges {
		filtered := make([]scoredCandidate, 0, len(candidates))
		for i := range candidates {
			c := &candidates[i]
			if evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore) {
				filtered = append(filtered, *c)
			}
		}
		return filtered
	}

	filtered := make([]scoredCandidate, 0, len(candidates))
	for i := range candidates {
		c := &candidates[i]
		if c.isExchange || evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore) {
			filtered = append(filtered, *c)
		}
	}
	return filtered
}
