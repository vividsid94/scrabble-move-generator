package main

// The Basics drills (drill generator, local only - see drills_gen_test.go).
// Quick questions with exact answers, no sims:
//
//	go test -run TestGenerateScoreDrills -v -timeout 0    // Score it
//	go test -run TestGenerateOutplayDrills -v -timeout 0  // Out-play maths
//	go test -run TestGenerateLeaveDrills -v -timeout 0    // Best leave
//	go test -run TestGenerateOpeningDrills -v -timeout 0  // Opening spot
//	go test -run TestGenerateOddsDrills -v -timeout 0     // Quick odds
//
// Score it: a real play on a real board; type its score. The breakdown
// (every word it makes, letter by letter) is worked out here and checked
// against the move generator's score, so the reveal can show the sum.
// Out-play maths: you go out in a real endgame; type the final spread
// (your play plus double their rack, NASPA style).
// Best leave: five leaves from one rack; which is worth most by our leave
// values (leaves.json, the ones Wally plays by).
// Opening spot: the best opening word for a rack; tap where it should start
// on row 8 to score most.
// Quick odds: a fixed set of bag trivia, worked out exactly.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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

// --- shared --------------------------------------------------------------------

func tileValue(letter string, blank bool) int {
	if blank || letter == "" {
		return 0
	}
	return letterPointValues[[]rune(strings.ToUpper(letter))[0]]
}

// drillWordBlocked: slurs that are still valid plays. A drill never puts
// one front and centre (the Opening spot word, the play to score).
var drillBlockedStems = []string{"FAGOT", "FAGGOT", "NIGGER", "NIGGA", "KIKE", "DYKE"}
var drillBlockedWords = map[string]bool{"FAG": true, "FAGS": true, "FAGGY": true, "SPIC": true, "SPICS": true, "SPIK": true, "SPIKS": true, "GOOK": true, "GOOKS": true, "WOP": true, "WOPS": true, "JAP": true, "JAPS": true, "COON": true, "COONS": true, "SPAZ": true, "SPAZZ": true, "RETARD": true, "RETARDS": true}

func drillWordBlocked(word string) bool {
	w := strings.ToUpper(strings.NewReplacer("(", "", ")", "", ".", "").Replace(word))
	if drillBlockedWords[w] {
		return true
	}
	for _, s := range drillBlockedStems {
		if strings.HasPrefix(w, s) {
			return true
		}
	}
	return false
}

