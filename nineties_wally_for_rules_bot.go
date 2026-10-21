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
//     more, play the allowed move leaving the fewest 8-letter lanes, if it
//     closes at least MinEightLetterLanesClosed more than the top move. Ties go to
//     higher equity. With CloseWhenBehindUpTo set, it also closes when behind
//     by that much or less (Wally-style bots are overaggressive, so closing
//     can pay even from slightly behind). Opening the board when behind was
//     tried and dropped.

// RulesBotConfig turns 90s Wally on for a Sandbox bot (BotConfig.RulesBot).
// Each rule has its own switch so rules can be tested one at a time.
type RulesBotConfig struct {
	// MaxEquityLoss: how far below the top move's equity a rule may go.
	// Default 6.
	MaxEquityLoss float64 `json:"maxEquityLoss,omitempty"`
	// LeadBy: the lead that switches the openness rule on. Default 30.
	LeadBy int `json:"leadBy,omitempty"`
	// CloseWhenAhead switches the openness rule on.
	CloseWhenAhead bool `json:"closeWhenAhead,omitempty"`
	// MinEightLetterLanesClosed: how many fewer 8-letter lanes (normalized, see
	// board_openness_for_rules_bot.go) than the top move a replacement must
	// leave. Default 0.125 (one run).
	MinEightLetterLanesClosed float64 `json:"minEightLetterLanesClosed,omitempty"`
	// CloseWhenBehindUpTo: if set, the openness rule also applies when behind
	// by this many points or fewer (so whenever the lead is at least minus
	// this), not just when ahead by LeadBy.
	CloseWhenBehindUpTo int `json:"closeWhenBehindUpTo,omitempty"`
}

// RulesBotSwitch records both moves when a rule moved 90s Wally off
// Wally's top move, so the Sandbox can show what Wally would have played.
type RulesBotSwitch struct {
	WallyMove     *DetailedMove `json:"wallyMove,omitempty"` // nil when Wally's top move was an exchange
	WallyExchange string        `json:"wallyExchange,omitempty"`
	WallyEquity   float64       `json:"wallyEquity"`
	WallyEightLetterLanes  float64       `json:"wallyEightLetterLanes"`
	ChosenEquity  float64       `json:"chosenEquity"`
	ChosenEightLetterLanes float64       `json:"chosenEightLetterLanes"`
	EightLetterLanesBefore float64       `json:"eightLetterLanesBefore"`
}

// rulesBotPick returns the index in best-first `candidates` that 90s Wally
// plays, plus a debug note and both moves when a rule moved it off index 0.
func rulesBotPick(cfg RulesBotConfig, candidates []scoredCandidate, bd *board.GameBoard, myScore, opponentScore int) (int, string, *RulesBotSwitch) {
	if len(candidates) == 0 {
		return -1, "", nil
	}
	maxLoss := cfg.MaxEquityLoss
	if maxLoss <= 0 {
		maxLoss = 6
	}
	leadBy := cfg.LeadBy
	if leadBy <= 0 {
		leadBy = 30
	}

	minClosed := cfg.MinEightLetterLanesClosed
	if minClosed <= 0 {
		minClosed = 0.125
	}

	lead := myScore - opponentScore
	active := lead >= leadBy || (cfg.CloseWhenBehindUpTo > 0 && lead >= -cfg.CloseWhenBehindUpTo)
	if !cfg.CloseWhenAhead || !active {
		return 0, "", nil
	}

	currentLanes := countEightLetterLanes(bd)
	lanesAfter := func(c *scoredCandidate) float64 {
		if c.isExchange {
			return currentLanes
		}
		after := bd.Copy()
		after.PlayMove(c.move)
		return countEightLetterLanes(after)
	}

	bestIdx := 0
	bestLanes := lanesAfter(&candidates[0])
	topLanes := bestLanes
	for i := 1; i < len(candidates); i++ {
		if candidates[0].total-candidates[i].total > maxLoss {
			break // best-first, so the rest are further below
		}
		lanes := lanesAfter(&candidates[i])
		// The small slack keeps exact multiples of 1/8 from failing on rounding.
		if lanes < bestLanes && lanes <= topLanes-minClosed+1e-9 {
			bestIdx, bestLanes = i, lanes
		}
	}
	if bestIdx == 0 {
		return 0, "", nil
	}
	top := candidates[0]
	sw := &RulesBotSwitch{
		WallyEquity: top.total, WallyEightLetterLanes: topLanes,
		ChosenEquity: candidates[bestIdx].total, ChosenEightLetterLanes: bestLanes,
		EightLetterLanesBefore: currentLanes,
	}
	if top.isExchange {
		sw.WallyExchange = top.exchangeTiles
	} else {
		sw.WallyMove = top.detailed
	}
	return bestIdx, fmt.Sprintf("90s Wally closed the board: %.3g 8-letter lanes instead of %.3g (%.1f equity given up)",
		bestLanes, topLanes, top.total-candidates[bestIdx].total), sw
}
