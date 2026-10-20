package main

import (
	"fmt"
	"math/rand"
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
// still stateless - no memory needed beyond this one move), C (same
// stateless shape as B, evaluated backwards - see DrawbackRule.Forcing),
// and 6 of D's 10 (stateful - needs to know about turns other than this
// one, or - Quota's own case - a value rolled once and held for the whole
// turn) are implemented here. D's own remaining 4 are either blocked on a
// real design decision (see the ledger) or just need a quick wording
// confirm, not built yet. E (scoring override) and F (deferred lose-
// conditions) need machinery this file doesn't have at all.
//
// D's own "state" turned out not to need any new persisted-memory
// machinery - lastWordPlayByPlayer/allWordPlaysByPlayer just query
// simulateOneGame's own turns history, which already exists for the
// turn-by-turn viewer output. That same history (moveHistory) already
// exists client-side in whiffers' gameStore for a live Play-mode game too,
// so this same querying approach carries over to that side for free
// whenever these get wired up there - nothing here is Sandbox-specific.
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

	// ExemptCenter (forbiddenSquareTypes): the center star never counts as
	// forbidden, so the opening move stays possible (Double Trouble - the
	// star is a DWS).
	ExemptCenter bool `json:"exemptCenter,omitempty"`

	// MinLength (loseIfFewLongPlays): the main-word length that counts as a
	// "long" play (We All Floater Down Here: 8).
	MinLength int `json:"minLength,omitempty"`

	// Row/Col (loseIfNoTileOnSquareByTurn): the square you must cover.
	Row int `json:"row,omitempty"`
	Col int `json:"col,omitempty"`
	// Rows (loseIfFewTilesInRows): rows that each need Value of your tiles.
	Rows []int `json:"rows,omitempty"`

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
// primitive), B (new stateless primitive), C (forcing), and 6 of D's 10
// (stateful) so far. Kept in the same order as the design doc for easy
// comparison (Quota and Well Aged, the newest additions, are the
// exception - appended at the end rather than resorted into their own
// numeric position, so they don't shift every ID below them in a diff).
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
		Description: "Can't play a tile on a DWS or DLS square (the center star is fine).",
		Rule:        DrawbackRule{Type: "forbiddenSquareTypes", Types: []string{"DWS", "DLS"}, ExemptCenter: true}},
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

	// -- Category D: stateful (these first 4, plus Quota and Well Aged
	// appended at the very end of this slice below - see the ledger for
	// the other 4, which are either genuinely blocked on a design decision
	// or just need a quick wording confirm - not implemented yet). Queried
	// fresh from the game's own turn history each time (see lastWordPlayByPlayer/
	// allWordPlaysByPlayer) rather than tracked as separate persisted
	// memory - the history already exists for the turn-by-turn viewer, so
	// there was nothing new to build to support these four specifically.
	{ID: 13, Name: "Crisscross", NameSource: "claude",
		Description: "Must alternate between playing vertically and horizontally.",
		Rule:        DrawbackRule{Type: "mustAlternateDirection"}},
	{ID: 18, Name: "Tunnel Vision", NameSource: "friend",
		Description: "Must play through your opponent's immediately preceding play.",
		Rule:        DrawbackRule{Type: "mustPlayThroughOpponentLastMove"}},
	{ID: 24, Name: "Oneupsmanship", NameSource: "friend",
		Description: "Must use more tiles than your opponent's last play - if you can't, exchange or pass.",
		// Filtered in filterCandidatesByDrawback's own special case: a
		// qualifying play is required when one exists; otherwise only
		// exchanges (or a pass) remain.
		Rule: DrawbackRule{Type: "exceedOpponentLastPlayTileCount"}},
	{ID: 23, Name: "Alphabet Soup", NameSource: "claude",
		Description: "Every word you play must start with a letter you haven't started a word with before.",
		Rule:        DrawbackRule{Type: "uniqueStartingLetterPerWord"}},
	{ID: 14, Name: "Quota", NameSource: "claude",
		Description: "Randomly assigned 2 numbers 1-7 each turn; must use exactly that many tiles (exchanges count).",
		// No AppliesToExchanges/other fields - this one's rolled and checked
		// entirely in filterCandidatesByDrawback's own special case (same
		// precedent as excludeTopNCandidates), not through evaluateDrawback,
		// since the 2 numbers have to be the SAME for every candidate in one
		// turn's list, not re-rolled per candidate.
		Rule: DrawbackRule{Type: "randomTileCountQuota"}},
	{ID: 39, Name: "Well Aged", NameSource: "friend",
		Description: "Can't play a tile you drew after your last turn.",
		Rule:        DrawbackRule{Type: "cannotPlayFreshlyDrawnTile"}},
	{ID: 33, Name: "Here For A Good Time", NameSource: "friend",
		Description: "Only 40 tiles may ever leave your rack, for the whole game (exchanges count).",
		// AppliesToExchanges: an exchange's tiles leave the rack too, so it
		// has to fit the remaining allowance like a play does.
		Rule: DrawbackRule{Type: "maxTilesLeaveRack", MaxTiles: 40, AppliesToExchanges: true}},
	{ID: 7, Name: "Shifting Sands", NameSource: "claude",
		Description: "50% of squares are off-limits, re-randomized every turn (never the center star).",
		// Rolled in filterCandidatesByDrawback's own special case - one
		// shared roll per turn, like Quota. Value is the off-limits share.
		Rule: DrawbackRule{Type: "randomBlockedSquares", Value: 0.5}},
	// -- Lose conditions: breaking one ends the game on the spot - the
	// player who broke it loses, scored 0, and the winner keeps their score
	// (see isLoseRule / drawbackLossAfterTurn and simulateOneGame).
	{ID: 8, Name: "Scoring Never Stops", NameSource: "friend",
		Description: "Score under 25 on a play (15 once the bag is empty) and you lose.",
		Rule:        DrawbackRule{Type: "loseIfPlayScoreBelow", Value: 25, ValueWhenBagEmpty: 15}},
	{ID: 34, Name: "Time Crunch", NameSource: "friend",
		Description: "Game runs past 25 turns and you lose.",
		Rule:        DrawbackRule{Type: "loseIfPastTurnLimit", Value: 25}},
	{ID: 10, Name: "Turnabout Board", NameSource: "friend",
		Description: "You can't be winning before turn 16.",
		Rule:        DrawbackRule{Type: "loseIfLeadingBeforeTurn", Value: 16}},
	{ID: 9, Name: "Vervain", NameSource: "friend",
		Description: "Your opponent plays a bingo and you lose.",
		Rule:        DrawbackRule{Type: "loseIfOpponentBingos"}},
	{ID: 21, Name: "Triple Threat", NameSource: "claude",
		Description: "Your opponent covers a triple word score and you lose.",
		Rule:        DrawbackRule{Type: "loseIfOpponentCoversSquare", Types: []string{"TWS"}}},
	{ID: 12, Name: "We All Floater Down Here", NameSource: "friend",
		Description: "Fewer than 3 eight-letter plays (bingo or not) by game end and you lose.",
		Rule:        DrawbackRule{Type: "loseIfFewLongPlays", Value: 3, MinLength: 8}},
	{ID: 20, Name: "Corner Rush", NameSource: "claude",
		Description: "No tile of yours on A1 by turn 14 and you lose.",
		Rule:        DrawbackRule{Type: "loseIfNoTileOnSquareByTurn", Row: 0, Col: 0, Value: 14}},
	{ID: 19, Name: "Border Patrol", NameSource: "claude",
		Description: "Fewer than 5 of your tiles in row 1, or in row 15, by game end and you lose.",
		Rule:        DrawbackRule{Type: "loseIfFewTilesInRows", Rows: []int{0, 14}, Value: 5}},
	{ID: 38, Name: "Claustrophobia", NameSource: "friend",
		Description: "A random 3x3 zone touching the board's edge is set at game start; whoever plays inside it loses.",
		// The zone is rolled per game in simulateOneGame (rollClaustroZone)
		// and applies to BOTH players - see claustroLoss.
		Rule: DrawbackRule{Type: "loseIfPlayInZone"}},
	{ID: 36, Name: "Zeroed In", NameSource: "friend",
		Description: "Must exchange or pass at least 6 times over the course of the game.",
		// Lose rule, judged at game end: exchanges and passes both count.
		Rule: DrawbackRule{Type: "loseIfFewExchangesOrPasses", Value: 6}},
	{ID: 25, Name: "I'll Take That", NameSource: "friend",
		Description: "If your opponent's play opens a bonus square you can reach, you must play there.",
		// Forcing: "opened" = an empty premium square right next to a tile
		// the opponent placed on their immediately preceding turn.
		Rule: DrawbackRule{Type: "mustCoverOpenedPremium", Forcing: true}},
	{ID: 17, Name: "Nerf War", NameSource: "friend",
		Description: "All of your tiles score 1 point, hidden from your opponent until the game ends.",
		// Rescored in filterCandidatesByDrawback's own special case: tiles
		// you place count 1 (blanks stay 0); tiles already on the board keep
		// their value; premiums and the bingo bonus still apply.
		Rule: DrawbackRule{Type: "nerfTileValues"}},
}

