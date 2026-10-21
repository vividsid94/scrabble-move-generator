package main

import (
	"fmt"

	"github.com/domino14/macondo/board"
	"github.com/domino14/word-golib/kwg"
)

// 90s Wally: plain Wally (rank 1, score + leave) plus hand-written if/else
// rules, like it's the 90s - no simulation, no neural nets.
//
// Each turn it takes Wally's candidates within MaxEquityLoss of the top
// move, subtracts every active rule's cost from each one's equity, and plays
// the best. Each rule is its own file and its own piece:
//   - Bingo defense (bingo_defense_for_rules_bot.go).
//
// A new rule adds a config field below and a cost to subtract in
// rulesBotPick.

// RulesBotConfig makes a Sandbox bot 90s Wally (BotConfig.RulesBot). An
// empty config is 90s Wally with every rule at its locked settings.
type RulesBotConfig struct {
	// MaxEquityLoss: how far below the top move's equity a rule may go.
	// Default 15.
	MaxEquityLoss float64            `json:"maxEquityLoss,omitempty"`
	BingoDefense  BingoDefenseConfig `json:"bingoDefense"`
}

const rulesBotMaxEquityLoss = 15.0

// RulesBotSwitch records both moves when a rule moved 90s Wally off Wally's
// top move, so the Sandbox can show what Wally would have played.
type RulesBotSwitch struct {
	WallyMove     *DetailedMove `json:"wallyMove,omitempty"` // nil when Wally's top move was an exchange
	WallyExchange string        `json:"wallyExchange,omitempty"`
	WallyEquity   float64       `json:"wallyEquity"`
	ChosenEquity  float64       `json:"chosenEquity"`
	// Each rule's view of the two moves (nil when the rule wasn't active).
	BingoDefense *BingoDefenseDetail `json:"bingoDefense,omitempty"`
}

// rulesBotPick returns the index in best-first `candidates` that 90s Wally
// plays, plus a debug note and both moves when a rule moved it off index 0.
// bagSize is the tiles left in the bag at the start of the turn.
func rulesBotPick(cfg RulesBotConfig, candidates []scoredCandidate, bd *board.GameBoard, gd *kwg.KWG, myScore, opponentScore, bagSize int) (int, string, *RulesBotSwitch) {
	if len(candidates) == 0 {
		return -1, "", nil
	}
	maxLoss := cfg.MaxEquityLoss
	if maxLoss <= 0 {
		maxLoss = rulesBotMaxEquityLoss
	}
	bingoOn := bingoDefenseActive(cfg.BingoDefense, myScore-opponentScore, bagSize)
	if !bingoOn {
		return 0, "", nil
	}

	opennessBefore := boardOpenness(bd)
	openness := make([]float64, 0, len(candidates))
	bestIdx, bestScore := 0, 0.0
	for i := range candidates {
		if candidates[0].total-candidates[i].total > maxLoss {
			break // best-first, so the rest are further below
		}
		o := opennessAfterMove(bd, gd, &candidates[i], opennessBefore)
		openness = append(openness, o)
		score := candidates[i].total - bingoDefenseCost(cfg.BingoDefense, o)
		if i == 0 || score > bestScore {
			bestIdx, bestScore = i, score
		}
	}
	if bestIdx == 0 {
		return 0, "", nil
	}

	top, chosen := candidates[0], candidates[bestIdx]
	sw := &RulesBotSwitch{
		WallyEquity:  top.total,
		ChosenEquity: chosen.total,
		BingoDefense: &BingoDefenseDetail{
			OpennessBefore:    opennessBefore,
			WallyOpenness:     openness[0],
			WallyBingoChance:  opponentBingoChance(openness[0]),
			ChosenOpenness:    openness[bestIdx],
			ChosenBingoChance: opponentBingoChance(openness[bestIdx]),
		},
	}
	if top.isExchange {
		sw.WallyExchange = top.exchangeTiles
	} else {
		sw.WallyMove = top.detailed
	}
	return bestIdx, fmt.Sprintf("90s Wally closed the board: opponent bingo chance %.1f%% instead of %.1f%% (%.1f equity given up)",
		100*sw.BingoDefense.ChosenBingoChance, 100*sw.BingoDefense.WallyBingoChance, top.total-chosen.total), sw
}
