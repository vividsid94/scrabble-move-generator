package main

// Drawbacks for server-run games (whiffers-api's "drawbacks" mode): the Go
// rulebook judged one turn at a time, instead of only inside the Sandbox's
// simulateOneGame. whiffers-api keeps the game (board, racks, bag, history,
// per-game and per-turn rolls) and asks here:
//
//   POST /drawback-moves  - which moves a player's drawback allows this turn
//                           (the best few, for the bot), whether a proposed
//                           move is allowed, and what the panel needs (how
//                           many plays fit, whether exchanging is allowed,
//                           whether a "must" drawback bites, opened squares).
//   POST /drawback-loss   - after a turn (or at game end): which players'
//                           lose conditions are now broken.
//
// Turns use simulate.go's SimTurn shape, so every rule in drawbacks.go reads
// a server game's history exactly the way it reads a Sandbox game's.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/kwg"
	"github.com/domino14/word-golib/tilemapping"
)

type DrawbackMovesRequest struct {
	Board      [][]string `json:"board"` // 15x15, "" empty, lowercase = blank
	Rack       string     `json:"rack"`
	Lexicon    string     `json:"lexicon,omitempty"`
	PoolSize   int        `json:"poolSize"`
	DrawbackID int        `json:"drawbackId"`
	Player     int        `json:"player"` // 1 or 2 - whose turn, as in Turns
	Turns      []SimTurn  `json:"turns"`
	// BoardBeforeLastPlay: the board before the most recent word play
	// (I'll Take That compares what this rack could reach then and now).
	BoardBeforeLastPlay [][]string `json:"boardBeforeLastPlay,omitempty"`
	// This turn's rolls, made and kept by the caller: Shifting Sands'
	// off-limits squares ([row, col]) and Quota's two numbers.
	BlockedSquares [][2]int `json:"blockedSquares,omitempty"`
	Quota          []int    `json:"quota,omitempty"`
	// Drop-Zone's quarter, Flavor Of The Day's kind, Peer Pressure's tiles.
	Quadrant    *int   `json:"quadrant,omitempty"`
	Flavor      string `json:"flavor,omitempty"`
	Highlighted string `json:"highlighted,omitempty"`
	// Devil On Your Shoulder: the caller's count says this turn must obey.
	MustObeyDevil bool `json:"mustObeyDevil,omitempty"`
	// ForBot: pick like a bot - avoid moves that break its own lose rule,
	// and its own Minefield zones, when it has a safe option. A human is
	// never stopped from making a losing move (the client warns instead).
	ForBot bool           `json:"forBot,omitempty"`
	Zones  []claustroZone `json:"zones,omitempty"` // the player's own Minefield zones
	// Proposed: a move to judge (the human's) - its new tiles, or the
	// letters of an exchange ("?" for a blank).
	Proposed         []MoveTile `json:"proposed,omitempty"`
	ProposedExchange string     `json:"proposedExchange,omitempty"`
	// Limit: how many allowed moves to return, best first (0 = none).
	Limit int `json:"limit,omitempty"`
}

// DrawbackMove is one allowed move. Score is what it scores for this player
// under the drawback (Nerf War rescores); FullScore is the normal score.
// Tiles is the main word (new tiles and play-throughs); WordStarts is the
// first letter of every word it forms - the server stores both in its
// history so later turns can be judged.
type DrawbackMove struct {
	Word          string     `json:"word"`
	Score         int        `json:"score"`
	FullScore     int        `json:"fullScore"`
	Direction     string     `json:"direction,omitempty"`
	StartPosition string     `json:"startPosition,omitempty"`
	Tiles         []MoveTile `json:"tiles,omitempty"`
	IsExchange    bool       `json:"isExchange,omitempty"`
	ExchangeTiles string     `json:"exchangeTiles,omitempty"`
	Leave         string     `json:"leave"`
	TotalValue    float64    `json:"totalValue"`
	WordStarts    []string   `json:"wordStarts,omitempty"`
}

type DrawbackMovesResponse struct {
	Moves           []DrawbackMove `json:"moves"`
	PlayableCount   int            `json:"playableCount"`
	CanExchange     bool           `json:"canExchange"`
	Forced          *bool          `json:"forced,omitempty"`
	Opened          [][2]int       `json:"opened,omitempty"`
	Proposed        *DrawbackMove  `json:"proposed,omitempty"`
	// Devil On Your Shoulder: the devil's suggestion this turn (the worst
	// word play), so the caller can show it and tell whether it was obeyed.
	Devil *DrawbackMove `json:"devil,omitempty"`
	// Restlessness: each rack tile's count.
	Timers []RestlessTile `json:"timers,omitempty"`
	ProposedAllowed bool           `json:"proposedAllowed"`
	ProposedReason  string         `json:"proposedReason,omitempty"`
	Debug           string         `json:"debug,omitempty"`
}

