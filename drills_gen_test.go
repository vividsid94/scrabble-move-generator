package main

// Drill question generator - local only (a test file, never in the server
// build). Writes JSON question banks into the whiffers repo's
// public/drills/ for the Drills page to read as static files:
//
//	go test -run TestGenerateDefenseDrills -v -timeout 0
//
// Env: DRILLS_OUT (default ../whiffers/public/drills), DRILLS_TARGET
// (questions to keep, default 300), DRILLS_SAMPLES (opponent racks per
// move, default 300).
//
// Defense: "which of these two moves gives up less?" Positions come from
// Wally-vs-Wally self-play. For each position, the top moves by equity are
// played, and the opponent answers with Wally's best move against
// DRILLS_SAMPLES random racks from the unseen tiles; the average score of
// those answers is what the move "gives up". A position becomes a question
// only when two moves are close on equity (so the score doesn't give it
// away) but clearly apart on what they give up.

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/kwg"
)

const (
	defenseMaxEquityGap  = 5.0 // the two moves this close on score + leave...
	defenseMinGivenUpGap = 6.0 // ...but at least this far apart on points given up
	defenseCandidates    = 8   // top moves by equity considered per position
	defenseMinScore      = 8   // both moves score at least this
	drillChunkSize       = 100
)

type drillReply struct {
	Position string     `json:"position"`
	Word     string     `json:"word"`
	Score    int        `json:"score"`
	Share    float64    `json:"share"` // how often it was the answer
	Tiles    []MoveTile `json:"tiles"`
}

// drillHotSquare is the bonus square the opponent's replies use most
// after a move - the "why" behind what it gives up.
type drillHotSquare struct {
	Square string  `json:"square"` // e.g. "15A"
	Row    int     `json:"row"`
	Col    int     `json:"col"`
	Kind   string  `json:"kind"`  // "tw" | "dw" | "tl" | "dl"
	Share  float64 `json:"share"` // fraction of replies that covered it
}

// premiumKind is the standard board's bonus squares.
var premiumKind = func() map[[2]int]string {
	m := map[[2]int]string{}
	put := func(kind string, list [][2]int) {
		for _, rc := range list {
			r, c := rc[0], rc[1]
			for _, p := range [][2]int{{r, c}, {c, r}, {14 - r, c}, {r, 14 - c}, {14 - r, 14 - c}, {c, 14 - r}, {14 - c, r}, {14 - c, 14 - r}} {
				m[p] = kind
			}
		}
	}
	put("tw", [][2]int{{0, 0}, {0, 7}})
	put("dw", [][2]int{{1, 1}, {2, 2}, {3, 3}, {4, 4}, {7, 7}})
	put("tl", [][2]int{{1, 5}, {5, 5}})
	put("dl", [][2]int{{0, 3}, {2, 6}, {3, 7}, {6, 6}})
	return m
}()

type defenseOption struct {
	Position string      `json:"position"`
	Word     string      `json:"word"`
	Score    int         `json:"score"`
	Leave    string      `json:"leave"`
	Equity   float64     `json:"equity"`
	Tiles    []MoveTile  `json:"tiles"`
	OppAvg   float64         `json:"oppAvg"`
	TopReply *drillReply     `json:"topReply,omitempty"`
	Hot      *drillHotSquare `json:"hot,omitempty"`
}

type defenseQuestion struct {
	ID       string           `json:"id"`
	Board    [][]string       `json:"board"` // lowercase = blank
	Rack     string           `json:"rack"`
	MyScore  int              `json:"myScore"`
	OppScore int              `json:"oppScore"`
	BagCount int              `json:"bagCount"`
	Options  [2]defenseOption `json:"options"`
	Answer   int              `json:"answer"` // index into Options: gives up less
	Samples  int              `json:"samples"`
}

type drillSnapshot struct {
	bd                *board.GameBoard
	rack              string
	unseen            []rune // bag + the opponent's rack, from the mover's view
	bagCount          int
	myScore, oppScore int
}

