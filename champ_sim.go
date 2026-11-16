package main

// Champ-like move ranking - a re-implementation of the method Quackle's
// "Championship Player" uses in the middle of the game (its SmartBogowin
// player, bogowinplayer.cpp), on this service's own move generator and
// leave values. Only that middle-game part: with 2 or fewer tiles in the
// bag Quackle hands over to a preendgame solver and then an endgame
// solver, and this endpoint simply doesn't apply there.
//
// For one position (your rack, the board, the unseen tiles, both scores):
//
//  1. Candidates: every word play (and exchange, with 7+ in the bag) ranked
//     by static equity - score + leave value - and the top 14 kept
//     (Quackle: 13 + the number of moves asked for, never more than 33).
//  2. One playout of a candidate: deal the opponent a random rack from the
//     unseen tiles and shuffle the bag, play the candidate, then let each
//     side play its static best move for 2 more plies (the opponent's
//     reply, then yours). Once the bag has 14 or fewer tiles, play on to
//     the end of the game instead.
//  3. Score the playout as a win chance: the real result if the game
//     ended, otherwise Quackle's win table (quackle_bogowin.txt, see
//     THIRD_PARTY_QUACKLE.md) looked up with the score difference plus
//     both players' leave values, and the tiles still unseen.
//  4. Iterations: the best static move gets minIterations playouts and
//     becomes the leader. Every other candidate gets minIterations too,
//     and only earns the extra (maxIterations - minIterations) if its
//     "bogopoints" (win chance turned back into an equivalent point lead)
//     plus a 95% margin (1.96 x 35 / sqrt(minIterations)) beat the
//     leader's - Quackle's way of not wasting time on hopeless moves. It
//     takes the lead if its win chance is higher.
//  5. The answer is the candidates sorted by win chance.
//
// Playouts run in parallel across CPU cores, each worker on its own copy
// of the board, so the same budget buys far more playouts than Quackle's
// single thread (its Championship Player: 132 / 330 in 66 seconds).

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"math"
	"math/rand"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	macondomove "github.com/domino14/macondo/move"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/kwg"
	"github.com/domino14/word-golib/tilemapping"
)

// --- The win table -----------------------------------------------------------

//go:embed quackle_bogowin.txt
var quackleBogowinRaw string

const (
	winTableMaxLead   = 300
	winTableMaxUnseen = 93
)

// winTable[lead+300][unseen]: the chance the player about to move wins.
var winTable [2*winTableMaxLead + 1][winTableMaxUnseen + 1]float64