// isLoseRule reports whether a drawback is a lose condition (breaking it
// ends the game as a loss) rather than a restriction on which moves count.
func isLoseRule(rule DrawbackRule) bool {
	switch rule.Type {
	case "loseIfPlayScoreBelow", "loseIfPastTurnLimit", "loseIfLeadingBeforeTurn",
		"loseIfOpponentBingos", "loseIfOpponentCoversSquare", "loseIfFewLongPlays",
		"loseIfNoTileOnSquareByTurn", "loseIfFewTilesInRows", "loseIfPlayInZone",
		"loseIfFewExchangesOrPasses":
		return true
	}
	return false
}

// Turn counts here are the game's turns across both players - every play,
// exchange and pass is one turn - so len(turns) once a turn has been added.

// lastScores is each player's running total as of the turns so far.
func lastScores(turns []SimTurn) (int, int) {
	s1, s2 := 0, 0
	for _, t := range turns {
		if t.Player == 1 {
			s1 = t.RunningTotal
		} else {
			s2 = t.RunningTotal
		}
	}
	return s1, s2
}

// losesWith reports whether the holder making candidate c this turn would
// break lose rule `rule` - used so a bot avoids a losing move when it has a
// safe one. Only rules the holder's own move can break are judged here (an
// opponent's bingo, say, isn't the holder's to avoid).
func losesWith(rule DrawbackRule, c *scoredCandidate, poolSizeBefore int, turns []SimTurn, holder int) (bool, string) {
	switch rule.Type {
	case "loseIfPlayScoreBelow":
		if c.isExchange {
			return false, ""
		}
		return playScoreBelow(rule, c.detailed.Score, poolSizeBefore)
	case "loseIfNoTileOnSquareByTurn":
		turnNumber := len(turns) + 1
		// Your last own turn at or before the deadline (turns alternate).
		lastChance := float64(turnNumber) <= rule.Value && float64(turnNumber+2) > rule.Value
		if !lastChance || holderCoveredSquare(turns, holder, rule.Row, rule.Col) {
			return false, ""
		}
		if !c.isExchange {
			for _, t := range c.detailed.Tiles {
				if t.IsNew && t.Row == rule.Row && t.Col == rule.Col {
					return false, ""
				}
			}
		}
		return true, "didn't cover the square in time"
	case "loseIfLeadingBeforeTurn":
		turnNumber := len(turns) + 1
		if float64(turnNumber) >= rule.Value {
			return false, ""
		}
		s1, s2 := lastScores(turns)
		mine, theirs := s1, s2
		if holder == 2 {
			mine, theirs = s2, s1
		}
		if !c.isExchange {
			mine += c.detailed.Score
		}
		if mine > theirs {
			return true, fmt.Sprintf("ahead %d-%d after turn %d", mine, theirs, turnNumber)
		}
	}
	return false, ""
}

