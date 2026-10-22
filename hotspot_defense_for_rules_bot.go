package main

// Hotspot defense: one rule of 90s Wally (nineties_wally_for_rules_bot.go).
// It charges each candidate move for the scoring it leaves the opponent:
//
//	cost = opponent's expected next-turn score after the move x CostMultiplier
//
// The expected score comes from the board's hotspot load after the move
// (hotspots_for_rules_bot.go) read off the fixed curve in
// hotspot_score_for_rules_bot.go. Covers non-bingo scoring - bingos are bingo
// defense's job. On whenever the bag has tiles (hotspots cost points whatever
// the score); off once it's empty, like bingo defense.
//
// Series against Wally (2026-10-22, 4,000 games each, both with bingo
// defense at 2): CostMultiplier 1 won 51.5% (19% of turns changed, 1.9 equity
// given up each), 2 won 51.2% (27%, 2.6 each; about 50.8% over 6,000), bingo
// defense alone 50.5%. Default 1 - the cheaper version, as good or better.
// Being confirmed on a fresh 8,000 games.

// HotspotDefenseConfig: zero values mean the defaults, so a Sandbox request
// can override one setting at a time for tests.
type HotspotDefenseConfig struct {
	// Off switches the rule off.
	Off bool `json:"off,omitempty"`
	// CostMultiplier scales the expected score. Default 1.
	CostMultiplier float64 `json:"costMultiplier,omitempty"`
}

const hotspotDefenseCostMultiplier = 1.0

// hotspotDefenseActive: does the rule apply this turn?
func hotspotDefenseActive(cfg HotspotDefenseConfig, bagSize int) bool {
	return !cfg.Off && bagSize > 0
}

// hotspotDefenseCost: the points charged against a move that leaves the board
// at this hotspot load.
func hotspotDefenseCost(cfg HotspotDefenseConfig, load float64) float64 {
	multiplier := cfg.CostMultiplier
	if multiplier <= 0 {
		multiplier = hotspotDefenseCostMultiplier
	}
	return opponentHotspotScore(load) * multiplier
}

// HotspotDefenseDetail: what hotspot defense saw, for RulesBotSwitch.
type HotspotDefenseDetail struct {
	LoadBefore          float64 `json:"loadBefore"`
	WallyLoad           float64 `json:"wallyLoad"`
	WallyExpectedScore  float64 `json:"wallyExpectedScore"`
	ChosenLoad          float64 `json:"chosenLoad"`
	ChosenExpectedScore float64 `json:"chosenExpectedScore"`
}