func init() {
	sc := bufio.NewScanner(strings.NewReader(quackleBogowinRaw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		lead, err1 := strconv.Atoi(f[0])
		unseen, err2 := strconv.Atoi(f[1])
		win, err3 := strconv.ParseFloat(f[2], 64)
		if err1 != nil || err2 != nil || err3 != nil || lead < -winTableMaxLead || lead > winTableMaxLead || unseen < 1 || unseen > winTableMaxUnseen {
			continue
		}
		winTable[lead+winTableMaxLead][unseen] = win
	}
}

// winChance is Quackle's lookup: the lead cut to a whole number toward
// zero, unseen capped at 93, an exact cell (no interpolation), and a sure
// thing past +/-300 or with nothing unseen.
func winChance(lead float64, unseen int) float64 {
	l := int(lead)
	if unseen <= 0 {
		switch {
		case l > 0:
			return 1
		case l < 0:
			return 0
		}
		return 0.5
	}
	if unseen > winTableMaxUnseen {
		unseen = winTableMaxUnseen
	}
	if l < -winTableMaxLead {
		return 0
	}
	if l > winTableMaxLead {
		return 1
	}
	return winTable[l+winTableMaxLead][unseen]
}

// bogopoints turns a win chance back into the point lead that would give
// it (at `tiles` unseen, minus the current spread), so the pruning margin
// can be in points. Quackle's SmartBogowin::bogopoints.
func bogopoints(win, equity float64, spread, tiles int) float64 {
	if tiles > winTableMaxUnseen {
		tiles = winTableMaxUnseen
	}
	if tiles < 1 {
		tiles = 1
	}
	switch {
	case win >= 1, win >= winTable[2*winTableMaxLead][tiles]:
		return equity + 1000
	case win <= 0, win <= winTable[0][tiles]:
		return equity - 1000
	}
	for x := -winTableMaxLead; x < winTableMaxLead; x++ {
		lo, hi := winTable[x+winTableMaxLead][tiles], winTable[x+winTableMaxLead+1][tiles]
		if lo <= win && win <= hi {
			if hi == lo {
				return float64(x - spread)
			}
			return float64(x) + (win-lo)/(hi-lo) - float64(spread)
		}
	}
	return equity
}

// --- Moves --------------------------------------------------------------------

const (
	champPlay = iota
	champExchange
	champPass
)

// champChoice is one option for a turn: a word play, an exchange or a pass.
type champChoice struct {
	kind  int
	m     *macondomove.Move
	exch  string // tiles exchanged
	leave string // sorted tiles kept
	score int
	total float64 // static equity: score + leave value (or empty-bag equity)
}

// usedTilesOf is the rack tiles a play puts down ('?' for a blank),
// read straight off the move rather than through toDetailedMove.
func usedTilesOf(m *macondomove.Move) string {
	var b strings.Builder
	for _, ml := range m.Tiles() {
		if ml == 0 {
			continue // played through
		}
		if ml.IsBlanked() {
			b.WriteByte('?')
		} else {
			b.WriteString(strings.ToUpper(ml.UserVisible(alph, false)))
		}
	}
	return b.String()
}

func playChoice(m *macondomove.Move, rack string, bagLen, oppRackPoints int) champChoice {
	leave := sortLeaveString(removeRackFromPool(rack, usedTilesOf(m)))
	total := float64(m.Score()) + getLeaveValue(leave)
	if bagLen == 0 {
		total = emptyBagEquity(m.Score(), leave, oppRackPoints)
	}
	return champChoice{kind: champPlay, m: m, leave: leave, score: m.Score(), total: total}
}

// champStaticBest is the move a side plays inside a playout: the highest
// static equity, exchanges included with 7+ in the bag, else a pass.
func champStaticBest(gen *movegen.GordonGenerator, rack string, bagLen, oppRackPoints int) champChoice {
	best := champChoice{kind: champPass, leave: sortLeaveString(rack), total: math.Inf(-1)}
	for _, m := range gen.GenAll(tilemapping.RackFromString(rack, alph), false) {
		if m.Action() != macondomove.MoveTypePlay {
			continue
		}
		if c := playChoice(m, rack, bagLen, oppRackPoints); c.total > best.total {
			best = c
		}
	}
	if bagLen >= 7 {
		for _, ex := range allExchangeCandidates(rack) {
			if ex.total > best.total {
				best = champChoice{kind: champExchange, exch: ex.exchangeTiles, leave: ex.leave, total: ex.total}
			}
		}
	}
	return best
}

// champCandidates is every option at the root, best static equity first.
func champCandidates(gd *kwg.KWG, bd *board.GameBoard, rack string, bagLen int) []champChoice {
	gen := movegen.NewGordonGenerator(gd, bd, ld)
	var out []champChoice
	for _, m := range gen.GenAll(tilemapping.RackFromString(rack, alph), false) {
		if m.Action() == macondomove.MoveTypePlay {
			out = append(out, playChoice(m, rack, bagLen, 0))
		}
	}
	if bagLen >= 7 {
		for _, ex := range allExchangeCandidates(rack) {
			out = append(out, champChoice{kind: champExchange, exch: ex.exchangeTiles, leave: ex.leave, total: ex.total})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].total > out[j].total })
	return out
}

// --- Playouts -------------------------------------------------------------------

type champRoot struct {
	gd                *kwg.KWG
	bd                *board.GameBoard
	rack              string
	unseen            []rune
	myScore, oppScore int
	plies             int // plies after the candidate; -1 = to the end of the game
}

// champWorker owns a board and a move generator for its playouts, so the
// root board is only ever read.
type champWorker struct {
	bd  *board.GameBoard
	gen *movegen.GordonGenerator
	rng *rand.Rand
}