// drawbackLossAfterTurn reports whether the turn just played (the last entry
// of turns, made by turn.Player) broke `holder`'s lose rule, and why.
// gameEnding: this turn also ended the game normally (went out / six
// scoreless turns) - Time Crunch only bites if the game would go on.
func drawbackLossAfterTurn(rule DrawbackRule, turn SimTurn, poolSizeBefore int, turns []SimTurn, holder int, gameEnding bool) (bool, string) {
	byHolder := turn.Player == holder
	switch rule.Type {
	case "loseIfPlayScoreBelow":
		if !byHolder || turn.Type != "play" {
			return false, ""
		}
		return playScoreBelow(rule, turn.Score, poolSizeBefore)
	case "loseIfPastTurnLimit":
		if !gameEnding && float64(len(turns)) >= rule.Value {
			return true, fmt.Sprintf("the game reached turn %d", len(turns))
		}
	case "loseIfLeadingBeforeTurn":
		if float64(len(turns)) >= rule.Value {
			return false, ""
		}
		s1, s2 := lastScores(turns)
		mine, theirs := s1, s2
		if holder == 2 {
			mine, theirs = s2, s1
		}
		if mine > theirs {
			return true, fmt.Sprintf("ahead %d-%d after turn %d", mine, theirs, len(turns))
		}
	case "loseIfNoTileOnSquareByTurn":
		if float64(len(turns)) == rule.Value && !holderCoveredSquare(turns, holder, rule.Row, rule.Col) {
			return true, fmt.Sprintf("no tile of yours on %s by turn %d", squareName(rule.Row, rule.Col), len(turns))
		}
	case "loseIfOpponentBingos":
		if !byHolder && turn.Type == "play" && newTileCountOf(turn.Tiles) == 7 {
			return true, "the opponent played a bingo"
		}
	case "loseIfOpponentCoversSquare":
		if byHolder || turn.Type != "play" {
			return false, ""
		}
		for _, t := range turn.Tiles {
			if t.IsNew && containsString(rule.Types, squareTypeAt(t.Row, t.Col)) {
				return true, "the opponent covered a " + squareTypeAt(t.Row, t.Col)
			}
		}
	}
	return false, ""
}

