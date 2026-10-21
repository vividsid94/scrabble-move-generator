package main

import (
	"fmt"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/word-golib/kwg"
)

// 90s Wally: plain Wally (rank 1, score + leave) plus hand-written if/else
// rules, like it's the 90s - no simulation, no neural nets. A rule may only
// move off Wally's top pick to a move within MaxEquityLoss of it.
//
// Rules so far:
//   - Bingo defense: when the rule is on, each allowed move is scored as its
//     equity minus the opponent's bingo chance after it (read from the fixed
//     curve in bingo_chance_for_rules_bot.go, by the board's 7-letter +
//     8-letter openness) times what a bingo costs (netBingoCost, times
//     BingoCostMultiplier). The best
//     score wins. Covers bingos only - not hotspots. Off once the bag is
//     empty: the curve was measured with tiles left, and an empty-bag
//     position is an endgame question. Opening the board when behind was
//     tried and dropped.

// RulesBotConfig turns 90s Wally on for a Sandbox bot (BotConfig.RulesBot).
// Each rule has its own switch so rules can be tested one at a time.
type RulesBotConfig struct {
	// MaxEquityLoss: how far below the top move's equity a rule may go.
	// Default 6.
	MaxEquityLoss float64 `json:"maxEquityLoss,omitempty"`
	// LeadBy: the lead that switches bingo defense on. Default 30.
	LeadBy int `json:"leadBy,omitempty"`
	// CloseWhenAhead switches bingo defense on.
	CloseWhenAhead bool `json:"closeWhenAhead,omitempty"`
	// BingoCostMultiplier scales netBingoCost in the bingo-defense formula, so
	// blocking can be valued above a bingo's average one-turn cost (an open
	// board stays open; when ahead, a bingo swings win chances more than its
	// points). Default 1.
	BingoCostMultiplier float64 `json:"bingoCostMultiplier,omitempty"`
	// CloseWhenBehindUpTo: if set, bingo defense also applies when behind by
	// this many points or fewer (so whenever the lead is at least minus
	// this), not just when ahead by LeadBy.
	CloseWhenBehindUpTo int `json:"closeWhenBehindUpTo,omitempty"`
}

// RulesBotSwitch records both moves when a rule moved 90s Wally off
// Wally's top move, so the Sandbox can show what Wally would have played.
type RulesBotSwitch struct {
	WallyMove         *DetailedMove `json:"wallyMove,omitempty"` // nil when Wally's top move was an exchange
	WallyExchange     string        `json:"wallyExchange,omitempty"`
	WallyEquity       float64       `json:"wallyEquity"`
	WallyOpenness     float64       `json:"wallyOpenness"`
	WallyBingoChance  float64       `json:"wallyBingoChance"`
	ChosenEquity      float64       `json:"chosenEquity"`
	ChosenOpenness    float64       `json:"chosenOpenness"`
	ChosenBingoChance float64       `json:"chosenBingoChance"`
	OpennessBefore    float64       `json:"opennessBefore"`
}

// boardOpenness is 7-letter + 8-letter openness. bd's cross-sets must be up
// to date.
func boardOpenness(bd *board.GameBoard) float64 {
	return sevenLetterOpenness(bd) + eightLetterOpenness(bd)
}

// rulesBotPick returns the index in best-first `candidates` that 90s Wally
// plays, plus a debug note and both moves when a rule moved it off index 0.
// bagSize is the tiles left in the bag at the start of the turn.
func rulesBotPick(cfg RulesBotConfig, candidates []scoredCandidate, bd *board.GameBoard, gd *kwg.KWG, myScore, opponentScore, bagSize int) (int, string, *RulesBotSwitch) {
	if len(candidates) == 0 {
		return -1, "", nil
	}
	if bagSize == 0 {
		return 0, "", nil
	}
	maxLoss := cfg.MaxEquityLoss
	if maxLoss <= 0 {
		maxLoss = 6
	}
	leadBy := cfg.LeadBy
	if leadBy <= 0 {
		leadBy = 30
	}

	lead := myScore - opponentScore
	active := lead >= leadBy || (cfg.CloseWhenBehindUpTo > 0 && lead >= -cfg.CloseWhenBehindUpTo)
	if !cfg.CloseWhenAhead || !active {
		return 0, "", nil
	}

	bingoCost := netBingoCost
	if cfg.BingoCostMultiplier > 0 {
		bingoCost *= cfg.BingoCostMultiplier
	}

	opennessBefore := boardOpenness(bd)
	opennessAfter := func(c *scoredCandidate) float64 {
		if c.isExchange {
			return opennessBefore
		}
		after := bd.Copy()
		after.PlayMove(c.move)
		cross_set.UpdateCrossSetsForMove(after, c.move, gd, ld)
		return boardOpenness(after)
	}
	// defended: equity minus the expected cost of an opponent bingo.
	defended := func(c *scoredCandidate, openness float64) float64 {
		return c.total - opponentBingoChance(openness)*bingoCost
	}

	topOpenness := opennessAfter(&candidates[0])
	bestIdx, bestOpenness := 0, topOpenness
	bestScore := defended(&candidates[0], topOpenness)
	for i := 1; i < len(candidates); i++ {
		if candidates[0].total-candidates[i].total > maxLoss {
			break // best-first, so the rest are further below
		}
		openness := opennessAfter(&candidates[i])
		if score := defended(&candidates[i], openness); score > bestScore {
			bestIdx, bestOpenness, bestScore = i, openness, score
		}
	}
	if bestIdx == 0 {
		return 0, "", nil
	}
	top := candidates[0]
	sw := &RulesBotSwitch{
		WallyEquity: top.total, WallyOpenness: topOpenness, WallyBingoChance: opponentBingoChance(topOpenness),
		ChosenEquity: candidates[bestIdx].total, ChosenOpenness: bestOpenness, ChosenBingoChance: opponentBingoChance(bestOpenness),
		OpennessBefore: opennessBefore,
	}
	if top.isExchange {
		sw.WallyExchange = top.exchangeTiles
	} else {
		sw.WallyMove = top.detailed
	}
	return bestIdx, fmt.Sprintf("90s Wally closed the board: opponent bingo chance %.1f%% instead of %.1f%% (%.1f equity given up)",
		100*sw.ChosenBingoChance, 100*sw.WallyBingoChance, top.total-candidates[bestIdx].total), sw
}