func newChampWorker(root *champRoot, seed int64) *champWorker {
	bd := root.bd.Copy()
	return &champWorker{bd: bd, gen: movegen.NewGordonGenerator(root.gd, bd, ld), rng: rand.New(rand.NewSource(seed))}
}

// playout plays one random continuation of cand and returns the win
// chance for the root player and the final score difference.
func (w *champWorker) playout(root *champRoot, cand *champChoice) (float64, float64) {
	w.bd.CopyFrom(root.bd)
	pool := make([]rune, len(root.unseen))
	copy(pool, root.unseen)
	w.rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	oppN := 7
	if len(pool) < oppN {
		oppN = len(pool)
	}
	racks := [2]string{root.rack, string(pool[:oppN])}
	bag := pool[oppN:]
	scores := [2]int{root.myScore, root.oppScore}
	var lastLeave [2]float64
	scoreless := 0
	ended := false

	draw := func(n int) string {
		if n > len(bag) {
			n = len(bag)
		}
		if n <= 0 {
			return ""
		}
		s := string(bag[len(bag)-n:])
		bag = bag[:len(bag)-n]
		return s
	}
	apply := func(p int, c *champChoice) {
		switch c.kind {
		case champPlay:
			w.bd.PlayMove(c.m)
			cross_set.UpdateCrossSetsForMove(w.bd, c.m, root.gd, ld)
			scores[p] += c.score
			scoreless = 0
			racks[p] = c.leave + draw(7-len([]rune(c.leave)))
		case champExchange:
			drawn := draw(7 - len([]rune(c.leave)))
			for _, t := range c.exch { // back into the bag at random spots
				bag = append(bag, t)
				k := w.rng.Intn(len(bag))
				bag[k], bag[len(bag)-1] = bag[len(bag)-1], bag[k]
			}
			racks[p] = c.leave + drawn
			scoreless++
		default:
			scoreless++
		}
		lastLeave[p] = getLeaveValue(c.leave)
		switch {
		case len(racks[p]) == 0 && len(bag) == 0:
			scores[p] += 2 * rackPointValue(racks[1-p])
			ended = true
		case scoreless >= 6:
			scores[0] -= rackPointValue(racks[0])
			scores[1] -= rackPointValue(racks[1])
			ended = true
		}
	}

	apply(0, cand)
	maxTurns := root.plies
	if maxTurns < 0 {
		maxTurns = 100
	}
	p := 1
	for turn := 0; !ended && turn < maxTurns; turn++ {
		c := champStaticBest(w.gen, racks[p], len(bag), rackPointValue(racks[1-p]))
		// The move list is reused by the next GenAll, so play it right away.
		apply(p, &c)
		p = 1 - p
	}

	spread := float64(scores[0] - scores[1])
	if ended {
		switch {
		case spread > 0:
			return 1, spread
		case spread < 0:
			return 0, spread
		}
		return 0.5, spread
	}
	// Leaves count too: yours added, the opponent's taken away. The table is
	// for the player about to move, so flip it when that's the opponent.
	lead := spread + lastLeave[0] - lastLeave[1]
	unseen := len(bag) + 7
	if p == 0 {
		return winChance(lead, unseen), spread
	}
	return 1 - winChance(-lead, unseen), spread
}

// champStat is one candidate's running totals.
type champStat struct {
	choice     champChoice
	staticRank int
	n          int
	winSum     float64
	spreadSum  float64
}

func (s *champStat) win() float64 {
	if s.n == 0 {
		return 0
	}
	return s.winSum / float64(s.n)
}

// runPlayouts adds up to n playouts of one candidate, split across the
// workers, stopping early at the deadline.
func runPlayouts(workers []*champWorker, root *champRoot, st *champStat, n int, deadline time.Time) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	per := (n + len(workers) - 1) / len(workers)
	remaining := n
	for _, w := range workers {
		count := per
		if count > remaining {
			count = remaining
		}
		remaining -= count
		if count <= 0 {
			break
		}
		wg.Add(1)
		go func(w *champWorker, count int) {
			defer wg.Done()
			var wins, spreads float64
			done := 0
			for i := 0; i < count; i++ {
				if i%4 == 0 && time.Now().After(deadline) {
					break
				}
				win, spread := w.playout(root, &st.choice)
				wins += win
				spreads += spread
				done++
			}
			mu.Lock()
			st.n += done
			st.winSum += wins
			st.spreadSum += spreads
			mu.Unlock()
		}(w, count)
	}
	wg.Wait()
}