// drawbackLossAtGameEnd reports whether `holder`'s lose rule is broken once
// the game is over (We All Floater Down Here), and why.
func drawbackLossAtGameEnd(rule DrawbackRule, turns []SimTurn, holder int) (bool, string) {
	if rule.Type == "loseIfFewExchangesOrPasses" {
		count := 0
		for _, t := range turns {
			if t.Player == holder && (t.Type == "exchange" || t.Type == "pass") {
				count++
			}
		}
		if float64(count) < rule.Value {
			return true, fmt.Sprintf("only %d exchanges/passes by game end", count)
		}
		return false, ""
	}
	if rule.Type == "loseIfFewTilesInRows" {
		for _, row := range rule.Rows {
			count := 0
			for _, t := range turns {
				if t.Player != holder || t.Type != "play" {
					continue
				}
				for _, tile := range t.Tiles {
					if tile.IsNew && tile.Row == row {
						count++
					}
				}
			}
			if float64(count) < rule.Value {
				return true, fmt.Sprintf("only %d of your tiles in row %d by game end", count, row+1)
			}
		}
		return false, ""
	}
	if rule.Type != "loseIfFewLongPlays" {
		return false, ""
	}
	count := 0
	for _, t := range turns {
		if t.Player == holder && t.Type == "play" && len(t.Tiles) == rule.MinLength {
			count++
		}
	}
	if float64(count) < rule.Value {
		return true, fmt.Sprintf("only %d %d-letter play(s) by game end", count, rule.MinLength)
	}
	return false, ""
}

// openedPremiumSquares: the empty premium squares right next to a tile the
// opponent placed on the immediately preceding turn (I'll Take That). None
// when that turn wasn't the opponent's play.
func openedPremiumSquares(turns []SimTurn, player int, bd *board.GameBoard) map[rcPos]bool {
	opened := map[rcPos]bool{}
	if len(turns) == 0 {
		return opened
	}
	last := turns[len(turns)-1]
	if last.Player == player || last.Type != "play" {
		return opened
	}
	for _, t := range last.Tiles {
		if !t.IsNew {
			continue
		}
		for _, d := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			r, c := t.Row+d[0], t.Col+d[1]
			if _, ok := premiumTypeAt(r, c, bd); ok {
				opened[rcPos{r, c}] = true
			}
		}
	}
	return opened
}

// squareMultipliers is (letter, word) multiplier for an EMPTY square.
func squareMultipliers(r, c int, bd *board.GameBoard) (int, int) {
	switch t, _ := premiumTypeAt(r, c, bd); t {
	case "DLS":
		return 2, 1
	case "TLS":
		return 3, 1
	case "DWS":
		return 1, 2
	case "TWS":
		return 1, 3
	}
	return 1, 1
}

// nerfedScore is candidate c's score with the tiles it places worth 1 each
// (Nerf War): the normal score minus what each new tile's face value above
// 1 added - in the main word (x its letter multiplier x the main word's
// multiplier) and in its cross word if it forms one (x its letter
// multiplier x its own square's word multiplier). Blanks are 0 either way.
// bd is the board before the move, so the new tiles' squares are empty.
func nerfedScore(c *scoredCandidate, bd *board.GameBoard) int {
	mainWordMult := 1
	for _, t := range c.detailed.Tiles {
		if t.IsNew {
			_, wm := squareMultipliers(t.Row, t.Col, bd)
			mainWordMult *= wm
		}
	}
	diff := 0
	for _, t := range c.detailed.Tiles {
		if !t.IsNew || t.IsBlank {
			continue
		}
		extra := tileLetterValue(t) - 1
		if extra <= 0 {
			continue
		}
		lm, wm := squareMultipliers(t.Row, t.Col, bd)
		diff += extra * lm * mainWordMult
		var crossBefore, crossAfter bool
		if c.detailed.Direction == "down" {
			crossBefore = t.Col > 0 && bd.GetLetter(t.Row, t.Col-1) != 0
			crossAfter = t.Col < 14 && bd.GetLetter(t.Row, t.Col+1) != 0
		} else {
			crossBefore = t.Row > 0 && bd.GetLetter(t.Row-1, t.Col) != 0
			crossAfter = t.Row < 14 && bd.GetLetter(t.Row+1, t.Col) != 0
		}
		if crossBefore || crossAfter {
			diff += extra * lm * wm
		}
	}
	return c.detailed.Score - diff
}

// holderCoveredSquare: did holder place a tile on (row, col) in any play?
func holderCoveredSquare(turns []SimTurn, holder, row, col int) bool {
	for _, t := range turns {
		if t.Player != holder || t.Type != "play" {
			continue
		}
		for _, tile := range t.Tiles {
			if tile.IsNew && tile.Row == row && tile.Col == col {
				return true
			}
		}
	}
	return false
}

// squareName is board notation for (row, col), e.g. (0, 0) -> "A1".
func squareName(row, col int) string {
	return fmt.Sprintf("%c%d", 'A'+col, row+1)
}

// claustroZone is Claustrophobia's 3x3 zone, by its top-left square, and
// the player whose drawback it is. Only that player knows where it is (it
// would give the drawback away), but it applies to both.
type claustroZone struct {
	Row    int `json:"row"`
	Col    int `json:"col"`
	Holder int `json:"holder"`
}