// runStandaloneGenerator calls make (in parallel) until it has
// DRILLS_TARGET questions, for drills that don't need mid-game positions.
func runStandaloneGenerator(t *testing.T, drill, idPrefix string, make func(gd *kwg.KWG, rng *rand.Rand) drillQuestion) {
	if err := initService(); err != nil {
		t.Fatal(err)
	}
	outDir := os.Getenv("DRILLS_OUT")
	if outDir == "" {
		outDir = filepath.Join("..", "whiffers", "public", "drills")
	}
	target := envInt("DRILLS_TARGET", 300)
	gd, lexName, err := resolveLexicon(os.Getenv("DRILLS_LEXICON"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var questions []drillQuestion
	seen := map[string]bool{}
	var stop atomic.Bool
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < envInt("DRILLS_BUILDERS", runtime.NumCPU()); w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)*7919))
			for !stop.Load() {
				q := make(gd, rng)
				if q == nil {
					continue
				}
				key := fmt.Sprintf("%v", q)
				mu.Lock()
				if !seen[key] && len(questions) < target {
					seen[key] = true
					q.setID(fmt.Sprintf("%s-%05d", idPrefix, len(questions)+1))
					questions = append(questions, q)
					if len(questions) >= target {
						stop.Store(true)
					}
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if err := writeDrillBank(outDir, drill, questions, len(questions), map[string]any{"lexicon": lexName}); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d %s questions in %.0fs", len(questions), drill, time.Since(start).Seconds())
}

// --- Score it ------------------------------------------------------------------

type scorePart struct {
	Letter string `json:"letter"` // lowercase = blank
	Value  int    `json:"value"`
	Mult   int    `json:"mult"` // letter multiplier: 1, 2 or 3
	New    bool   `json:"new"`
}

type scoreWord struct {
	Word     string      `json:"word"`
	Parts    []scorePart `json:"parts"`
	WordMult int         `json:"wordMult"`
	Score    int         `json:"score"`
}

type scoreQuestion struct {
	ID       string        `json:"id"`
	Board    [][]string    `json:"board"`
	Rack     string        `json:"rack"`
	Move     defenseOption `json:"move"`
	Words    []scoreWord   `json:"words"` // the main word first, then cross words
	Bingo    bool          `json:"bingo"`
	Score    int           `json:"score"`
}

func (q *scoreQuestion) setID(id string) { q.ID = id }

// scoreWordAt scores one word through the squares in tiles (new tiles get
// their bonus squares, old ones don't).
func scoreWordAt(tiles []MoveTile) scoreWord {
	w := scoreWord{WordMult: 1}
	sum := 0
	for _, t := range tiles {
		v := tileValue(t.Letter, t.IsBlank)
		p := scorePart{Letter: t.Letter, Value: v, Mult: 1, New: t.IsNew}
		if t.IsBlank {
			p.Letter = strings.ToLower(t.Letter)
		}
		if t.IsNew {
			switch premiumKind[[2]int{t.Row, t.Col}] {
			case "dl":
				p.Mult = 2
			case "tl":
				p.Mult = 3
			case "dw":
				w.WordMult *= 2
			case "tw":
				w.WordMult *= 3
			}
		}
		sum += v * p.Mult
		w.Word += strings.ToUpper(t.Letter)
		w.Parts = append(w.Parts, p)
	}
	w.Score = sum * w.WordMult
	return w
}

// playBreakdown is every word a play makes, scored, from the board before it.
func playBreakdown(grid [][]string, tiles []MoveTile) []scoreWord {
	words := []scoreWord{scoreWordAt(tiles)}
	horizontal := tiles[0].Row == tiles[len(tiles)-1].Row
	at := func(r, c int) (MoveTile, bool) {
		if r < 0 || r > 14 || c < 0 || c > 14 || grid[r][c] == "" {
			return MoveTile{}, false
		}
		s := grid[r][c]
		return MoveTile{Row: r, Col: c, Letter: strings.ToUpper(s), IsBlank: s != strings.ToUpper(s)}, true
	}
	for _, t := range tiles {
		if !t.IsNew {
			continue
		}
		dr, dc := 1, 0
		if !horizontal {
			dr, dc = 0, 1
		}
		var before, after []MoveTile
		for r, c := t.Row-dr, t.Col-dc; ; r, c = r-dr, c-dc {
			x, ok := at(r, c)
			if !ok {
				break
			}
			before = append([]MoveTile{x}, before...)
		}
		for r, c := t.Row+dr, t.Col+dc; ; r, c = r+dr, c+dc {
			x, ok := at(r, c)
			if !ok {
				break
			}
			after = append(after, x)
		}
		if len(before)+len(after) == 0 {
			continue
		}
		words = append(words, scoreWordAt(append(append(before, t), after...)))
	}
	return words
}

func scoreQuestionFrom(gd *kwg.KWG, snap drillSnapshot, _ int, rng *rand.Rand) *scoreQuestion {
	cands := champCandidates(gd, snap.bd, snap.rack, snap.bagCount)
	grid := boardStrings(snap.bd)
	var options []*scoreQuestion
	for i, c := range cands {
		if i >= 12 {
			break
		}
		if c.kind != champPlay || c.m.TilesPlayed() < 2 || c.score < 14 || c.score > 90 {
			continue
		}
		d := toDetailedMove(c.m, snap.bd, alph)
		words := playBreakdown(grid, d.Tiles)
		if drillWordBlocked(words[0].Word) {
			continue
		}
		total := 0
		features := len(words) - 1 // cross words
		for _, w := range words {
			total += w.Score
		}
		for _, t := range d.Tiles {
			if _, ok := premiumKind[[2]int{t.Row, t.Col}]; ok && t.IsNew {
				features++
			}
		}
		bingo := c.m.TilesPlayed() == 7
		if bingo {
			total += 50
		}
		if total != c.score || features < 2 || len(words) > 4 {
			continue // a scoring rule this doesn't know, or too plain / too busy
		}
		options = append(options, &scoreQuestion{Board: grid, Rack: snap.rack, Move: defenseOptionFor(gd, snap, c, 0, nil, nil), Words: words, Bingo: bingo, Score: c.score})
	}
	if len(options) == 0 {
		return nil
	}
	return options[rng.Intn(len(options))]
}

func TestGenerateScoreDrills(t *testing.T) {
	runDrillGenerator(t, "score", "sco", func(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) drillQuestion {
		if q := scoreQuestionFrom(gd, snap, samples, rng); q != nil {
			return q
		}
		return nil
	})
}

// --- Out-play maths ------------------------------------------------------------

type outplayQuestion struct {
	ID        string        `json:"id"`
	Board     [][]string    `json:"board"`
	Rack      string        `json:"rack"`
	MyScore   int           `json:"myScore"`
	OppScore  int           `json:"oppScore"`
	Move      defenseOption `json:"move"`
	OppRack   string        `json:"oppRack"`
	RackValue int           `json:"rackValue"` // their tiles, before doubling
	Spread    int           `json:"spread"`    // your final score minus theirs
}

func (q *outplayQuestion) setID(id string) { q.ID = id }

// outplayQuestionFrom plays a Wally-vs-Wally game to the end and asks about
// the first time a side goes out - kept when the game is close (final
// spread within 50) and the scores look real (500+ combined).
func outplayQuestionFrom(gd *kwg.KWG, rng *rand.Rand) drillQuestion {
	bd := board.MakeBoard(board.CrosswordGameBoard)
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()
	gen := movegen.NewGordonGenerator(gd, bd, ld)
	bag := []rune(standardTilePool)
	rng.Shuffle(len(bag), func(i, j int) { bag[i], bag[j] = bag[j], bag[i] })
	draw := func(n int) string {
		n = min(n, len(bag))
		s := string(bag[len(bag)-n:])
		bag = bag[:len(bag)-n]
		return s
	}
	racks := [2]string{draw(7), draw(7)}
	scores := [2]int{}
	p, scoreless := 0, 0
	for turn := 0; turn < 60 && scoreless < 6; turn++ {
		c := champStaticBest(gen, racks[p], len(bag), rackPointValue(racks[1-p]))
		switch c.kind {
		case champPlay:
			if len(bag) == 0 && c.leave == "" {
				v := rackPointValue(racks[1-p])
				spread := scores[p] + c.score + 2*v - scores[1-p]
				if v < 3 || c.score < 6 || scores[p]+scores[1-p] < 500 || spread < -50 || spread > 50 {
					return nil
				}
				opt := defenseOptionFor(gd, drillSnapshot{bd: bd}, c, 0, nil, nil)
				if drillWordBlocked(opt.Word) {
					return nil
				}
				return &outplayQuestion{
					Board: boardStrings(bd), Rack: racks[p], MyScore: scores[p], OppScore: scores[1-p], Move: opt,
					OppRack: sortLeaveString(racks[1-p]), RackValue: v, Spread: spread,
				}
			}
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
		p = 1 - p
	}
	return nil
}

func TestGenerateOutplayDrills(t *testing.T) {
	runStandaloneGenerator(t, "outplay", "out", func(gd *kwg.KWG, rng *rand.Rand) drillQuestion {
		if q := outplayQuestionFrom(gd, rng); q != nil {
			return q
		}
		return nil
	})
}

// --- Best leave ----------------------------------------------------------------

type leaveOption struct {
	Leave string  `json:"leave"`
	Value float64 `json:"value"`
}

type leaveQuestion struct {
	ID      string        `json:"id"`
	Rack    string        `json:"rack"`
	Keep    int           `json:"keep"`
	Options []leaveOption `json:"options"`
	Answer  int           `json:"answer"`
}

func (q *leaveQuestion) setID(id string) { q.ID = id }

// subsetsOf is every distinct sorted n-tile subset of rack.
func subsetsOf(rack string, n int) []string {
	r := []rune(sortLeaveString(rack))
	seen := map[string]bool{}
	var out []string
	var pick []rune
	var walk func(i int)
	walk = func(i int) {
		if len(pick) == n {
			s := string(pick)
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
			return
		}
		for j := i; j < len(r); j++ {
			pick = append(pick, r[j])
			walk(j + 1)
			pick = pick[:len(pick)-1]
		}
	}
	walk(0)
	return out
}

// leaveQuestionFrom: a random rack (no blank), keep 2-4 tiles, five
// options. With an S on the rack every option keeps it, so the question is
// about what goes with it. The best must beat the next by 1.5+.
func leaveQuestionFrom(_ *kwg.KWG, rng *rand.Rand) drillQuestion {
	pool := []rune(strings.ReplaceAll(standardTilePool, "?", ""))
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	rack := sortLeaveString(string(pool[:7]))
	keep := 2 + rng.Intn(3)
	var opts []leaveOption
	for _, s := range subsetsOf(rack, keep) {
		if strings.Contains(rack, "S") && !strings.Contains(s, "S") {
			continue
		}
		v, ok := leaveValues[s]
		if !ok {
			continue
		}
		opts = append(opts, leaveOption{Leave: s, Value: math.Round(v*10) / 10})
	}
	if len(opts) < 5 {
		return nil
	}
	rng.Shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
	opts = opts[:5]
	sorted := append([]leaveOption(nil), opts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Value > sorted[j].Value })
	if sorted[0].Value-sorted[1].Value < 1.5 || sorted[0].Value-sorted[4].Value < 5 {
		return nil
	}
	q := &leaveQuestion{Rack: rack, Keep: keep, Options: opts}
	for i, o := range opts {
		if o.Leave == sorted[0].Leave {
			q.Answer = i
		}
	}
	return q
}

func TestGenerateLeaveDrills(t *testing.T) {
	runStandaloneGenerator(t, "leave", "lv", leaveQuestionFrom)
}

// --- Opening spot --------------------------------------------------------------

type openingSpot struct {
	Start int `json:"start"` // column of the first letter on row 8
	Score int `json:"score"`
}

type openingQuestion struct {
	ID    string        `json:"id"`
	Rack  string        `json:"rack"`
	Word  string        `json:"word"`
	Spots []openingSpot `json:"spots"` // every start that covers the star
	Best  int           `json:"best"`  // the top score
}

func (q *openingQuestion) setID(id string) { q.ID = id }

func openingScore(word string, start int) int {
	sum, mult := 0, 1
	for i, ch := range word {
		v := letterPointValues[ch]
		switch premiumKind[[2]int{7, start + i}] {
		case "dl":
			v *= 2
		case "tl":
			v *= 3
		case "dw":
			mult *= 2
		case "tw":
			mult *= 3
		}
		sum += v
	}
	total := sum * mult
	if len([]rune(word)) == 7 {
		total += 50
	}
	return total
}

// openingQuestionFrom: the best opening (by equity) for a random rack with
// no blank; a question if it's 5+ letters and the spot matters (best beats
// worst by 4+, and at most two spots tie for best).
func openingQuestionFrom(gd *kwg.KWG, rng *rand.Rand) drillQuestion {
	pool := []rune(strings.ReplaceAll(standardTilePool, "?", ""))
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	rack := sortLeaveString(string(pool[:7]))
	bd := board.MakeBoard(board.CrosswordGameBoard)
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()
	cands := champCandidates(gd, bd, rack, 86)
	if len(cands) == 0 || cands[0].kind != champPlay || cands[0].m.TilesPlayed() < 5 {
		return nil
	}
	word := moveWordFromString(cands[0].m.String())
	if drillWordBlocked(word) {
		return nil
	}
	n := len([]rune(word))
	q := &openingQuestion{Rack: rack, Word: word}
	worst := 1 << 30
	for s := max(0, 7-n+1); s <= 7 && s+n <= 15; s++ {
		sc := openingScore(word, s)
		q.Spots = append(q.Spots, openingSpot{Start: s, Score: sc})
		q.Best = max(q.Best, sc)
		worst = min(worst, sc)
	}
	ties := 0
	for _, sp := range q.Spots {
		if sp.Score == q.Best {
			ties++
		}
	}
	if q.Best-worst < 4 || ties > 2 {
		return nil
	}
	return q
}

func TestGenerateOpeningDrills(t *testing.T) {
	runStandaloneGenerator(t, "opening", "op", openingQuestionFrom)
}

// --- Quick odds ----------------------------------------------------------------

type oddsQuestion struct {
	ID      string   `json:"id"`
	Ask     string   `json:"ask"`
	Choices []string `json:"choices"`
	Answer  int      `json:"answer"`
	Why     string   `json:"why"`
	Source  string   `json:"source"` // "exact" | "simulated"
}

func (q *oddsQuestion) setID(id string) { q.ID = id }

// rackChance is the exact chance a 7-tile opening rack from the full bag
// passes test, enumerating every rack by letter counts.
func rackChance(test func(counts map[rune]int) bool) float64 {
	counts := map[rune]int{}
	for _, t := range standardTilePool {
		counts[t]++
	}
	letters := make([]rune, 0, len(counts))
	for l := range counts {
		letters = append(letters, l)
	}
	sort.Slice(letters, func(i, j int) bool { return letters[i] < letters[j] })
	hit := 0.0
	have := map[rune]int{}
	var walk func(i, left int, weight float64)
	walk = func(i, left int, weight float64) {
		if left == 0 {
			if test(have) {
				hit += weight
			}
			return
		}
		if i == len(letters) {
			return
		}
		l := letters[i]
		for k := 0; k <= min(left, counts[l]); k++ {
			have[l] = k
			walk(i+1, left-k, weight*choose(counts[l], k))
		}
		have[l] = 0
	}
	walk(0, 7, 1)
	return hit / choose(100, 7)
}

func pctString(p float64) string { return pctAt(p, pctDecimals(p)) }

// pctDecimals: whole numbers from 30% up, one decimal from 1%, else two.
func pctDecimals(p float64) int {
	switch {
	case p >= 0.3:
		return 0
	case p >= 0.01:
		return 1
	default:
		return 2
	}
}

func pctAt(p float64, decimals int) string {
	return fmt.Sprintf("%.*f%%", decimals, p*100)
}

// oddsChoices: the answer plus three wrong ones around it - a random mix
// of lower and higher, so the answer's slot gives nothing away.
func oddsChoices(p float64, rng *rand.Rand) ([]string, int) {
	dec := pctDecimals(p) // every choice to the answer's precision
	right := pctAt(p, dec)
	lower := []float64{0.75, 0.55, 0.4, 0.3}
	higher := []float64{1.35, 1.75, 2.3, 3}
	nLow := rng.Intn(4)
	var factors []float64
	factors = append(factors, lower[:nLow]...)
	factors = append(factors, higher...)
	factors = append(factors, lower[nLow:]...)
	out := []string{right}
	for _, f := range factors {
		if p*f > 0.97 {
			continue
		}
		w := pctAt(p*f, dec)
		dup := false
		for _, o := range out {
			dup = dup || o == w
		}
		if !dup {
			out = append(out, w)
		}
		if len(out) == 4 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		var a, b float64
		fmt.Sscanf(out[i], "%f", &a)
		fmt.Sscanf(out[j], "%f", &b)
		return a < b
	})
	for i, o := range out {
		if o == right {
			return out, i
		}
	}
	return out, 0
}