type champConfig struct {
	candidates, minIterations, maxIterations int
	budget                                   time.Duration
}

// champRank is SmartBogowin's loop (step 4 above). It returns the
// candidates it reached, best win chance first, and whether time ran out.
func champRank(root *champRoot, cands []champChoice, cfg champConfig) ([]*champStat, bool) {
	deadline := time.Now().Add(cfg.budget)
	nWorkers := runtime.NumCPU()
	workers := make([]*champWorker, nWorkers)
	seed := time.Now().UnixNano()
	for i := range workers {
		workers[i] = newChampWorker(root, seed+int64(i)*7919)
	}
	if len(cands) > cfg.candidates {
		cands = cands[:cfg.candidates]
	}
	spread := root.myScore - root.oppScore
	tiles := len(root.unseen)
	margin := 1.96 * 35 / math.Sqrt(float64(cfg.minIterations))

	var simmed []*champStat
	var best *champStat
	var bestBP float64
	timedOut := false
	for i := range cands {
		if time.Now().After(deadline) {
			timedOut = true
			break
		}
		st := &champStat{choice: cands[i], staticRank: i + 1}
		runPlayouts(workers, root, st, cfg.minIterations, deadline)
		bp := bogopoints(st.win(), st.choice.total, spread, tiles)
		if best == nil {
			best, bestBP = st, bp
		} else if bp+margin > bestBP {
			runPlayouts(workers, root, st, cfg.maxIterations-cfg.minIterations, deadline)
			bp = bogopoints(st.win(), st.choice.total, spread, tiles)
			if st.win() > best.win() {
				best, bestBP = st, bp
			}
		}
		simmed = append(simmed, st)
	}
	sort.SliceStable(simmed, func(i, j int) bool { return simmed[i].win() > simmed[j].win() })
	return simmed, timedOut
}

// --- The endpoint -----------------------------------------------------------------

type ChampMovesRequest struct {
	Rack  string     `json:"rack"`
	Board [][]string `json:"board"`
	// Unseen is every tile not on the board or your rack: the bag plus the
	// opponent's rack, e.g. "AEEIOR?..." (blanks as '?').
	Unseen        string `json:"unseen"`
	MyScore       int    `json:"myScore"`
	OpponentScore int    `json:"opponentScore"`
	Lexicon       string `json:"lexicon,omitempty"`
	// Optional tuning; defaults are below.
	Candidates    int     `json:"candidates,omitempty"`
	MinIterations int     `json:"minIterations,omitempty"`
	MaxIterations int     `json:"maxIterations,omitempty"`
	Seconds       float64 `json:"seconds,omitempty"`
}

type ChampCandidate struct {
	Move Move `json:"move"`
	// WinChance is 0-1 for the player to move.
	WinChance float64 `json:"winChance"`
	// AvgSpreadGain is the average change in the score difference over the
	// playout (positive = you gained on the opponent).
	AvgSpreadGain float64 `json:"avgSpreadGain"`
	Iterations    int     `json:"iterations"`
	// StaticRank is where the move stood by score + leave alone (1 = best).
	StaticRank int `json:"staticRank"`
}

type ChampMovesResponse struct {
	Moves      []ChampCandidate `json:"moves"`
	Plies      int              `json:"plies"` // -1 = played to the end of the game
	Iterations int              `json:"iterations"`
	ElapsedMs  int64            `json:"elapsedMs"`
	TimedOut   bool             `json:"timedOut"`
	Workers    int              `json:"workers"`
	Lexicon    string           `json:"lexicon,omitempty"`
}

const (
	champDefaultCandidates = 14
	champMaxCandidates     = 33
	champDefaultMinIter    = 132 // Quackle's Championship Player
	champDefaultMaxIter    = 330
	champDefaultSeconds    = 8.0
	champMaxSeconds        = 30.0
)