func boardStrings(bd *board.GameBoard) [][]string {
	out := make([][]string, 15)
	for r := 0; r < 15; r++ {
		out[r] = make([]string, 15)
		for c := 0; c < 15; c++ {
			ml := bd.GetLetter(r, c)
			if ml == 0 {
				continue
			}
			s := strings.ToUpper(ml.Unblank().UserVisible(alph, false))
			if ml.IsBlanked() {
				s = strings.ToLower(s)
			}
			out[r][c] = s
		}
	}
	return out
}

// selfPlaySnapshots plays Wally-vs-Wally games and sends positions worth
// asking about (mid-game, a real bag) to out until stop is set.
func selfPlaySnapshots(gd *kwg.KWG, rng *rand.Rand, out chan<- drillSnapshot, done <-chan struct{}, stop *atomic.Bool) {
	for !stop.Load() {
		bd := board.MakeBoard(board.CrosswordGameBoard)
		cross_set.GenAllCrossSets(bd, gd, ld)
		bd.UpdateAllAnchors()
		gen := movegen.NewGordonGenerator(gd, bd, ld)
		bag := []rune(standardTilePool)
		rng.Shuffle(len(bag), func(i, j int) { bag[i], bag[j] = bag[j], bag[i] })
		draw := func(n int) string {
			if n > len(bag) {
				n = len(bag)
			}
			s := string(bag[len(bag)-n:])
			bag = bag[:len(bag)-n]
			return s
		}
		racks := [2]string{draw(7), draw(7)}
		scores := [2]int{}
		p, scoreless := 0, 0
		for turn := 0; turn < 60 && !stop.Load(); turn++ {
			if turn >= 2 && len(bag) >= 15 && len(bag) <= 75 && rng.Float64() < 0.35 {
				select {
				case out <- drillSnapshot{bd: bd.Copy(), rack: racks[p], unseen: []rune(string(bag) + racks[1-p]), bagCount: len(bag), myScore: scores[p], oppScore: scores[1-p]}:
				case <-done:
					return
				}
			}
			c := champStaticBest(gen, racks[p], len(bag), rackPointValue(racks[1-p]))
			switch c.kind {
			case champPlay:
				bd.PlayMove(c.m)
				cross_set.UpdateCrossSetsForMove(bd, c.m, gd, ld)
				scores[p] += c.score
				scoreless = 0
				racks[p] = c.leave + draw(7-len([]rune(c.leave)))
			case champExchange:
				drawn := draw(7 - len([]rune(c.leave)))
				bag = append(bag, []rune(c.exch)...)
				rng.Shuffle(len(bag), func(i, j int) { bag[i], bag[j] = bag[j], bag[i] })
				racks[p] = c.leave + drawn
				scoreless++
			default:
				scoreless++
			}
			if (len(racks[p]) == 0 && len(bag) == 0) || scoreless >= 6 {
				break
			}
			p = 1 - p
		}
	}
}

