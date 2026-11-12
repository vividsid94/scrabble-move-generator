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

	// MinLength (loseIfFewLongPlays): the shortest main word that counts as a
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

	ValueWhenBagEmpty float64 `json:"valueWhenBagEmpty,omitempty"` // wordsFormedCount, score

	N int `json:"n,omitempty"` // excludeTopNCandidates

	Letters  string `json:"letters,omitempty"` // restrictedTileSetScoreFloor
	MinScore int    `json:"minScore,omitempty"`
	// BlankMinScore (restrictedTileSetScoreFloor): a blank's own floor, when
	// it differs from MinScore (Feel My Power: 70).
	BlankMinScore int `json:"blankMinScore,omitempty"`

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

	// Step (Frontload): how much the score cap drops each turn (Value is
	// the first turn's cap, MinScore the floor).
	Step int `json:"step,omitempty"`
	// Highlight / Need (Peer Pressure): how many rack tiles are picked each
	// turn, and how many of them the move has to use.
	Highlight int `json:"highlight,omitempty"`
	Need      int `json:"need,omitempty"`
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
		Description: "Can't score more than 40 points on a turn.",
		Rule:        DrawbackRule{Type: "score", Comparator: "lte", Value: 40}},
	{ID: 16, Name: "Negative Nancy", NameSource: "friend",
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
		Description: "You can only play main words whose tiles add up to at least 7.",
		Rule:        DrawbackRule{Type: "tileValueSum", Comparator: "gte", Value: 7}},
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
		Description: "If you have an S or blank on your rack, you can only play one tile.",
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
	{ID: 22, Name: "Humble Beginnings", NameSource: "claude",
		Description: "Main word can't start with a tile worth more than 1 point.",
		Rule:        DrawbackRule{Type: "firstTileValueLimit", MaxValue: 1}},
	{ID: 26, Name: "Lone Wolf", NameSource: "claude",
		Description: "Your plays can only make one word - no cross-words.",
		Rule:        DrawbackRule{Type: "wordsFormedCount", Comparator: "eq", Value: 1}},
	{ID: 27, Name: "Settle For Less", NameSource: "friend",
		Description: "Can't play either of the top 2 equity plays.",
		Rule:        DrawbackRule{Type: "excludeTopNCandidates", N: 2}},
	{ID: 28, Name: "Feel My Power", NameSource: "friend",
		Description: "You can't play S, J, K, Q, X or Z unless you score at least 50, or ? unless you score at least 70.",
		Rule:        DrawbackRule{Type: "restrictedTileSetScoreFloor", Letters: "SJKQXZ?", MinScore: 50, BlankMinScore: 70}},
	{ID: 32, Name: "Parallel Play", NameSource: "friend",
		// A "must" since 2026-11-12 (was: every play forms 3+ words, 2+ with
		// the bag empty - far too punishing, and it couldn't open).
		Description: "If you can make a play that forms at least 2 words, you must - or exchange or pass.",
		Rule:        DrawbackRule{Type: "wordsFormedCount", Comparator: "gte", Value: 2, Forcing: true}},
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
		Description: "If you can play a word that starts with your rack's last-alphabetical tile (placed by you), you must - or exchange or pass.",
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
		Description: "You must place a tile on a square next to the last word played, by either player (the highlighted squares).",
		// His option C: the last word that put tiles on the board, by
		// either player - so a pass doesn't pin you to the opponent's old
		// word. See tunnelSquares.
		Rule: DrawbackRule{Type: "mustPlayNextToLastPlay"}},
	{ID: 24, Name: "Oneupsmanship", NameSource: "friend",
		Description: "Must use more tiles than your opponent's last play - if you can't, exchange or pass.",
		// Filtered in filterCandidatesByDrawback's own special case: a
		// qualifying play is required when one exists; otherwise only
		// exchanges (or a pass) remain.
		Rule: DrawbackRule{Type: "exceedOpponentLastPlayTileCount"}},
	{ID: 23, Name: "Alphabet Soup", NameSource: "claude",
		Description: "Every word you form, cross-words included, must start with a letter you haven't started a word with before.",
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
		Description: "If you scored points on your last turn, you can't play any of the tiles you just drew. Bingos are always allowed.",
		Rule:        DrawbackRule{Type: "cannotPlayFreshlyDrawnTile"}},
	{ID: 33, Name: "Here For A Good Time", NameSource: "friend",
		Description: "You can only place 40 tiles on the board, for the whole game.",
		// Counts only tiles placed on the board; exchanges are free (the
		// type name is from when they counted too).
		Rule: DrawbackRule{Type: "maxTilesLeaveRack", MaxTiles: 40}},
	{ID: 7, Name: "Shifting Sands", NameSource: "claude",
		Description: "60 squares are off-limits, re-randomized every turn (never the center star).",
		// Rolled in filterCandidatesByDrawback's own special case - one
		// shared roll per turn, like Quota. Value is how many squares (was
		// half the board, far too harsh in playtesting).
		Rule: DrawbackRule{Type: "randomBlockedSquares", Value: 60}},

	// -- New drawbacks (his list, #41-#64), batch 1: plain rules. Exchanging
	// and passing stay allowed for all of them.
	{ID: 43, Name: "Claustrophobia", NameSource: "friend",
		Description: "You can't place tiles in the outer two rows or columns.",
		Rule:        DrawbackRule{Type: "forbiddenBorder", Value: 2}},
	{ID: 45, Name: "Go Big or Go Home", NameSource: "friend",
		Description: "If you can outscore your opponent's last play, you must. If you can't, you must exchange or pass.",
		Rule:        DrawbackRule{Type: "outscoreOpponentLastPlay"}},
	{ID: 46, Name: "Triskaidekaphobia", NameSource: "friend",
		Description: "If the tiles on your rack add up to 13 points or more, you must exchange or pass.",
		Rule:        DrawbackRule{Type: "rackValueBelow", Value: 13}},
	{ID: 47, Name: "Rising Tide", NameSource: "friend",
		Description: "Your play must start on a higher row than your opponent's last play. If it can't, you must exchange or pass.",
		Rule:        DrawbackRule{Type: "startAboveOpponentLastPlay"}},
	{ID: 49, Name: "Lonerism", NameSource: "friend",
		Description: "If you have more than one of the same tile, you can't play any of them. Exchanging is allowed.",
		Rule:        DrawbackRule{Type: "noDuplicateRackTiles"}},
	{ID: 58, Name: "Megalophobia", NameSource: "friend",
		Description: "If you can score 60 or more, you must exchange or pass.",
		// Judged across the whole candidate list in filterCandidatesByDrawback.
		Rule: DrawbackRule{Type: "noPlaysIfAnyScoresAtLeast", Value: 60}},
	// -- Batch 2: counters and per-turn rolls.
	{ID: 42, Name: "Climbing the Ranks", NameSource: "friend",
		Description: "You must play 1 tile, then 2, then 3, and so on. If you can't play the right number, you drop a rank. Exchanging resets you to 1.",
		// Judged across the whole list in filterCandidatesByDrawback; the
		// rank comes from the history (climbRank). MaxTiles caps it.
		Rule: DrawbackRule{Type: "climbTileCount", MaxTiles: 7}},
	{ID: 44, Name: "Drop-Zone", NameSource: "friend",
		Description: "Each turn you're given a random quarter of the board (8x8). You must place a tile in it, or exchange or pass.",
		// The quarter is rolled per turn (drawbackExtras.Quadrant).
		Rule: DrawbackRule{Type: "randomQuadrant"}},
	{ID: 50, Name: "Frontload", NameSource: "friend",
		Description: "On your first turn you can score at most 120. Each turn after, 10 less - down to 10.",
		Rule:        DrawbackRule{Type: "shrinkingScoreCap", Value: 120, Step: 10, MinScore: 10}},
	{ID: 54, Name: "Mood Swings", NameSource: "friend",
		Description: "A 3-turn cycle: anything goes, then only one consonant, then only one vowel. Exchanging or passing moves the cycle on.",
		Rule:        DrawbackRule{Type: "moodCycle"}},
	{ID: 55, Name: "Ghostly Presence", NameSource: "friend",
		Description: "Every play you make has a 20% chance of scoring 0.",
		// Not a move rule: the 0 is rolled after the play is chosen (the
		// game server, and simulateOneGame for the Sandbox).
		Rule: DrawbackRule{Type: "ghostlyScore", Value: 0.2}},
	{ID: 56, Name: "Performance Anxiety", NameSource: "friend",
		Description: "Fewer than 5 turns scoring 40 or more by the end of the game and you lose.",
		Rule:        DrawbackRule{Type: "loseIfFewBigPlays", Value: 5, MinScore: 40}},
	{ID: 62, Name: "Flavor Of The Day", NameSource: "friend",
		Description: "Each turn one kind of bonus square is named (DLS, DWS, TLS or TWS). It's the only bonus square you can use that turn - plain squares are fine.",
		// The kind is rolled per turn (drawbackExtras.Flavor). The center
		// star is always fine, so the opening move is possible.
		Rule: DrawbackRule{Type: "randomBonusFlavor"}},
	// Peer Pressure, three variants to playtest (his #59): Highlight tiles
	// picked at random from the rack each turn (drawbackExtras.Highlighted),
	// and the move - a play or an exchange - must use Need of them.
	{ID: 100, Name: "Peer Pressure", NameSource: "friend",
		Description: "Each turn 4 random tiles on your rack are highlighted. You must play or exchange at least 3 of them.",
		Rule:        DrawbackRule{Type: "useHighlightedTiles", Highlight: 4, Need: 3, AppliesToExchanges: true}},
	{ID: 101, Name: "Peer Pressure (lite)", NameSource: "friend",
		Description: "Each turn 3 random tiles on your rack are highlighted. You must play or exchange at least 2 of them.",
		Rule:        DrawbackRule{Type: "useHighlightedTiles", Highlight: 3, Need: 2, AppliesToExchanges: true}},
	{ID: 102, Name: "Peer Pressure (big crowd)", NameSource: "friend",
		Description: "Each turn 5 random tiles on your rack are highlighted. You must play or exchange at least 3 of them.",
		Rule:        DrawbackRule{Type: "useHighlightedTiles", Highlight: 5, Need: 3, AppliesToExchanges: true}},
	{ID: 61, Name: "Copy Cat", NameSource: "friend",
		Description: "If your opponent plays a tile you have, you must use every copy of it on your rack, or exchange or pass. A blank counts as a blank, whatever letter it was.",
		Rule:        DrawbackRule{Type: "useOpponentLetters"}},

	// -- Lose conditions: breaking one ends the game on the spot - the
	// player who broke it loses, scored 0, and the winner keeps their score
	// (see isLoseRule / drawbackLossAfterTurn and simulateOneGame).
	{ID: 8, Name: "Scoring Never Stops", NameSource: "friend",
		// No longer a lose condition (2026-11-12): a play under the
		// threshold just isn't allowed, so you exchange or pass instead.
		Description: "You must score at least 30 or you must pass or exchange (15 once the bag is empty).",
		Rule:        DrawbackRule{Type: "score", Comparator: "gte", Value: 30, ValueWhenBagEmpty: 15}},
	{ID: 34, Name: "Time Crunch", NameSource: "friend",
		Description: "Game runs past 25 turns and you lose.",
		Rule:        DrawbackRule{Type: "loseIfPastTurnLimit", Value: 25}},
	{ID: 10, Name: "Sandbagger", NameSource: "friend",
		Description: "You can't be winning before turn 16.",
		Rule:        DrawbackRule{Type: "loseIfLeadingBeforeTurn", Value: 16}},
	{ID: 9, Name: "Vervain", NameSource: "friend",
		Description: "Your opponent plays a bingo and you lose.",
		Rule:        DrawbackRule{Type: "loseIfOpponentBingos"}},
	{ID: 21, Name: "Triple Threat", NameSource: "claude",
		Description: "Your opponent covers a triple word score and you lose.",
		Rule:        DrawbackRule{Type: "loseIfOpponentCoversSquare", Types: []string{"TWS"}}},
	{ID: 12, Name: "We All Floater Down Here", NameSource: "friend",
		Description: "Fewer than 3 plays of 8 or more letters (bingo or not) by game end and you lose.",
		Rule:        DrawbackRule{Type: "loseIfFewLongPlays", Value: 3, MinLength: 8}},
	{ID: 20, Name: "Corner Rush", NameSource: "claude",
		Description: "No tile of yours on A1 by turn 14 and you lose. If your opponent takes A1 first, you lose on the spot.",
		Rule:        DrawbackRule{Type: "loseIfNoTileOnSquareByTurn", Row: 0, Col: 0, Value: 14}},
	{ID: 19, Name: "Bookends", NameSource: "friend",
		Description: "You must have at least 5 tiles in Row 1 and Row 15 by the end of the game or you lose.",
		Rule:        DrawbackRule{Type: "loseIfFewTilesInRows", Rows: []int{0, 14}, Value: 5}},
	{ID: 38, Name: "Minefield", NameSource: "friend",
		Description: "A random 3x3 zone touching the board's edge is set at game start; whoever plays inside it loses.",
		// The zone is rolled per game in simulateOneGame (rollClaustroZone)
		// and applies to BOTH players - see claustroLoss.
		Rule: DrawbackRule{Type: "loseIfPlayInZone"}},
	{ID: 36, Name: "Zeroed In", NameSource: "friend",
		Description: "Must exchange or pass at least 6 times over the course of the game.",
		// Lose rule, judged at game end: exchanges and passes both count.
		Rule: DrawbackRule{Type: "loseIfFewExchangesOrPasses", Value: 6}},
	{ID: 25, Name: "I'll Take That", NameSource: "friend",
		Description: "If your opponent's play opens up a bonus square you can now reach with a word, you must cover it - or exchange or pass.",
		// Forcing. "Opened" (2026-11-12) = an empty premium square this
		// player's rack can cover with a legal play now but couldn't have
		// before the opponent's last play - see openedPremiumSquares.
		Rule: DrawbackRule{Type: "mustCoverOpenedPremium", Forcing: true}},
	{ID: 17, Name: "Nerf War", NameSource: "friend",
		Description: "Tiles you place score 1 point on the turn you place them (blanks 0). Once on the board they're worth their usual value, to both players. Hidden from your opponent until the game ends.",
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
		"loseIfFewExchangesOrPasses", "loseIfFewBigPlays":
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
		// The opponent taking the square ends it at once - the holder can
		// never cover it after that.
		if !byHolder && turn.Type == "play" {
			for _, t := range turn.Tiles {
				if t.IsNew && t.Row == rule.Row && t.Col == rule.Col {
					return true, "the opponent took " + squareName(rule.Row, rule.Col)
				}
			}
		}
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
	// Performance Anxiety: fewer than Value plays scoring MinScore or more.
	if rule.Type == "loseIfFewBigPlays" {
		count := 0
		for _, t := range turns {
			if t.Player == holder && t.Type == "play" && t.Score >= rule.MinScore {
				count++
			}
		}
		if float64(count) < rule.Value {
			return true, fmt.Sprintf("only %d play(s) of %d+ points by game end", count, rule.MinScore)
		}
		return false, ""
	}
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
		if t.Player == holder && t.Type == "play" && len(t.Tiles) >= rule.MinLength {
			count++
		}
	}
	if float64(count) < rule.Value {
		return true, fmt.Sprintf("only %d play(s) of %d+ letters by game end", count, rule.MinLength)
	}
	return false, ""
}

