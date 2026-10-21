package main

import (
	"fmt"

	"github.com/domino14/macondo/board"
)

// 90s Wally: plain Wally (rank 1, score + leave) plus hand-written if/else
// rules, like it's the 90s - no simulation, no neural nets. A rule may only
// move off Wally's top pick to a move within MaxEquityLoss of it.
//
// Rules so far:
//   - Board openness (board_openness_for_rules_bot.go): ahead by LeadBy or
//     more, play the allowed move leaving the fewest bingo lanes; behind by
//     LeadBy or more, the one leaving the most. Ties go to higher equity.

// RulesBotConfig turns 90s Wally on for a Sandbox bot (BotConfig.RulesBot).
// Each rule has its own switch so rules can be tested one at a time.
type RulesBotConfig struct {
	// MaxEquityLoss: how far below the top move's equity a rule may go.
	// Default 6.
	MaxEquityLoss float64 `json:"maxEquityLoss,omitempty"`
	// LeadBy: the lead (or deficit) that switches the openness rule on.
	// Default 30.
	LeadBy int `json:"leadBy,omitempty"`
	// CloseWhenAhead / OpenWhenBehind: the openness rule's two halves.
	CloseWhenAhead bool `json:"closeWhenAhead,omitempty"`
	OpenWhenBehind bool `json:"openWhenBehind,omitempty"`
}

// rulesBotPick returns the index in best-first `candidates` that 90s Wally
// plays, and a debug note when a rule moved it off index 0.
func rulesBotPick(cfg RulesBotConfig, candidates []scoredCandidate, bd *board.GameBoard, myScore, opponentScore int) (int, string) {
	if len(candidates) == 0 {
		return -1, ""
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
	closing := cfg.CloseWhenAhead && lead >= leadBy
	opening := cfg.OpenWhenBehind && lead <= -leadBy
	if !closing && !opening {
		return 0, ""
	}

	currentLanes := countBingoLanes(bd)
	lanesAfter := func(c *scoredCandidate) int {
		if c.isExchange {
			return currentLanes
		}
		after := bd.Copy()
		after.PlayMove(c.move)
		return countBingoLanes(after)
	}

	bestIdx := 0
	bestLanes := lanesAfter(&candidates[0])
	topLanes := bestLanes
	for i := 1; i < len(candidates); i++ {
		if candidates[0].total-candidates[i].total > maxLoss {
			break // best-first, so the rest are further below
		}
		lanes := lanesAfter(&candidates[i])
		if (closing && lanes < bestLanes) || (opening && lanes > bestLanes) {
			bestIdx, bestLanes = i, lanes
		}
	}
	if bestIdx == 0 {
		return 0, ""
	}
	direction := "closed"
	if opening {
		direction = "opened"
	}
	return bestIdx, fmt.Sprintf("90s Wally %s the board: %d lanes instead of %d (%.1f equity given up)",
		direction, bestLanes, topLanes, candidates[0].total-candidates[bestIdx].total)
}