func TestGenerateOddsDrills(t *testing.T) {
	outDir := os.Getenv("DRILLS_OUT")
	if outDir == "" {
		outDir = filepath.Join("..", "whiffers", "public", "drills")
	}
	rng := rand.New(rand.NewSource(7))
	allRacks := choose(100, 7)
	atLeastOne := func(k int) float64 { return 1 - choose(100-k, 7)/allRacks }
	vowels := func(c map[rune]int) int { return c['A'] + c['E'] + c['I'] + c['O'] + c['U'] }
	type item struct {
		ask, why, source string
		p                float64
	}
	items := []item{
		{"Chance your opening rack has at least one blank?", "There are 2 blanks in 100 tiles. No blank in 7 draws: 98/100 × 97/99 × … × 92/94 = {q}, so {p} for one or both.", "exact", atLeastOne(2)},
		{"Chance your opening rack has both blanks?", "7/100 × 6/99, the chance both blanks are among your 7 tiles: about 1 in 236.", "exact", choose(98, 5) / allRacks},
		{"Chance your opening rack has at least one S?", "4 S's in 100 tiles. No S in 7 draws: 96/100 × … × 90/94 = {q}, so {p} for at least one.", "exact", atLeastOne(4)},
		{"Chance your opening rack has an S or a blank?", "6 of the 100 tiles are S or blank. Missing all six in 7 draws is {q}, so {p} to hold at least one.", "exact", atLeastOne(6)},
		{"Chance your opening rack has the Q?", "One Q in 100 tiles, and you hold 7 of them: 7%.", "exact", 0.07},
		{"Chance your opening rack has the Q but no U?", "Q in 7% of racks; with 4 U's among the other 99 tiles, no U in the other 6 draws is 77.6%. 7% × 77.6% = {p}.", "exact", 0.07 * choose(95, 6) / choose(99, 6)},
		{"Chance your opening rack has at least one of J, Q, X or Z?", "4 power tiles in 100, the same count as the S's: {p} for at least one.", "exact", atLeastOne(4)},
		{"Chance your opening rack has no vowels at all (A, E, I, O, U)?", "58 of the 100 tiles aren't vowels (blanks and Y count as not). All 7 from those 58: {p}.", "exact", choose(58, 7) / allRacks},
		{"Chance your opening rack has 5 or more vowels?", "42 vowels in the bag. Racks with 5, 6 or 7 of them, counted exactly: {p}.", "exact", rackChance(func(c map[rune]int) bool { return vowels(c) >= 5 })},
		{"Chance your opening rack has 2, 3 or 4 vowels?", "The balanced racks: counted exactly over every rack, {p}. The rest are vowel-heavy (5+) or vowel-light (0-1).", "exact", rackChance(func(c map[rune]int) bool { v := vowels(c); return v >= 2 && v <= 4 })},
		{"Chance your opening rack has seven different tiles (no repeats)?", "Counted over every rack: most racks have at least one pair, thanks to 12 E's, 9 A's, 9 I's and 8 O's.", "exact", rackChance(func(c map[rune]int) bool {
			for _, k := range c {
				if k > 1 {
					return false
				}
			}
			return true
		})},
		{"Chance your opening rack has at least one E?", "12 E's, the most of any letter. No E in 7 draws: 88/100 × … × 82/94 = {q}, so {p} to hold one.", "exact", atLeastOne(12)},
		{"Chance your opening rack has two or more I's?", "9 I's in the bag. Counted exactly over every rack: about 1 in 8 racks has a pair of I's or worse.", "exact", rackChance(func(c map[rune]int) bool { return c['I'] >= 2 })},
		{"Chance you can bingo on your first turn, going first?", "From 20,000 simulated games (NWL23): only a 7-letter word fits, and about 1 opening rack in 7 makes one.", "simulated", 0.136},
		{"Chance you can bingo on your first turn, going second?", "From 20,000 simulated games (NWL23): about 60% more likely than going first, because the opener's tiles let you play 8s through them.", "simulated", 0.217},
		{"You exchange the Q. Chance you draw it straight back?", "Zero: you draw your new tiles before the old ones go back in the bag.", "exact", 0},
		{"9 tiles unseen and 2 in the bag. Chance the Z is in the bag rather than on their rack?", "They hold 7 of the 9 unseen tiles, so the Z is in the bag 2 times in 9.", "exact", 2.0 / 9},
		{"8 tiles unseen, 1 in the bag. Chance they hold the last blank?", "They hold 7 of the 8 unseen tiles: 7/8.", "exact", 7.0 / 8},
		{"30 tiles unseen, one of them the last S. You play 4 tiles. Chance to draw it?", "Each draw is another 1 in 30 shot at it: 4 ÷ 30.", "exact", 4.0 / 30},
		{"40 tiles unseen, including both blanks. You play 7 and draw 7. Chance to draw at least one blank?", "No blank in 7 draws: 38/40 × 37/39 × … × 32/34 = {q}, so {p} for at least one.", "exact", chanceOfABlank(40, 2, 7)},
		{"What share of the 100 tiles are vowels (A, E, I, O, U)?", "A 9, E 12, I 9, O 8, U 4 = 42 tiles.", "exact", 0.42},
		{"What share of the 100 tiles are worth 1 point?", "A E I O U L N S T R: 9+12+9+8+4+4+6+4+6+6 = 68 tiles.", "exact", 0.68},
	}
	var questions []drillQuestion
	for i, it := range items {
		q := &oddsQuestion{Ask: it.ask, Why: it.why, Source: it.source}
		if it.p == 0 {
			q.Choices, q.Answer = []string{"0%", "1%", "7%", "14%"}, 0
		} else {
			q.Choices, q.Answer = oddsChoices(it.p, rng)
		}
		q.Why = strings.NewReplacer("{p}", pctString(it.p), "{q}", pctString(1-it.p)).Replace(q.Why)
		if strings.HasPrefix(q.Why, "Counted") {
			q.Why += fmt.Sprintf(" Exactly %s.", pctString(it.p))
		}
		q.setID(fmt.Sprintf("odd-%05d", i+1))
		questions = append(questions, q)
		t.Logf("%-90s %s  %v", it.ask, pctString(it.p), q.Choices)
	}
	if err := writeDrillBank(outDir, "odds", questions, len(questions), nil); err != nil {
		t.Fatal(err)
	}
}