// zeroedInMustSkip: should a Zeroed In bot exchange or pass this turn? Yes
// once the exchanges/passes it still needs reach its own turns left, roughly
// one per 8 tiles in the bag (both players drawing ~4 a turn) plus one.
func zeroedInMustSkip(rule DrawbackRule, poolSize int, turns []SimTurn, player int) bool {
	count := 0
	for _, t := range turns {
		if t.Player == player && (t.Type == "exchange" || t.Type == "pass") {
			count++
		}
	}
	needed := int(rule.Value) - count
	return needed > 0 && needed >= poolSize/8+1
}

// drawbackExtras: what filterCandidatesByDrawback needs worked out once per
// turn, not per candidate. Opened: I'll Take That's opened squares. Blocked
// and Quota: Shifting Sands' squares (row*15+col) and Quota's two numbers
// when the caller rolled them (a server game); nil means roll here (the
// Sandbox).
type drawbackExtras struct {
	Opened  map[rcPos]bool
	Blocked map[int]bool
	Quota   []int
	// Drop-Zone's quarter (0 top-left, 1 top-right, 2 bottom-left, 3
	// bottom-right), Flavor Of The Day's kind, Peer Pressure's picked tiles
	// ("?" for a blank) - nil/"" means roll here (the Sandbox).
	Quadrant    *int
	Flavor      string
	Highlighted string
}