// boardFromStrings builds a macondo board (cross-sets and anchors ready for
// move generation) from the 15x15 string grid every endpoint here takes.
func boardFromStrings(cells [][]string, gd *kwg.KWG) (*board.GameBoard, int) {
	bd := board.MakeBoard(board.CrosswordGameBoard)
	played := 0
	for r := 0; r < 15 && r < len(cells); r++ {
		for c := 0; c < 15 && c < len(cells[r]); c++ {
			if cells[r][c] == "" {
				continue
			}
			if ml, err := alph.Val(cells[r][c]); err == nil {
				bd.SetLetter(r, c, ml)
				played++
			}
		}
	}
	bd.TestSetTilesPlayed(played)
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()
	return bd, played
}

// turnCandidates is every word play for rack on bd plus (with 7+ in the bag)
// every exchange, scored by score + leave value (Quackle's empty-bag equity
// when the bag is empty) - the same scoring the Sandbox's bots use.
func turnCandidates(bd *board.GameBoard, gd *kwg.KWG, rack string, poolSize int) []scoredCandidate {
	rackObj := tilemapping.RackFromString(rack, alph)
	moves := movegen.NewGordonGenerator(gd, bd, ld).GenAll(rackObj, false)
	emptyBag, opponentRackPoints := emptyBagFromBoard(bd, rack)
	candidates := make([]scoredCandidate, 0, len(moves))
	for _, m := range moves {
		if !strings.Contains(m.String(), "play word:") {
			continue
		}
		detailed := toDetailedMove(m, bd, alph)
		var used []string
		for _, t := range detailed.Tiles {
			if t.IsNew {
				if t.IsBlank {
					used = append(used, "?")
				} else {
					used = append(used, t.Letter)
				}
			}
		}
		leave := sortLeaveString(removeRackFromPool(rack, strings.Join(used, "")))
		total := float64(detailed.Score) + getLeaveValue(leave)
		if emptyBag {
			total = emptyBagEquity(detailed.Score, leave, opponentRackPoints)
		}
		candidates = append(candidates, scoredCandidate{
			move: m, detailed: detailed, leave: leave, total: total, baselineTotal: total, isBingo: len(used) == 7,
		})
	}
	if poolSize >= 7 {
		candidates = append(candidates, allExchangeCandidates(rack)...)
	}
	return candidates
}

// withMirroredOpeners: on an empty board the generator returns only the
// across version of each opener (the position is symmetric). Direction rules
// need the down versions too, so each across opener gets its mirror added.
func withMirroredOpeners(candidates []scoredCandidate) []scoredCandidate {
	out := append([]scoredCandidate{}, candidates...)
	for _, c := range candidates {
		if c.isExchange || c.detailed.Direction == "down" {
			continue
		}
		d := *c.detailed
		d.Direction = "down"
		d.Tiles = make([]MoveTile, len(c.detailed.Tiles))
		for i, t := range c.detailed.Tiles {
			t.Row, t.Col = t.Col, t.Row
			d.Tiles[i] = t
		}
		d.StartPosition = fmt.Sprintf("%c%d", 'A'+d.Tiles[0].Col, d.Tiles[0].Row+1)
		mirror := c
		mirror.move = nil
		mirror.detailed = &d
		out = append(out, mirror)
	}
	return out
}