// rollClaustroZone picks a random 3x3 zone touching the board's edge.
func rollClaustroZone(holder int) claustroZone {
	var options []claustroZone
	for r := 0; r <= 12; r++ {
		for c := 0; c <= 12; c++ {
			if r == 0 || r == 12 || c == 0 || c == 12 {
				options = append(options, claustroZone{Row: r, Col: c})
			}
		}
	}
	z := options[rand.Intn(len(options))]
	z.Holder = holder
	return z
}

// placesInZone: does a play's new tile land inside any of the zones?
func placesInZone(tiles []MoveTile, zones []claustroZone) bool {
	for _, t := range tiles {
		if !t.IsNew {
			continue
		}
		for _, z := range zones {
			if t.Row >= z.Row && t.Row < z.Row+3 && t.Col >= z.Col && t.Col < z.Col+3 {
				return true
			}
		}
	}
	return false
}

func newTileCountOf(tiles []MoveTile) int {
	n := 0
	for _, t := range tiles {
		if t.IsNew {
			n++
		}
	}
	return n
}

// squareTypeAt is the board layout's premium type at (r, c) - "TWS", "DWS",
// "TLS", "DLS" or "" - whether or not a tile now sits there (premiumTypeAt
// answers "" for an occupied square).
func squareTypeAt(r, c int) string {
	pos := rcPos{r, c}
	switch {
	case twsSquares[pos]:
		return "TWS"
	case dwsSquares[pos]:
		return "DWS"
	case tlsSquares[pos]:
		return "TLS"
	case dlsSquares[pos]:
		return "DLS"
	}
	return ""
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Scoring Never Stops: a play scoring under Value (ValueWhenBagEmpty once
// the bag is empty) loses. Exchanges and passes aren't plays.
func playScoreBelow(rule DrawbackRule, score, poolSizeBefore int) (bool, string) {
	threshold := rule.Value
	if poolSizeBefore == 0 && rule.ValueWhenBagEmpty > 0 {
		threshold = rule.ValueWhenBagEmpty
	}
	if float64(score) < threshold {
		return true, fmt.Sprintf("scored %d, under %d", score, int(threshold))
	}
	return false, ""
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
// poolParityAfterMove and Quota (randomTileCountQuota) are the rules that
// need this distinction today.
func drawnTileCount(c *scoredCandidate) int {
	if c.isExchange {
		// Pre-existing bug, fixed here: an exchange scoredCandidate never
		// has .detailed set at all (see its own construction - only
		// isExchange/exchangeTiles/leave/total are populated), so reading
		// c.detailed.Tiles panicked with a nil pointer dereference on ANY
		// exchange candidate - simply never hit in practice until Quota
		// became the first caller to run this against an UNFILTERED
		// candidate list (every other caller reaches this only through
		// AppliesToExchanges, and apparently never actually drew a real
		// exchange candidate while testing). exchangeTiles is the right
		// field - its own length IS how many tiles this exchange draws
		// replacements for.
		return len(c.exchangeTiles)
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

// firstTileOfTurn is firstTileOf's own twin for an already-committed
// SimTurn instead of a live scoredCandidate - Category D's rules need to
// ask this same question about PAST turns (Alphabet Soup: what letter did
// I start my last few words with), not just the candidate under
// evaluation right now.
func firstTileOfTurn(t SimTurn) MoveTile {
	tiles := t.Tiles
	first := tiles[0]
	for _, tile := range tiles[1:] {
		if t.Direction == "down" {
			if tile.Row < first.Row {
				first = tile
			}
		} else if tile.Col < first.Col {
			first = tile
		}
	}
	return first
}

// lastWordPlayByPlayer scans the game's turn history backward for the most
// recent "play" entry (a real word play - Pass/Exchange don't count as "a
// play" for any of Category D's rules, which all read the word the same
// way normal Scrabble terminology does) by the given player. Returns nil
// when that player hasn't played a word yet - every Category D rule here
// treats "no prior play to compare against" as "nothing to enforce yet,"
// not as a failure.
func lastWordPlayByPlayer(turns []SimTurn, player int) *SimTurn {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Player == player && turns[i].Type == "play" {
			return &turns[i]
		}
	}
	return nil
}

// lastTurnByPlayer is lastWordPlayByPlayer without the "play" filter - the
// player's most recent turn of any kind (play, exchange or pass). Well Aged
// ages tiles at every turn, not just word plays.
func lastTurnByPlayer(turns []SimTurn, player int) *SimTurn {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Player == player {
			return &turns[i]
		}
	}
	return nil
}

// tilesLeftRackTotal is how many tiles have left player's rack so far this
// game: every play's new tiles plus every exchange's returned tiles (Here
// For A Good Time).
func tilesLeftRackTotal(turns []SimTurn, player int) int {
	total := 0
	for _, t := range turns {
		if t.Player != player {
			continue
		}
		switch t.Type {
		case "play":
			for _, tile := range t.Tiles {
				if tile.IsNew {
					total++
				}
			}
		case "exchange":
			total += len([]rune(t.TilesExchanged))
		}
	}
	return total
}

// allWordPlaysByPlayer is lastWordPlayByPlayer's own twin for rules that
// need the WHOLE history, not just the most recent entry - Alphabet Soup's
// "a letter you haven't started a word with before" has to check every
// past word, not only the last one.
func allWordPlaysByPlayer(turns []SimTurn, player int) []SimTurn {
	var result []SimTurn
	for _, t := range turns {
		if t.Player == player && t.Type == "play" {
			result = append(result, t)
		}
	}
	return result
}

// newlyUsedLetters is Well Aged's own need: the letters a PAST turn's play
// actually placed (blanks as "?"), from its own []MoveTile - not reused
// from endgame_solver_exact.go's near-identical newlyUsedLettersExact,
// which takes a *DetailedMove specifically rather than a bare []MoveTile,
// and lives in a file this one has no reason to depend on.
func newlyUsedLetters(tiles []MoveTile) string {
	var used []rune
	for _, t := range tiles {
		if !t.IsNew {
			continue
		}
		if t.IsBlank {
			used = append(used, '?')
		} else {
			used = append(used, []rune(t.Letter)[0])
		}
	}
	return string(used)
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

// wordLengthsFormed is the length of every word candidate c forms: its main
// word, plus the cross word through each NEW tile that has neighbours on
// the perpendicular axis (walked on the board as it stood before this
// move, same convention as countWordsFormed).
func wordLengthsFormed(c *scoredCandidate, bd *board.GameBoard) []int {
	lengths := []int{len(c.detailed.Tiles)}
	for _, t := range c.detailed.Tiles {
		if !t.IsNew {
			continue
		}
		length := 1
		if c.detailed.Direction == "down" {
			for col := t.Col - 1; col >= 0 && bd.GetLetter(t.Row, col) != 0; col-- {
				length++
			}
			for col := t.Col + 1; col <= 14 && bd.GetLetter(t.Row, col) != 0; col++ {
				length++
			}
		} else {
			for row := t.Row - 1; row >= 0 && bd.GetLetter(row, t.Col) != 0; row-- {
				length++
			}
			for row := t.Row + 1; row <= 14 && bd.GetLetter(row, t.Col) != 0; row++ {
				length++
			}
		}
		if length > 1 {
			lengths = append(lengths, length)
		}
	}
	return lengths
}

// evaluateDrawback reports whether candidate c is still allowed under
// rule, given the board as it stood before this move, the acting player's
// rack before this move, how many tiles remain in the bag before this
// move, and (Category D only) the game's own turn history so far plus
// which player (1 or 2) is the one this candidate belongs to - the same
// role Viewer's own moveHistory scans (getOpponentTotalAtMove and
// friends) play in the frontend, just queried here instead. Only ever
// called for word-play candidates - exchanges are always left alone by
// every drawback here, the same way BingoAversion leaves them alone -
// except where AppliesToExchanges opts a rule out of that (see its own
// comment).
func evaluateDrawback(rule DrawbackRule, c *scoredCandidate, preMoveRack string, bd *board.GameBoard, poolSizeBefore int, turns []SimTurn, currentPlayer int) bool {
	switch rule.Type {
	// Every word the play forms must pass - the main word and each cross
	// word. Checking only the main word let DE through beside a column
	// that it turned into ORATED (Hippopotomonstrosesquipedaliophobia).
	case "wordLength":
		for _, length := range wordLengthsFormed(c, bd) {
			if !compareFloat(float64(length), rule.Comparator, rule.Value) {
				return false
			}
		}
		return true

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
			if rule.ExemptCenter && t.Row == 7 && t.Col == 7 {
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

	// Category D ("Stateful"): unlike A/B/C, these need to know something
	// about turns OTHER than this one - but per drawbacks.go's own header
	// comment on the state-threading structure, that's just the game's own
	// turn history (turns), already built up by simulateOneGame's loop for
	// its turn-by-turn viewer output - queried fresh each time via
	// lastWordPlayByPlayer/allWordPlaysByPlayer rather than tracked as a
	// separate piece of persisted memory. "No prior play yet to compare
	// against" is treated as "nothing to enforce yet" (return true), not a
	// failure, for all four of these - early game turns shouldn't be
	// unplayable just because there's no history yet.

	// Crisscross: same 1-tile-play exemption Fear of Heights/Vertie already
	// use (a play that's just one tile long has no real direction).
	case "mustAlternateDirection":
		if len(c.detailed.Tiles) <= 1 {
			return true
		}
		last := lastWordPlayByPlayer(turns, currentPlayer)
		if last == nil {
			return true
		}
		return c.detailed.Direction != last.Direction

	// Tunnel Vision: "play through" means the candidate's own resulting
	// word (its FULL tile span, not just the newly-placed ones - a play
	// can run through the opponent's tile without placing anything new
	// exactly there) shares at least one square with a tile the opponent
	// placed NEW on their immediately preceding play.
	case "mustPlayThroughOpponentLastMove":
		opponent := 1
		if currentPlayer == 1 {
			opponent = 2
		}
		last := lastWordPlayByPlayer(turns, opponent)
		if last == nil {
			return true
		}
		for _, t := range c.detailed.Tiles {
			for _, lt := range last.Tiles {
				if lt.IsNew && lt.Row == t.Row && lt.Col == t.Col {
					return true
				}
			}
		}
		return false

	// Oneupsmanship: whether one word play uses more tiles than the
	// opponent's last play. filterCandidatesByDrawback's own special case
	// decides what happens to exchanges (allowed only when no play
	// qualifies). The isExchange check stays as a guard: an exchange
	// candidate's .detailed is nil, so newTileCount(c) would panic on one.
	case "exceedOpponentLastPlayTileCount":
		if c.isExchange {
			return false
		}
		opponent := 1
		if currentPlayer == 1 {
			opponent = 2
		}
		last := lastWordPlayByPlayer(turns, opponent)
		if last == nil {
			return true
		}
		lastCount := 0
		for _, t := range last.Tiles {
			if t.IsNew {
				lastCount++
			}
		}
		return newTileCount(c) > lastCount

	// Alphabet Soup: a blank's own played Letter (not its blank-ness) is
	// what counts as "the letter you started with" - once placed, a blank
	// IS that letter for every other purpose in this codebase (board
	// storage, scoring), and there's no reason "which letter you started
	// with" should be the one exception.
	case "uniqueStartingLetterPerWord":
		first := firstTileOf(c)
		for _, t := range allWordPlaysByPlayer(turns, currentPlayer) {
			if firstTileOfTurn(t).Letter == first.Letter {
				return false
			}
		}
		return true

	// Well Aged (#39): a tile counts as "aged" if it was already in hand
	// at the player's own last TURN of any kind - that turn's RackBefore,
	// minus whatever left the rack on it (a play's placed tiles, an
	// exchange's returned tiles, nothing for a pass). Anything beyond those
	// per-letter counts was drawn since then, and so is forbidden now.
	// Exchanges and passes count as turns on purpose: counting only word
	// plays (the original version) let a player stuck with unplayable aged
	// tiles - e.g. after a bingo, which leaves none - exchange forever,
	// since nothing ever aged again. No prior turn yet (the first turn)
	// means everything already in hand is aged by definition. Exchanges
	// themselves stay allowed (no AppliesToExchanges on this rule) - this
	// is about which tiles get PLACED, not which get discarded.
	// Here For A Good Time: this move's tiles (placed or exchanged) must fit
	// in what's left of the player's whole-game allowance.
	case "maxTilesLeaveRack":
		return tilesLeftRackTotal(turns, currentPlayer)+drawnTileCount(c) <= rule.MaxTiles

	// I'll Take That (forcing): does this play put a new tile on a square
	// the opponent's last play opened?
	case "mustCoverOpenedPremium":
		opened := openedPremiumSquares(turns, currentPlayer, bd)
		for _, t := range c.detailed.Tiles {
			if t.IsNew && opened[rcPos{t.Row, t.Col}] {
				return true
			}
		}
		return false

	case "cannotPlayFreshlyDrawnTile":
		last := lastTurnByPlayer(turns, currentPlayer)
		if last == nil {
			return true
		}
		left := ""
		switch last.Type {
		case "play":
			left = newlyUsedLetters(last.Tiles)
		case "exchange":
			left = last.TilesExchanged
		}
		agedRack := removeRackFromPool(last.RackBefore, left)
		for _, t := range c.detailed.Tiles {
			if !t.IsNew {
				continue
			}
			letter := "?"
			if !t.IsBlank {
				letter = t.Letter
			}
			idx := strings.Index(agedRack, letter)
			if idx == -1 {
				return false
			}
			agedRack = agedRack[:idx] + agedRack[idx+1:]
		}
		return true

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
// excludeTopNCandidates (needs the word plays' relative rank rather than a
// per-candidate check) and randomTileCountQuota (needs ONE shared random
// roll for the whole turn, not a fresh one per candidate), each handled as
// their own pass instead.
//
// The second return value is a short, human-readable debug note - empty
// for every rule except randomTileCountQuota, whose own 2 rolled numbers
// are otherwise genuinely unobservable from outside this function (used
// once to filter, then gone). Surfaced as SimTurn.DrawbackDebug so a real
// game log can be checked by eye ("needed 3 or 6 tiles, played 3") instead
// of having to trust the filter blindly - see simulate.go's own call site.
func filterCandidatesByDrawback(candidates []scoredCandidate, rule DrawbackRule, preMoveRack string, bd *board.GameBoard, poolSizeBefore int, turns []SimTurn, currentPlayer int) ([]scoredCandidate, string) {
	// Nerf War: nothing is forbidden - every word play is rescored with the
	// player's placed tiles worth 1, so the bot picks (and scores) by its
	// real points. Copies each DetailedMove so the unfiltered snapshot the
	// caller keeps for comparison isn't changed.
	if rule.Type == "nerfTileValues" {
		rescored := make([]scoredCandidate, len(candidates))
		for i, c := range candidates {
			if !c.isExchange {
				nerfed := nerfedScore(&c, bd)
				delta := float64(c.detailed.Score - nerfed)
				d := *c.detailed
				d.Score = nerfed
				c.detailed = &d
				c.total -= delta
				c.baselineTotal -= delta
			}
			rescored[i] = c
		}
		return rescored, ""
	}

	// Lose conditions don't forbid moves - a bot just avoids the losing ones
	// when it has a safe option; with none, it makes its best move and loses.
	if isLoseRule(rule) {
		safe := make([]scoredCandidate, 0, len(candidates))
		for i := range candidates {
			if lost, _ := losesWith(rule, &candidates[i], poolSizeBefore, turns, currentPlayer); !lost {
				safe = append(safe, candidates[i])
			}
		}
		if len(safe) > 0 {
			return safe, ""
		}
		return candidates, ""
	}

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
		return filtered, ""
	}

	// Quota (#14): the 2 numbers are rolled ONCE per call (= once per real
	// turn, since this function is called once per turn, not once per
	// candidate) - evaluateDrawback runs once PER CANDIDATE, so rolling
	// there would hand every candidate in the same turn's list a different
	// pair instead of the one shared quota a real turn actually has. Same
	// special-casing precedent as excludeTopNCandidates above. Measures
	// drawnTileCount, not newTileCount, because the ledger's own wording
	// ("exchanges count") means an exchange has to satisfy the same quota a
	// word play does, not get the automatic pass most drawbacks give it -
	// so this never checks c.isExchange at all, word plays and exchanges
	// are both just measured by however many tiles they actually move.
	// Oneupsmanship: a play using more tiles than the opponent's last one is
	// required whenever one exists. When none does, the player may exchange
	// (or pass, which is what an empty list becomes) - word plays stay out.
	if rule.Type == "exceedOpponentLastPlayTileCount" {
		qualifying := make([]scoredCandidate, 0, len(candidates))
		exchanges := make([]scoredCandidate, 0)
		for i := range candidates {
			c := &candidates[i]
			if c.isExchange {
				exchanges = append(exchanges, *c)
			} else if evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore, turns, currentPlayer) {
				qualifying = append(qualifying, *c)
			}
		}
		if len(qualifying) > 0 {
			return qualifying, ""
		}
		return exchanges, ""
	}

	// Shifting Sands: this turn, a random share (rule.Value) of the squares
	// other than the center star are off-limits - a word play can't put a
	// new tile on one. Rolled once here so every candidate faces the same
	// squares; exchanges aren't affected.
	if rule.Type == "randomBlockedSquares" {
		const center = 7*15 + 7
		others := make([]int, 0, 224)
		for i := 0; i < 225; i++ {
			if i != center {
				others = append(others, i)
			}
		}
		rand.Shuffle(len(others), func(i, j int) { others[i], others[j] = others[j], others[i] })
		blockedCount := int(float64(len(others)) * rule.Value)
		blocked := make(map[int]bool, blockedCount)
		for _, sq := range others[:blockedCount] {
			blocked[sq] = true
		}
		filtered := make([]scoredCandidate, 0, len(candidates))
		for i := range candidates {
			c := &candidates[i]
			if c.isExchange {
				filtered = append(filtered, *c)
				continue
			}
			ok := true
			for _, t := range c.detailed.Tiles {
				if t.IsNew && blocked[t.Row*15+t.Col] {
					ok = false
					break
				}
			}
			if ok {
				filtered = append(filtered, *c)
			}
		}
		return filtered, fmt.Sprintf("Shifting Sands: %d squares off-limits this turn", blockedCount)
	}

	if rule.Type == "randomTileCountQuota" {
		n1 := rand.Intn(7) + 1
		n2 := rand.Intn(7) + 1
		filtered := make([]scoredCandidate, 0, len(candidates))
		for i := range candidates {
			n := drawnTileCount(&candidates[i])
			if n == n1 || n == n2 {
				filtered = append(filtered, candidates[i])
			}
		}
		return filtered, fmt.Sprintf("Quota: needed %d or %d tiles this turn", n1, n2)
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
			if !c.isExchange && evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore, turns, currentPlayer) {
				qualifying = append(qualifying, *c)
			}
		}
		if len(qualifying) > 0 {
			return qualifying, ""
		}
		return candidates, ""
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
			if evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore, turns, currentPlayer) {
				filtered = append(filtered, *c)
			}
		}
		return filtered, ""
	}

	filtered := make([]scoredCandidate, 0, len(candidates))
	for i := range candidates {
		c := &candidates[i]
		if c.isExchange || evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore, turns, currentPlayer) {
			filtered = append(filtered, *c)
		}
	}
	return filtered, ""
}