// openedPremiumSquares (I'll Take That): the empty premium squares in
// reachNow (squares this player's rack can put a tile on with a legal play
// now) that weren't in reachBefore (the same rack, on the board before the
// opponent's last play). None when the last turn wasn't the opponent's
// play. simulate.go builds both sets with reachableSquares.
func openedPremiumSquares(turns []SimTurn, player int, bd *board.GameBoard, reachNow, reachBefore map[rcPos]bool) map[rcPos]bool {
	opened := map[rcPos]bool{}
	if len(turns) == 0 {
		return opened
	}
	last := turns[len(turns)-1]
	if last.Player == player || last.Type != "play" {
		return opened
	}
	for pos := range reachNow {
		if reachBefore[pos] {
			continue
		}
		if _, ok := premiumTypeAt(pos.row, pos.col, bd); ok {
			opened[pos] = true
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

// claustroZone is Minefield's 3x3 zone, by its top-left square, and
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

// loseIfPlayScoreBelow (unused since Scoring Never Stops became a "score"
// rule): a play scoring under Value (ValueWhenBagEmpty once
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

// tunnelSquares is Tunnel Vision's target squares: take the last word that
// put tiles on the board, by either player (its whole main word, including
// tiles it ran through), and from each of its tiles walk up, down, left and
// right to the first empty square on bd. nil before any word is on the
// board (anything goes). evaluate.js tunnelSquares, same.
func tunnelSquares(turns []SimTurn, bd *board.GameBoard) map[rcPos]bool {
	steps := [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}}
	inBounds := func(r, c int) bool { return r >= 0 && r < 15 && c >= 0 && c < 15 }
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.Type != "play" || newTileCountOf(t.Tiles) == 0 {
			continue
		}
		squares := map[rcPos]bool{}
		for _, tile := range t.Tiles {
			for _, d := range steps {
				r, c := tile.Row+d[0], tile.Col+d[1]
				for inBounds(r, c) && bd.GetLetter(r, c) != 0 {
					r, c = r+d[0], c+d[1]
				}
				if inBounds(r, c) {
					squares[rcPos{r, c}] = true
				}
			}
		}
		return squares
	}
	return nil
}

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

// wordsFormed is every word candidate c forms: its main word, then the
// cross word through each new tile, walked on bd (the board before the
// move). Each word is its tiles in reading order. evaluate.js wordsOfMove,
// same.
func wordsFormed(c *scoredCandidate, bd *board.GameBoard) [][]MoveTile {
	words := [][]MoveTile{c.detailed.Tiles}
	dr, dc := 1, 0 // cross words run down when the main word runs across
	if c.detailed.Direction == "down" {
		dr, dc = 0, 1
	}
	for _, t := range c.detailed.Tiles {
		if !t.IsNew {
			continue
		}
		r, col := t.Row-dr, t.Col-dc
		for r >= 0 && col >= 0 && bd.GetLetter(r, col) != 0 {
			r, col = r-dr, col-dc
		}
		r, col = r+dr, col+dc
		var cross []MoveTile
		for r < 15 && col < 15 {
			if r == t.Row && col == t.Col {
				cross = append(cross, t)
			} else if ml := bd.GetLetter(r, col); ml != 0 {
				cross = append(cross, MoveTile{Row: r, Col: col, Letter: strings.ToUpper(alph.Letter(ml.Unblank())), IsBlank: ml.IsBlanked()})
			} else {
				break
			}
			r, col = r+dr, col+dc
		}
		if len(cross) > 1 {
			words = append(words, cross)
		}
	}
	return words
}

// wordStartLetters is the first letter of each word c forms (upper case).
func wordStartLetters(c *scoredCandidate, bd *board.GameBoard) []string {
	words := wordsFormed(c, bd)
	starts := make([]string, len(words))
	for i, w := range words {
		starts[i] = strings.ToUpper(w[0].Letter)
	}
	return starts
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

// tilesPlacedTotal is how many tiles player has placed on the board so far
// this game (Here For A Good Time). Exchanges don't count.
func tilesPlacedTotal(turns []SimTurn, player int) int {
	total := 0
	for _, t := range turns {
		if t.Player != player || t.Type != "play" {
			continue
		}
		for _, tile := range t.Tiles {
			if tile.IsNew {
				total++
			}
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

	// Modesty's cap, Scoring Never Stops' floor (ValueWhenBagEmpty once the
	// bag is empty, when the rule has one).
	case "score":
		threshold := rule.Value
		if poolSizeBefore == 0 && rule.ValueWhenBagEmpty > 0 {
			threshold = rule.ValueWhenBagEmpty
		}
		return compareFloat(float64(c.detailed.Score), rule.Comparator, threshold)

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
		// Kevin's own exemption: one-tile plays. Placing a single tile has
		// no real direction, even when the word it makes runs the forbidden
		// way (X above an I makes XI down). Count only the NEW tiles -
		// counting the whole main word refused XI because the I was
		// already there.
		if newTileCount(c) <= 1 {
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
		// The play must reach the highest floor among the restricted tiles
		// it places (a blank's is BlankMinScore, when set).
		floor := 0
		for _, t := range c.detailed.Tiles {
			if !t.IsNew {
				continue
			}
			if t.IsBlank {
				if strings.ContainsRune(rule.Letters, '?') {
					blankFloor := rule.MinScore
					if rule.BlankMinScore > 0 {
						blankFloor = rule.BlankMinScore
					}
					floor = max(floor, blankFloor)
				}
				continue
			}
			if strings.ContainsRune(rule.Letters, []rune(t.Letter)[0]) {
				floor = max(floor, rule.MinScore)
			}
		}
		return c.detailed.Score >= floor

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
		// Some word the play forms (main or cross) starts with a tile the
		// player placed that's that letter (not a blank): T(AR) and H(A)
		// next to PA count; (TACO)S, (TAR)T and (STAR)T don't.
		for _, word := range wordsFormed(c, bd) {
			if first := word[0]; first.IsNew && !first.IsBlank && first.Letter == string(lastAlpha) {
				return true
			}
		}
		return false

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
	// Crisscross: one-tile plays are always fine (count NEW tiles), and they
	// don't set the direction either - compare with the last play of more
	// than one tile.
	case "mustAlternateDirection":
		if newTileCount(c) <= 1 {
			return true
		}
		var last *SimTurn
		for i := len(turns) - 1; i >= 0; i-- {
			if turns[i].Player == currentPlayer && turns[i].Type == "play" && newTileCountOf(turns[i].Tiles) > 1 {
				last = &turns[i]
				break
			}
		}
		if last == nil {
			return true
		}
		return c.detailed.Direction != last.Direction

	// Tunnel Vision: "play through" means the candidate's own resulting
	// word (its FULL tile span, not just the newly-placed ones - a play
	// can run through the opponent's tile without placing anything new
	// exactly there) shares at least one square with a tile the opponent
	// placed NEW on their immediately preceding play.
	// Tunnel Vision: a new tile on one of tunnelSquares.
	case "mustPlayNextToLastPlay":
		squares := tunnelSquares(turns, bd)
		if squares == nil {
			return true
		}
		for _, t := range c.detailed.Tiles {
			if t.IsNew && squares[rcPos{t.Row, t.Col}] {
				return true
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
	// Alphabet Soup: every word the play forms (cross-words included)
	// starts with a different letter, none used to start a word before
	// (SimTurn.WordStarts, recorded as each play is made).
	case "uniqueStartingLetterPerWord":
		used := map[string]bool{}
		for _, t := range allWordPlaysByPlayer(turns, currentPlayer) {
			for _, l := range t.WordStarts {
				used[l] = true
			}
		}
		for _, l := range wordStartLetters(c, bd) {
			if used[l] {
				return false
			}
			used[l] = true
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
	// Here For A Good Time: this play's tiles must fit in what's left of the
	// player's whole-game allowance of tiles placed. Exchanges never reach
	// here (no AppliesToExchanges), so they're free.
	case "maxTilesLeaveRack":
		return tilesPlacedTotal(turns, currentPlayer)+newTileCount(c) <= rule.MaxTiles

	// I'll Take That is handled in filterCandidatesByDrawback (it needs the
	// opened squares, worked out once per turn in simulate.go).
	case "mustCoverOpenedPremium":
		return true

	// Well Aged: only after a scoring play - an exchange, a pass or a
	// 0-point play lifts it for the next turn, so it can't trap anyone in
	// an exchange loop. A bingo (all 7 tiles) is always allowed. Otherwise
	// every new tile must come from what was kept through that play.
	case "cannotPlayFreshlyDrawnTile":
		if newTileCount(c) == 7 {
			return true
		}
		last := lastTurnByPlayer(turns, currentPlayer)
		if last == nil || last.Type != "play" || last.Score <= 0 {
			return true
		}
		agedRack := removeRackFromPool(last.RackBefore, newlyUsedLetters(last.Tiles))
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

	// Frontload: the cap drops by Step for each of the player's earlier
	// turns, down to MinScore.
	case "shrinkingScoreCap":
		limit := int(rule.Value) - rule.Step*ownTurnCount(turns, currentPlayer)
		if limit < rule.MinScore {
			limit = rule.MinScore
		}
		return c.detailed.Score <= limit

	// Mood Swings: the player's own turns so far, mod 3 - 0 anything, 1 at
	// most one consonant placed, 2 at most one vowel placed.
	case "moodCycle":
		consonants, vowels := 0, 0
		for _, t := range c.detailed.Tiles {
			if !t.IsNew {
				continue
			}
			if strings.ContainsRune("AEIOU", []rune(strings.ToUpper(t.Letter))[0]) {
				vowels++
			} else {
				consonants++
			}
		}
		switch ownTurnCount(turns, currentPlayer) % 3 {
		case 1:
			return consonants <= 1
		case 2:
			return vowels <= 1
		}
		return true

	// Claustrophobia: no new tile in the outer Value rows or columns.
	case "forbiddenBorder":
		depth := int(rule.Value)
		for _, t := range c.detailed.Tiles {
			if t.IsNew && (t.Row < depth || t.Row > 14-depth || t.Col < depth || t.Col > 14-depth) {
				return false
			}
		}
		return true

	// Lonerism: a letter held more than once (two blanks count) can't be
	// played.
	case "noDuplicateRackTiles":
		for _, letter := range newlyUsedLetters(c.detailed.Tiles) {
			if strings.Count(preMoveRack, string(letter)) > 1 {
				return false
			}
		}
		return true

	// Copy Cat: every rack copy of each letter the opponent placed on their
	// immediately preceding turn (a blank counts as "?") has to be used.
	case "useOpponentLetters":
		required := copyCatRequired(preMoveRack, turns, currentPlayer)
		return required == "" || containsLetters(newlyUsedLetters(c.detailed.Tiles), required)

	// Go Big or Go Home: outscore the opponent's last word play.
	case "outscoreOpponentLastPlay":
		last := lastWordPlayByPlayer(turns, 3-currentPlayer)
		return last == nil || c.detailed.Score > last.Score

	// Rising Tide: start on a higher row (a smaller row number) than the
	// opponent's last word play started on.
	case "startAboveOpponentLastPlay":
		last := lastWordPlayByPlayer(turns, 3-currentPlayer)
		if last == nil || len(last.Tiles) == 0 {
			return true
		}
		return topRow(c.detailed.Tiles) < topRow(last.Tiles)

	// Triskaidekaphobia: plays only while the rack adds up to under Value.
	case "rackValueBelow":
		return float64(rackPointValue(preMoveRack)) < rule.Value

	default:
		// Unknown/not-yet-implemented type - never silently disqualify a
		// player's whole move set over a rule this build doesn't know how
		// to check.
		return true
	}
}

// ownTurnCount: how many turns (plays, exchanges, passes) the player has
// taken so far. Wally's step-in opener is player 0, so it never counts.
func ownTurnCount(turns []SimTurn, player int) int {
	n := 0
	for _, t := range turns {
		if t.Player == player {
			n++
		}
	}
	return n
}

// climbRank (Climbing the Ranks): how many tiles this turn calls for -
// one more than the player's last play placed (capped), back to 1 after
// an exchange, unchanged by a pass, 1 to begin with.
func climbRank(turns []SimTurn, player, maxTiles int) int {
	rank := 1
	for _, t := range turns {
		if t.Player != player {
			continue
		}
		switch t.Type {
		case "play":
			rank = newTileCountOf(t.Tiles) + 1
		case "exchange":
			rank = 1
		}
	}
	if rank > maxTiles {
		rank = maxTiles
	}
	return rank
}

// topRow is the row a play starts on: the smallest row among its tiles.
func topRow(tiles []MoveTile) int {
	top := 15
	for _, t := range tiles {
		if t.Row < top {
			top = t.Row
		}
	}
	return top
}

// copyCatRequired: the rack tiles Copy Cat makes the player use - every copy
// on preMoveRack of each letter ("?" for a blank) the opponent placed on the
// turn just before this one. "" when that turn wasn't the opponent's play.
func copyCatRequired(preMoveRack string, turns []SimTurn, player int) string {
	if len(turns) == 0 {
		return ""
	}
	last := turns[len(turns)-1]
	if last.Player != 3-player || last.Type != "play" {
		return ""
	}
	seen := map[rune]bool{}
	var required []rune
	for _, letter := range newlyUsedLetters(last.Tiles) {
		if seen[letter] {
			continue
		}
		seen[letter] = true
		for _, held := range preMoveRack {
			if held == letter {
				required = append(required, held)
			}
		}
	}
	return string(required)
}

// lettersInCommon: how many letters a and b share, counting repeats.
func lettersInCommon(a, b string) int {
	counts := map[rune]int{}
	for _, r := range b {
		counts[r]++
	}
	n := 0
	for _, r := range a {
		if counts[r] > 0 {
			counts[r]--
			n++
		}
	}
	return n
}

// containsLetters: does have contain every letter of want, counting repeats?
func containsLetters(have, want string) bool {
	counts := map[rune]int{}
	for _, r := range have {
		counts[r]++
	}
	for _, r := range want {
		if counts[r] == 0 {
			return false
		}
		counts[r]--
	}
	return true
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
//
// extras: things worked out once per turn by the caller - see drawbackExtras.
func filterCandidatesByDrawback(candidates []scoredCandidate, rule DrawbackRule, preMoveRack string, bd *board.GameBoard, poolSizeBefore int, turns []SimTurn, currentPlayer int, extras drawbackExtras) ([]scoredCandidate, string) {
	opened := extras.Opened
	// Climbing the Ranks: plays of exactly the rank's tile count; if there
	// are none, one rank lower, and so on. Exchanges always stay in.
	if rule.Type == "climbTileCount" {
		exchanges := make([]scoredCandidate, 0)
		for _, c := range candidates {
			if c.isExchange {
				exchanges = append(exchanges, c)
			}
		}
		for need := climbRank(turns, currentPlayer, rule.MaxTiles); need >= 1; need-- {
			kept := append([]scoredCandidate{}, exchanges...)
			found := false
			for i := range candidates {
				if !candidates[i].isExchange && newTileCount(&candidates[i]) == need {
					kept = append(kept, candidates[i])
					found = true
				}
			}
			if found {
				return kept, fmt.Sprintf("Climbing the Ranks: %d tiles this turn", need)
			}
		}
		return exchanges, "Climbing the Ranks: no play fits"
	}

	// Drop-Zone: a play must put a new tile in this turn's quarter.
	if rule.Type == "randomQuadrant" {
		q := rand.Intn(4)
		if extras.Quadrant != nil {
			q = *extras.Quadrant
		}
		rowLo, colLo := 0, 0
		if q >= 2 {
			rowLo = 7
		}
		if q%2 == 1 {
			colLo = 7
		}
		kept := make([]scoredCandidate, 0, len(candidates))
		for _, c := range candidates {
			if c.isExchange {
				kept = append(kept, c)
				continue
			}
			for _, t := range c.detailed.Tiles {
				if t.IsNew && t.Row >= rowLo && t.Row <= rowLo+7 && t.Col >= colLo && t.Col <= colLo+7 {
					kept = append(kept, c)
					break
				}
			}
		}
		return kept, fmt.Sprintf("Drop-Zone: quarter %d", q)
	}

	// Flavor Of The Day: no new tile on a bonus square of any other kind
	// (the center star is always fine).
	if rule.Type == "randomBonusFlavor" {
		flavor := extras.Flavor
		if flavor == "" {
			flavor = []string{"DLS", "DWS", "TLS", "TWS"}[rand.Intn(4)]
		}
		kept := make([]scoredCandidate, 0, len(candidates))
		for _, c := range candidates {
			ok := true
			if !c.isExchange {
				for _, t := range c.detailed.Tiles {
					if !t.IsNew || (t.Row == 7 && t.Col == 7) {
						continue
					}
					if kind, isBonus := premiumTypeAt(t.Row, t.Col, bd); isBonus && kind != flavor {
						ok = false
						break
					}
				}
			}
			if ok {
				kept = append(kept, c)
			}
		}
		return kept, "Flavor Of The Day: " + flavor
	}

	// Peer Pressure: the move (a play or an exchange) must use Need of this
	// turn's highlighted tiles - all of them when fewer are left.
	if rule.Type == "useHighlightedTiles" {
		highlighted := extras.Highlighted
		if highlighted == "" {
			rack := []rune(preMoveRack)
			rand.Shuffle(len(rack), func(i, j int) { rack[i], rack[j] = rack[j], rack[i] })
			if len(rack) > rule.Highlight {
				rack = rack[:rule.Highlight]
			}
			highlighted = string(rack)
		}
		need := rule.Need
		if n := len([]rune(highlighted)); n < need {
			need = n
		}
		kept := make([]scoredCandidate, 0, len(candidates))
		for _, c := range candidates {
			used := c.exchangeTiles
			if !c.isExchange {
				used = newlyUsedLetters(c.detailed.Tiles)
			}
			if lettersInCommon(used, highlighted) >= need {
				kept = append(kept, c)
			}
		}
		return kept, "Peer Pressure: " + highlighted
	}

	// Megalophobia: if any play scores Value or more, no play is allowed -
	// only exchanges (or a pass).
	if rule.Type == "noPlaysIfAnyScoresAtLeast" {
		for i := range candidates {
			if !candidates[i].isExchange && float64(candidates[i].detailed.Score) >= rule.Value {
				exchanges := make([]scoredCandidate, 0)
				for _, c := range candidates {
					if c.isExchange {
						exchanges = append(exchanges, c)
					}
				}
				return exchanges, fmt.Sprintf("Megalophobia: a %d+ play exists", int(rule.Value))
			}
		}
		return candidates, ""
	}

	// I'll Take That (forcing): if any play covers an opened square, the
	// plays collapse to those; exchanges always stay in.
	if rule.Type == "mustCoverOpenedPremium" {
		kept := make([]scoredCandidate, 0, len(candidates))
		bites := false
		for i := range candidates {
			c := &candidates[i]
			if c.isExchange {
				kept = append(kept, *c)
				continue
			}
			for _, t := range c.detailed.Tiles {
				if t.IsNew && opened[rcPos{t.Row, t.Col}] {
					kept = append(kept, *c)
					bites = true
					break
				}
			}
		}
		if bites {
			return kept, ""
		}
		return candidates, ""
	}

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
		// Zeroed In: a bot never exchanges or passes by choice, so once the
		// count still needed catches up with its turns left it only
		// exchanges - or passes (no candidates) when it can't.
		if rule.Type == "loseIfFewExchangesOrPasses" && zeroedInMustSkip(rule, poolSizeBefore, turns, currentPlayer) {
			exchanges := make([]scoredCandidate, 0)
			for _, c := range candidates {
				if c.isExchange {
					exchanges = append(exchanges, c)
				}
			}
			return exchanges, "Zeroed In: exchanging/passing to reach the count"
		}
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
		// Exchanges always stay in (you can always exchange or pass).
		if len(qualifying) > 0 {
			return append(qualifying, exchanges...), ""
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
		blockedCount := min(int(rule.Value), len(others))
		blocked := make(map[int]bool, blockedCount)
		for _, sq := range others[:blockedCount] {
			blocked[sq] = true
		}
		// A server game rolls its own squares (and keeps them) - use those.
		if extras.Blocked != nil {
			blocked = extras.Blocked
			blockedCount = len(blocked)
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
		// A server game rolls its own numbers (and keeps them) - use those.
		if len(extras.Quota) == 2 {
			n1, n2 = extras.Quota[0], extras.Quota[1]
		}
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
	// here (see DrawbackRule.Forcing's own comment). If any word-play
	// qualifies, the plays collapse to just those - but exchanges always
	// stay in (2026-11-12: you can always exchange or pass unless a rule
	// says otherwise, e.g. when you don't know the word that fits). If
	// none qualify, the rule doesn't bite this turn.
	if rule.Forcing {
		qualifying := make([]scoredCandidate, 0, len(candidates))
		for i := range candidates {
			c := &candidates[i]
			if c.isExchange || evaluateDrawback(rule, c, preMoveRack, bd, poolSizeBefore, turns, currentPlayer) {
				qualifying = append(qualifying, *c)
			}
		}
		for i := range qualifying {
			if !qualifying[i].isExchange {
				return qualifying, ""
			}
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