// newTilesKey identifies a play by the tiles it places (row, col, letter,
// blank), in any order - how a proposed move is matched to a candidate.
func newTilesKey(tiles []MoveTile) string {
	parts := make([]string, 0, len(tiles))
	for _, t := range tiles {
		if !t.IsNew {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d,%d,%s,%t", t.Row, t.Col, strings.ToUpper(t.Letter), t.IsBlank))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// mirroredKey is newTilesKey for the same tiles with row and col swapped.
func mirroredKey(tiles []MoveTile) string {
	swapped := make([]MoveTile, len(tiles))
	for i, t := range tiles {
		t.Row, t.Col = t.Col, t.Row
		swapped[i] = t
	}
	return newTilesKey(swapped)
}

func toDrawbackMove(c *scoredCandidate, bd *board.GameBoard, fullScores map[string]int) DrawbackMove {
	if c.isExchange {
		return DrawbackMove{IsExchange: true, ExchangeTiles: c.exchangeTiles, Leave: c.leave, TotalValue: c.total, Word: "Exchange"}
	}
	key := newTilesKey(c.detailed.Tiles)
	full, ok := fullScores[key]
	if !ok {
		full = c.detailed.Score
	}
	return DrawbackMove{
		Word: c.detailed.Word, Score: c.detailed.Score, FullScore: full,
		Direction: c.detailed.Direction, StartPosition: c.detailed.StartPosition, Tiles: c.detailed.Tiles,
		Leave: c.leave, TotalValue: c.total, WordStarts: wordStartLetters(c, bd),
	}
}

func drawbackMovesHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req DrawbackMovesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	resp, err := judgeDrawbackTurn(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func judgeDrawbackTurn(req DrawbackMovesRequest) (*DrawbackMovesResponse, error) {
	if len(req.Board) != 15 {
		return nil, fmt.Errorf("board must have 15 rows")
	}
	if req.Player != 1 && req.Player != 2 {
		return nil, fmt.Errorf("player must be 1 or 2")
	}
	def, ok := drawbackByID[req.DrawbackID]
	if !ok {
		return nil, fmt.Errorf("unknown drawbackId %d", req.DrawbackID)
	}
	gd, _, err := resolveLexicon(req.Lexicon)
	if err != nil {
		return nil, err
	}
	rule := def.Rule
	bd, played := boardFromStrings(req.Board, gd)
	all := turnCandidates(bd, gd, req.Rack, req.PoolSize)
	if played == 0 {
		all = withMirroredOpeners(all)
	}

	// Normal scores, kept for Nerf War (the filter rescores its copies).
	fullScores := make(map[string]int, len(all))
	for i := range all {
		if !all[i].isExchange {
			fullScores[newTilesKey(all[i].detailed.Tiles)] = all[i].detailed.Score
		}
	}

	extras := drawbackExtras{}
	if len(req.BlockedSquares) > 0 {
		extras.Blocked = make(map[int]bool, len(req.BlockedSquares))
		for _, sq := range req.BlockedSquares {
			extras.Blocked[sq[0]*15+sq[1]] = true
		}
	}
	if len(req.Quota) == 2 {
		extras.Quota = req.Quota
	}
	extras.Quadrant = req.Quadrant
	extras.MustObeyDevil = req.MustObeyDevil
	extras.Flavor = req.Flavor
	extras.Highlighted = req.Highlighted
	if rule.Type == "mustCoverOpenedPremium" && len(req.BoardBeforeLastPlay) == 15 {
		before, beforePlayed := boardFromStrings(req.BoardBeforeLastPlay, gd)
		beforeCands := turnCandidates(before, gd, req.Rack, req.PoolSize)
		if beforePlayed == 0 {
			beforeCands = withMirroredOpeners(beforeCands)
		}
		extras.Opened = openedPremiumSquares(req.Turns, req.Player, bd, reachableFromCandidates(all), reachableFromCandidates(beforeCands))
	}

	// A human is never stopped from making a losing move; only a bot steers
	// clear of its own lose rule (the filter's lose-rule branch is that bot
	// behaviour).
	var allowed []scoredCandidate
	debug := ""
	if isLoseRule(rule) && !req.ForBot {
		allowed = all
	} else {
		allowed, debug = filterCandidatesByDrawback(append([]scoredCandidate{}, all...), rule, req.Rack, bd, req.PoolSize, req.Turns, req.Player, extras)
	}

	// Settle For Less: a mirrored opener is the same play as its original,
	// so it goes when its original was one of the excluded top plays.
	if rule.Type == "excludeTopNCandidates" && played == 0 {
		kept := map[string]bool{}
		for i := range allowed {
			if !allowed[i].isExchange && allowed[i].detailed.Direction != "down" {
				kept[newTilesKey(allowed[i].detailed.Tiles)] = true
			}
		}
		filtered := allowed[:0:0]
		for _, c := range allowed {
			if !c.isExchange && c.detailed.Direction == "down" && !kept[mirroredKey(c.detailed.Tiles)] {
				continue
			}
			filtered = append(filtered, c)
		}
		allowed = filtered
	}

	// A bot also keeps out of its own Minefield zones when it can.
	if req.ForBot && len(req.Zones) > 0 {
		safe := make([]scoredCandidate, 0, len(allowed))
		for _, c := range allowed {
			if c.isExchange || !placesInZone(c.detailed.Tiles, req.Zones) {
				safe = append(safe, c)
			}
		}
		if len(safe) > 0 {
			allowed = safe
		}
	}

	sort.SliceStable(allowed, func(i, j int) bool { return allowed[i].total > allowed[j].total })

	resp := &DrawbackMovesResponse{Debug: debug, Moves: []DrawbackMove{}}
	if rule.Type == "restlessTiles" {
		resp.Timers = restlessTimers(req.Turns, req.Player, req.Rack, int(rule.Value))
	}
	if rule.Type == "obeyDevil" {
		if devil := devilMove(all); devil != nil {
			m := toDrawbackMove(devil, bd, fullScores)
			resp.Devil = &m
		}
	}
	for i := range allowed {
		if allowed[i].isExchange {
			resp.CanExchange = true
		} else {
			resp.PlayableCount++
		}
	}
	if rule.Forcing {
		bites := false
		for i := range all {
			c := &all[i]
			if c.isExchange {
				continue
			}
			if rule.Type == "mustCoverOpenedPremium" {
				for _, t := range c.detailed.Tiles {
					if t.IsNew && extras.Opened[rcPos{t.Row, t.Col}] {
						bites = true
					}
				}
			} else if evaluateDrawback(rule, c, req.Rack, bd, req.PoolSize, req.Turns, req.Player) {
				bites = true
			}
			if bites {
				break
			}
		}
		resp.Forced = &bites
	}
	for pos := range extras.Opened {
		resp.Opened = append(resp.Opened, [2]int{pos.row, pos.col})
	}
	for i := 0; i < len(allowed) && i < req.Limit; i++ {
		resp.Moves = append(resp.Moves, toDrawbackMove(&allowed[i], bd, fullScores))
	}

	// Judge the proposed move: found among the allowed moves, or not.
	if len(req.Proposed) > 0 || req.ProposedExchange != "" {
		judgeProposed(req, rule, def.Name, all, allowed, bd, fullScores, resp)
	}
	return resp, nil
}

func judgeProposed(req DrawbackMovesRequest, rule DrawbackRule, name string, all, allowed []scoredCandidate, bd *board.GameBoard, fullScores map[string]int, resp *DrawbackMovesResponse) {
	find := func(list []scoredCandidate) *scoredCandidate {
		if req.ProposedExchange != "" {
			want := sortLeaveString(strings.ToUpper(req.ProposedExchange))
			for i := range list {
				if list[i].isExchange && sortLeaveString(list[i].exchangeTiles) == want {
					return &list[i]
				}
			}
			return nil
		}
		proposed := make([]MoveTile, len(req.Proposed))
		for i, t := range req.Proposed {
			t.IsNew = true
			proposed[i] = t
		}
		want := newTilesKey(proposed)
		for i := range list {
			if !list[i].isExchange && newTilesKey(list[i].detailed.Tiles) == want {
				return &list[i]
			}
		}
		return nil
	}
	if c := find(allowed); c != nil {
		m := toDrawbackMove(c, bd, fullScores)
		resp.Proposed = &m
		resp.ProposedAllowed = true
		return
	}
	if c := find(all); c != nil {
		m := toDrawbackMove(c, bd, fullScores)
		resp.Proposed = &m
		resp.ProposedReason = fmt.Sprintf("Your drawback (%s) doesn't allow that move", name)
		return
	}
	resp.ProposedReason = "That isn't a legal move"
}

// reachableFromCandidates: every square some word play in candidates puts a
// new tile on.
func reachableFromCandidates(candidates []scoredCandidate) map[rcPos]bool {
	squares := map[rcPos]bool{}
	for i := range candidates {
		if candidates[i].isExchange {
			continue
		}
		for _, t := range candidates[i].detailed.Tiles {
			if t.IsNew {
				squares[rcPos{t.Row, t.Col}] = true
			}
		}
	}
	return squares
}

type DrawbackLossRequest struct {
	// DrawbackIDs[0] is player 1's drawback, [1] player 2's (0 = none).
	DrawbackIDs    [2]int    `json:"drawbackIds"`
	Turns          []SimTurn `json:"turns"` // including the turn just made
	PoolSizeBefore int       `json:"poolSizeBefore"`
	GameEnding     bool      `json:"gameEnding"` // this turn also ends the game normally
	AtGameEnd      bool      `json:"atGameEnd"`  // judge the end-of-game rules instead
}

type DrawbackLoss struct {
	Player int    `json:"player"`
	Reason string `json:"reason"`
}

type DrawbackLossResponse struct {
	Losses []DrawbackLoss `json:"losses"`
}

func drawbackLossHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req DrawbackLossRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	resp := DrawbackLossResponse{Losses: []DrawbackLoss{}}
	if !req.AtGameEnd && len(req.Turns) == 0 {
		http.Error(w, "turns required", http.StatusBadRequest)
		return
	}
	for i, id := range req.DrawbackIDs {
		def, ok := drawbackByID[id]
		if !ok || !isLoseRule(def.Rule) {
			continue
		}
		holder := i + 1
		var lost bool
		var reason string
		if req.AtGameEnd {
			lost, reason = drawbackLossAtGameEnd(def.Rule, req.Turns, holder)
		} else {
			lost, reason = drawbackLossAfterTurn(def.Rule, req.Turns[len(req.Turns)-1], req.PoolSizeBefore, req.Turns, holder, req.GameEnding)
		}
		if lost {
			resp.Losses = append(resp.Losses, DrawbackLoss{Player: holder, Reason: def.Name + ": " + reason})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
