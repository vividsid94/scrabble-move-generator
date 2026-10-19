package main

import (
	"encoding/json"
	"math/rand"
	"net/http"
)

// Opponent personas: chess.com-style characters with habits ("Diana gets
// frustrated when she's losing"). NOT drawbacks - drawbacks.go is the rules
// of a future Scrabble variant; persona traits are a character's playing
// style. Keep the two separate.
//
// The server decides, the frontend executes: /bot-turn takes the position
// plus the facts a persona's rules need (both scores), ranks moves exactly
// like /generate-moves (rankMovesForRequest), and returns ONE action. The
// frontend owns presentation only (face, name, lines). Stateless - every
// fact a rule needs is sent on every request.
//
// A persona's rules work on the ranked list alone, so they don't depend on
// the lexicon - the same persona plays NWL and CSW the same way.

// personaRule is one way of picking a move.
type personaRule struct {
	// behindBy: the rule applies when the persona trails by at least this
	// many points (only used for the conditional rules in persona.when).
	behindBy int
	// mood is echoed to the frontend so it can show the right lines.
	mood string
	// The rank (1 = best by score + leave) to play: one of `ranks` at
	// random, or else a random rank in [rankMin, rankMax].
	ranks            []int
	rankMin, rankMax int
	// neverMissBingo: whenever any bingo is available, play the best one,
	// whatever rank it sits at.
	neverMissBingo bool
}

type persona struct {
	base personaRule
	// Conditional rules, checked in order; the first that applies replaces
	// base for this turn.
	when []personaRule
}

// personaRegistry is keyed by the id the frontend sends (opponents.js).
var personaRegistry = map[string]persona{
	// Diana: strong and steady - plays the best or second-best move at
	// random and never misses a bingo - until she falls 40+ behind. Then
	// she gets frustrated and sloppy: anywhere from the 6th to the 15th
	// best move, bingos included or not.
	"diana": {
		base: personaRule{mood: "calm", ranks: []int{1, 2}, neverMissBingo: true},
		when: []personaRule{
			{behindBy: 40, mood: "frustrated", rankMin: 6, rankMax: 15},
		},
	},
}

// botTurnCandidateCount is how many ranked moves /bot-turn considers - deep
// enough for any persona's worst rank and to find a bingo ranked below it.
const botTurnCandidateCount = 200

func (p persona) ruleFor(myScore, opponentScore int) personaRule {
	deficit := opponentScore - myScore
	for _, rule := range p.when {
		if rule.behindBy > 0 && deficit >= rule.behindBy {
			return rule
		}
	}
	return p.base
}

func (rule personaRule) pickRank() int {
	if len(rule.ranks) > 0 {
		return rule.ranks[rand.Intn(len(rule.ranks))]
	}
	if rule.rankMax > rule.rankMin {
		return rule.rankMin + rand.Intn(rule.rankMax-rule.rankMin+1)
	}
	return rule.rankMin
}

// decidePersonaMove picks from best-first `moves`. Returns the index into
// moves (-1 when there are none) and the mood the turn was played in.
func decidePersonaMove(p persona, moves []Move, myScore, opponentScore int) (int, string) {
	rule := p.ruleFor(myScore, opponentScore)
	if len(moves) == 0 {
		return -1, rule.mood
	}
	if rule.neverMissBingo {
		for i := range moves {
			if isBingoWord(moves[i]) {
				return i, rule.mood
			}
		}
	}
	rank := rule.pickRank()
	// Fewer candidates than the rank (late game): play the weakest one
	// available rather than jumping to the best.
	if rank > len(moves) {
		rank = len(moves)
	}
	if rank < 1 {
		rank = 1
	}
	return rank - 1, rule.mood
}

type BotTurnRequest struct {
	GenerateMovesRequest
	Persona       string `json:"persona"`
	MyScore       int    `json:"myScore"`
	OpponentScore int    `json:"opponentScore"`
}

type BotTurnResponse struct {
	// "play", "exchange" or "pass" (no legal move).
	Action string `json:"action"`
	Move   *Move  `json:"move,omitempty"`
	// Rank is the chosen move's 1-based position in the ranked list.
	Rank    int    `json:"rank,omitempty"`
	Mood    string `json:"mood,omitempty"`
	Persona string `json:"persona"`
	Lexicon string `json:"lexicon,omitempty"`
}

func botTurnHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req BotTurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	p, ok := personaRegistry[req.Persona]
	if !ok {
		http.Error(w, "Unknown persona", http.StatusBadRequest)
		return
	}
	if msg := validateMovesRequest(&req.GenerateMovesRequest); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	req.TopN = botTurnCandidateCount
	moves, _, lexiconName, err := rankMovesForRequest(req.GenerateMovesRequest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	idx, mood := decidePersonaMove(p, moves, req.MyScore, req.OpponentScore)
	resp := BotTurnResponse{Action: "pass", Mood: mood, Persona: req.Persona, Lexicon: lexiconName}
	if idx >= 0 {
		chosen := moves[idx]
		resp.Move = &chosen
		resp.Rank = idx + 1
		resp.Action = "play"
		if chosen.IsExchange {
			resp.Action = "exchange"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
