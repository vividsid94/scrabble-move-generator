package main

// Bingo defense: one rule of 90s Wally (nineties_wally_for_rules_bot.go).
// It charges each candidate move for the opponent bingo chance it leaves:
//
//	cost = opponent bingo chance after the move x netBingoCost x CostMultiplier
//
// The bingo chance comes from the board's openness after the move
// (board_openness_for_rules_bot.go) read off the fixed curve in
// bingo_chance_for_rules_bot.go. Covers bingos only - not hotspots.
//
// Locked in 2026-10-20 from Wally-vs-Wally series (90s Wally vs plain
// Wally, half going first):
//   - CostMultiplier 2: about 51.3% over 12,000 games (51.2% then 51.4% on
//     a fresh run). 1 was about 50%, 3 fell back to about 50%, 5 lost.
//     Blocking is worth about twice a bingo's one-turn cost: the lanes stay
//     open and a bingo is what lets the opponent come back.
//   - On whenever ahead (LeadBy 1). Ahead by 30+ and 60+ did no better;
//     closing while behind didn't help either.
//   - Off once the bag is empty: the curve was measured with tiles left,
//     and an empty-bag position is an endgame question.

// BingoDefenseConfig: zero values mean the locked settings above, so a
// Sandbox request can override one setting at a time for tests.
type BingoDefenseConfig struct {
	// Off switches the rule off.
	Off bool `json:"off,omitempty"`
	// CostMultiplier scales netBingoCost. Default 2.
	CostMultiplier float64 `json:"costMultiplier,omitempty"`
	// LeadBy: the rule applies when ahead by at least this much. Default 1.
	LeadBy int `json:"leadBy,omitempty"`
	// BehindUpTo, if set, also applies it when behind by this much or less.
	BehindUpTo int `json:"behindUpTo,omitempty"`
}

const (
	bingoDefenseCostMultiplier = 2.0
	bingoDefenseLeadBy         = 1
)

// bingoDefenseActive: does the rule apply this turn? lead is my score minus
// the opponent's; bagSize is the tiles left in the bag at the turn's start.
func bingoDefenseActive(cfg BingoDefenseConfig, lead, bagSize int) bool {
	if cfg.Off || bagSize == 0 {
		return false
	}
	leadBy := cfg.LeadBy
	if leadBy <= 0 {
		leadBy = bingoDefenseLeadBy
	}
	return lead >= leadBy || (cfg.BehindUpTo > 0 && lead >= -cfg.BehindUpTo)
}

// bingoDefenseCost: the points charged against a move that leaves the board
// at this openness.
func bingoDefenseCost(cfg BingoDefenseConfig, openness float64) float64 {
	multiplier := cfg.CostMultiplier
	if multiplier <= 0 {
		multiplier = bingoDefenseCostMultiplier
	}
	return opponentBingoChance(openness) * netBingoCost * multiplier
}

// BingoDefenseDetail: what bingo defense saw, for RulesBotSwitch.
type BingoDefenseDetail struct {
	OpennessBefore    float64 `json:"opennessBefore"`
	WallyOpenness     float64 `json:"wallyOpenness"`
	WallyBingoChance  float64 `json:"wallyBingoChance"`
	ChosenOpenness    float64 `json:"chosenOpenness"`
	ChosenBingoChance float64 `json:"chosenBingoChance"`
}