// givenUp plays cand on a copy of the position and returns the opponent's
// average best-reply score over `samples` random racks, plus their most
// common reply.
func givenUp(gd *kwg.KWG, snap drillSnapshot, cand champChoice, samples int, rng *rand.Rand) (float64, *drillReply, *drillHotSquare) {
	bd := snap.bd.Copy()
	bd.PlayMove(cand.m)
	cross_set.UpdateCrossSetsForMove(bd, cand.m, gd, ld)
	gen := movegen.NewGordonGenerator(gd, bd, ld)
	bagAfter := snap.bagCount - cand.m.TilesPlayed()
	if bagAfter < 0 {
		bagAfter = 0
	}
	pool := make([]rune, len(snap.unseen))
	copy(pool, snap.unseen)
	type tally struct {
		count int
		reply drillReply
	}
	seen := map[string]*tally{}
	total := 0
	var covered [15][15]int
	for i := 0; i < samples; i++ {
		rng.Shuffle(len(pool), func(a, b int) { pool[a], pool[b] = pool[b], pool[a] })
		n := 7
		if len(pool) < n {
			n = len(pool)
		}
		oppRack := string(pool[:n])
		c := champStaticBest(gen, oppRack, bagAfter, 0)
		if c.kind != champPlay {
			continue // an exchange or pass gives up nothing
		}
		total += c.score
		key := c.m.ShortDescription()
		t, ok := seen[key]
		if !ok {
			d := toDetailedMove(c.m, bd, alph)
			t = &tally{reply: drillReply{Position: c.m.BoardCoords(), Word: moveWordFromString(c.m.String()), Score: c.score, Tiles: d.Tiles}}
			seen[key] = t
		}
		t.count++
		for _, tile := range t.reply.Tiles {
			if tile.IsNew {
				covered[tile.Row][tile.Col]++
			}
		}
	}
	var top *tally
	for _, t := range seen {
		if top == nil || t.count > top.count || (t.count == top.count && t.reply.Score > top.reply.Score) {
			top = t
		}
	}
	var reply *drillReply
	if top != nil {
		r := top.reply
		r.Share = float64(top.count) / float64(samples)
		reply = &r
	}
	// The bonus square replies use most; word multipliers count for more.
	var hot *drillHotSquare
	bestScore := 0.0
	weight := map[string]float64{"tw": 3, "dw": 2, "tl": 1.5, "dl": 1}
	for r := 0; r < 15; r++ {
		for c := 0; c < 15; c++ {
			kind, ok := premiumKind[[2]int{r, c}]
			if !ok || covered[r][c] == 0 {
				continue
			}
			if v := float64(covered[r][c]) * weight[kind]; v > bestScore {
				bestScore = v
				hot = &drillHotSquare{Square: fmt.Sprintf("%d%c", r+1, 'A'+c), Row: r, Col: c, Kind: kind, Share: math.Round(float64(covered[r][c])/float64(samples)*100) / 100}
			}
		}
	}
	return float64(total) / float64(samples), reply, hot
}

func defenseOptionFor(gd *kwg.KWG, snap drillSnapshot, c champChoice, oppAvg float64, reply *drillReply, hot *drillHotSquare) defenseOption {
	d := toDetailedMove(c.m, snap.bd, alph)
	return defenseOption{
		Position: c.m.BoardCoords(), Word: moveWordFromString(c.m.String()), Score: c.score, Leave: c.leave,
		Equity: math.Round(c.total*10) / 10, Tiles: d.Tiles, OppAvg: math.Round(oppAvg*10) / 10, TopReply: reply, Hot: hot,
	}
}

// defenseQuestionFrom returns a question for the position, or nil if no
// pair of moves makes a good one.
func defenseQuestionFrom(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) *defenseQuestion {
	var plays []champChoice
	for _, c := range champCandidates(gd, snap.bd, snap.rack, snap.bagCount) {
		if c.kind == champPlay && c.score >= defenseMinScore {
			plays = append(plays, c)
		}
		if len(plays) == defenseCandidates {
			break
		}
	}
	if len(plays) < 2 {
		return nil
	}
	avgs := make([]float64, len(plays))
	replies := make([]*drillReply, len(plays))
	hots := make([]*drillHotSquare, len(plays))
	for i, c := range plays {
		avgs[i], replies[i], hots[i] = givenUp(gd, snap, c, samples, rng)
	}
	bi, bj, bestGap := -1, -1, 0.0
	for i := 0; i < len(plays); i++ {
		for j := i + 1; j < len(plays); j++ {
			if math.Abs(plays[i].total-plays[j].total) > defenseMaxEquityGap {
				continue
			}
			if gap := math.Abs(avgs[i] - avgs[j]); gap >= defenseMinGivenUpGap && gap > bestGap {
				bi, bj, bestGap = i, j, gap
			}
		}
	}
	if bi < 0 {
		return nil
	}
	if rng.Intn(2) == 0 { // don't always put the stronger-equity move first
		bi, bj = bj, bi
	}
	q := &defenseQuestion{
		Board: boardStrings(snap.bd), Rack: snap.rack, MyScore: snap.myScore, OppScore: snap.oppScore, BagCount: snap.bagCount, Samples: samples,
		Options: [2]defenseOption{defenseOptionFor(gd, snap, plays[bi], avgs[bi], replies[bi], hots[bi]), defenseOptionFor(gd, snap, plays[bj], avgs[bj], replies[bj], hots[bj])},
	}
	if avgs[bj] < avgs[bi] {
		q.Answer = 1
	}
	return q
}