func champChoiceToMove(c champChoice, bd *board.GameBoard) Move {
	if c.kind == champExchange {
		return Move{
			Position: "Exchange", Word: "Exchange " + c.exch, Leave: c.leave, LeaveValue: c.total, TotalValue: c.total,
			IsExchange: true, Direction: "exchange", StartPosition: "Exchange", Tiles: exchangeTilesToMoveTiles(c.exch),
		}
	}
	detailed := toDetailedMove(c.m, bd, alph)
	return Move{
		Position: c.m.BoardCoords(), Word: moveWordFromString(c.m.String()), Score: c.score,
		Leave: c.leave, LeaveValue: c.total - float64(c.score), TotalValue: c.total,
		Direction: detailed.Direction, StartPosition: detailed.StartPosition, Tiles: detailed.Tiles,
	}
}

func champMovesHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ChampMovesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	req.Rack = strings.ToUpper(req.Rack)
	req.Unseen = strings.ToUpper(req.Unseen)
	rackLen, unseenLen := len([]rune(req.Rack)), len([]rune(req.Unseen))
	if rackLen == 0 || rackLen > 7 {
		http.Error(w, "Rack must have 1-7 tiles", http.StatusBadRequest)
		return
	}
	if len(req.Board) != 15 {
		http.Error(w, "Board must have 15 rows", http.StatusBadRequest)
		return
	}
	for i := range req.Board {
		if len(req.Board[i]) != 15 {
			http.Error(w, "Each board row must have 15 columns", http.StatusBadRequest)
			return
		}
	}
	// The middle game only: the bag (unseen minus the opponent's 7) needs 3+.
	if unseenLen-7 < 3 {
		http.Error(w, "Champ sims need 3 or more tiles in the bag", http.StatusBadRequest)
		return
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	bd := board.MakeBoard(board.CrosswordGameBoard)
	tilesPlayed := 0
	for row := 0; row < 15; row++ {
		for col := 0; col < 15; col++ {
			if tile := req.Board[row][col]; tile != "" {
				if ml, err := alph.Val(tile); err == nil {
					bd.SetLetter(row, col, ml)
					tilesPlayed++
				}
			}
		}
	}
	if tilesPlayed+rackLen+unseenLen != 100 {
		http.Error(w, "Board + rack + unseen must add up to 100 tiles", http.StatusBadRequest)
		return
	}
	bd.TestSetTilesPlayed(tilesPlayed)
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	cfg := champConfig{candidates: champDefaultCandidates, minIterations: champDefaultMinIter, maxIterations: champDefaultMaxIter}
	if req.Candidates > 0 {
		cfg.candidates = min(req.Candidates, champMaxCandidates)
	}
	if req.MinIterations > 0 {
		cfg.minIterations = req.MinIterations
	}
	if req.MaxIterations > 0 {
		cfg.maxIterations = req.MaxIterations
	}
	if cfg.maxIterations < cfg.minIterations {
		cfg.maxIterations = cfg.minIterations
	}
	seconds := champDefaultSeconds
	if req.Seconds > 0 {
		seconds = math.Min(req.Seconds, champMaxSeconds)
	}
	cfg.budget = time.Duration(seconds * float64(time.Second))

	bagLen := unseenLen - 7
	root := &champRoot{gd: gd, bd: bd, rack: req.Rack, unseen: []rune(req.Unseen), myScore: req.MyScore, oppScore: req.OpponentScore, plies: 2}
	if bagLen <= 14 {
		root.plies = -1
	}

	start := time.Now()
	cands := champCandidates(gd, bd, req.Rack, bagLen)
	ranked, timedOut := champRank(root, cands, cfg)

	resp := ChampMovesResponse{Plies: root.plies, TimedOut: timedOut, Workers: runtime.NumCPU(), Lexicon: lexiconName}
	for _, st := range ranked {
		resp.Iterations += st.n
		gain := 0.0
		if st.n > 0 {
			gain = st.spreadSum/float64(st.n) - float64(req.MyScore-req.OpponentScore)
		}
		resp.Moves = append(resp.Moves, ChampCandidate{
			Move: champChoiceToMove(st.choice, bd), WinChance: st.win(), AvgSpreadGain: gain,
			Iterations: st.n, StaticRank: st.staticRank,
		})
	}
	resp.ElapsedMs = time.Since(start).Milliseconds()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