func writeDrillBank(dir, drill string, questions any, count int, extra map[string]any) error {
	out := filepath.Join(dir, drill)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(questions)
	var all []json.RawMessage
	_ = json.Unmarshal(b, &all)
	chunks := 0
	for start := 0; start < len(all); start += drillChunkSize {
		end := min(start+drillChunkSize, len(all))
		chunks++
		data, _ := json.Marshal(all[start:end])
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("%04d.json", chunks)), data, 0o644); err != nil {
			return err
		}
	}
	index := map[string]any{"drill": drill, "count": count, "chunkSize": drillChunkSize, "chunks": chunks, "generatedAt": time.Now().UTC().Format(time.RFC3339)}
	for k, v := range extra {
		index[k] = v
	}
	data, _ := json.MarshalIndent(index, "", "  ")
	return os.WriteFile(filepath.Join(out, "index.json"), data, 0o644)
}

// drillQuestion is any generated question; the runner numbers them.
type drillQuestion interface{ setID(string) }

func (q *defenseQuestion) setID(id string) { q.ID = id }

// runDrillGenerator feeds self-play positions to build (in parallel) until
// it has DRILLS_TARGET questions, then writes them as public/drills/<drill>/.
func runDrillGenerator(t *testing.T, drill, idPrefix string, build func(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) drillQuestion) {
	if err := initService(); err != nil {
		t.Fatal(err)
	}
	outDir := os.Getenv("DRILLS_OUT")
	if outDir == "" {
		outDir = filepath.Join("..", "whiffers", "public", "drills")
	}
	target := envInt("DRILLS_TARGET", 300)
	samples := envInt("DRILLS_SAMPLES", 300)
	gd, lexName, err := resolveLexicon(os.Getenv("DRILLS_LEXICON"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d questions, %d opponent racks per move, %s -> %s", drill, target, samples, lexName, outDir)

	var stop atomic.Bool
	snaps := make(chan drillSnapshot, 64)
	done := make(chan struct{})
	go selfPlaySnapshots(gd, rand.New(rand.NewSource(time.Now().UnixNano())), snaps, done, &stop)

	var mu sync.Mutex
	var questions []drillQuestion
	tried := 0
	start := time.Now()
	var wg sync.WaitGroup
	builders := envInt("DRILLS_BUILDERS", runtime.NumCPU())
	for w := 0; w < builders; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)*7919))
			for {
				var snap drillSnapshot
				select {
				case snap = <-snaps:
				case <-done:
					return
				}
				q := build(gd, snap, samples, rng)
				mu.Lock()
				tried++
				if q != nil && len(questions) < target {
					q.setID(fmt.Sprintf("%s-%05d", idPrefix, len(questions)+1))
					questions = append(questions, q)
					if len(questions)%25 == 0 {
						t.Logf("%d questions from %d positions (%.0fs)", len(questions), tried, time.Since(start).Seconds())
					}
					if len(questions) >= target {
						stop.Store(true)
					}
				}
				mu.Unlock()
			}
		}(w)
	}
	for !stop.Load() {
		time.Sleep(200 * time.Millisecond)
	}
	close(done)
	wg.Wait()

	if err := writeDrillBank(outDir, drill, questions, len(questions), map[string]any{"samples": samples, "lexicon": lexName}); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d %s questions from %d positions in %.0fs", len(questions), drill, tried, time.Since(start).Seconds())
}

func TestGenerateDefenseDrills(t *testing.T) {
	runDrillGenerator(t, "defense", "def", func(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) drillQuestion {
		if q := defenseQuestionFrom(gd, snap, samples, rng); q != nil {
			return q
		}
		return nil
	})
}
