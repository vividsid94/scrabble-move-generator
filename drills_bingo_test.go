package main

// Bingo odds (drill generator, local only - see drills_gen_test.go):
// "Two good leaves. Against this pool, which is likelier to give you a
// bingo next turn?"
//
//	go test -run TestGenerateBingoDrills -v -timeout 0
//
// Exact, not sampled: for a leave of L tiles, every possible draw of 7-L
// tiles from the unseen pool is enumerated (identical tiles grouped, each
// draw weighted by how many ways it can happen), and a draw counts if the
// full rack makes a 7-letter word (NWL23 sevens; a blank tries every
// letter). The leaves come from real positions - two of your candidate
// plays - and make a question when both are good but not textbook (20-60%,
// no blank, not a 5+ tile slice of RETAINS) and 5-15 points apart.

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/domino14/word-golib/kwg"
)

const (
	bingoLeaveMin, bingoLeaveMax = 4, 6
	bingoMinChance, bingoMaxChance = 0.20, 0.60
	bingoMinGap, bingoMaxGap     = 0.05, 0.15
)

// sevensByAlphagram: sorted letters -> the most probable word with them.
var sevensByAlphagram = func() map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(string(nwl23SevensData), "\n") {
		w := strings.ToUpper(strings.TrimSpace(line))
		if len(w) != 7 {
			continue
		}
		key := sortLeaveString(w)
		if _, ok := m[key]; !ok {
			m[key] = w
		}
	}
	return m
}()

// bingoWord returns a 7-letter word the rack makes ("" if none), trying
// every letter for each blank.
func bingoWord(rack string) string {
	blanks := strings.Count(rack, "?")
	base := strings.ReplaceAll(rack, "?", "")
	if blanks == 0 {
		return sevensByAlphagram[sortLeaveString(base)]
	}
	for a := 'A'; a <= 'Z'; a++ {
		if blanks == 1 {
			if w := sevensByAlphagram[sortLeaveString(base+string(a))]; w != "" {
				return w
			}
			continue
		}
		for b := a; b <= 'Z'; b++ {
			if w := sevensByAlphagram[sortLeaveString(base+string(a)+string(b))]; w != "" {
				return w
			}
		}
	}
	return ""
}

type bingoExample struct {
	Word  string  `json:"word"`
	Drawn string  `json:"drawn"`
	Share float64 `json:"share"` // of all possible draws
}

type bingoOption struct {
	Leave    string         `json:"leave"`
	Chance   float64        `json:"chance"`
	From     string         `json:"from"` // the play that keeps it, e.g. "8D QUOIT"
	Examples []bingoExample `json:"examples"`
}

type bingoQuestion struct {
	ID       string         `json:"id"`
	Unseen   string         `json:"unseen"`
	BagCount int            `json:"bagCount"`
	Options  [2]bingoOption `json:"options"`
	Answer   int            `json:"answer"`
}

func (q *bingoQuestion) setID(id string) { q.ID = id }

func choose(n, k int) float64 {
	if k < 0 || k > n {
		return 0
	}
	r := 1.0
	for i := 0; i < k; i++ {
		r = r * float64(n-i) / float64(i+1)
	}
	return r
}

// leaveBingoChance enumerates every draw that fills the leave to 7 tiles.
func leaveBingoChance(leave string, pool []rune) (float64, []bingoExample) {
	need := 7 - len([]rune(leave))
	counts := map[rune]int{}
	for _, t := range pool {
		counts[t]++
	}
	letters := make([]rune, 0, len(counts))
	for l := range counts {
		letters = append(letters, l)
	}
	sort.Slice(letters, func(i, j int) bool { return letters[i] < letters[j] })
	total := choose(len(pool), need)
	hit := 0.0
	byWord := map[string]*bingoExample{}
	var drawn []rune
	var walk func(i, left int, weight float64)
	walk = func(i, left int, weight float64) {
		if left == 0 {
			if w := bingoWord(leave + string(drawn)); w != "" {
				hit += weight
				e, ok := byWord[w]
				if !ok {
					e = &bingoExample{Word: w, Drawn: sortLeaveString(string(drawn))}
					byWord[w] = e
				}
				e.Share += weight / total
			}
			return
		}
		if i == len(letters) {
			return
		}
		l := letters[i]
		for k := 0; k <= min(left, counts[l]); k++ {
			for j := 0; j < k; j++ {
				drawn = append(drawn, l)
			}
			walk(i+1, left-k, weight*choose(counts[l], k))
			drawn = drawn[:len(drawn)-k]
		}
	}
	walk(0, need, 1)
	examples := make([]bingoExample, 0, len(byWord))
	for _, e := range byWord {
		e.Share = math.Round(e.Share*1000) / 1000
		examples = append(examples, *e)
	}
	sort.Slice(examples, func(i, j int) bool { return examples[i].Share > examples[j].Share })
	if len(examples) > 4 {
		examples = examples[:4]
	}
	return hit / total, examples
}

// textbookLeave: a 5+ tile slice of RETAINS - too well known to ask about.
func textbookLeave(leave string) bool {
	if len(leave) < 5 {
		return false
	}
	// Every tile of the leave comes out of AEINRST.
	return len(removeRackFromPool("AEINRST", leave)) == 7-len(leave)
}

func bingoQuestionFrom(gd *kwg.KWG, snap drillSnapshot, _ int, rng *rand.Rand) *bingoQuestion {
	if snap.bagCount < 10 {
		return nil
	}
	type cand struct {
		opt bingoOption
	}
	var cands []cand
	seen := map[string]bool{}
	for i, c := range champCandidates(gd, snap.bd, snap.rack, snap.bagCount) {
		if i >= 15 {
			break
		}
		n := len([]rune(c.leave))
		if n < bingoLeaveMin || n > bingoLeaveMax || strings.Contains(c.leave, "?") || seen[c.leave] || textbookLeave(c.leave) {
			continue
		}
		seen[c.leave] = true
		p, ex := leaveBingoChance(c.leave, snap.unseen)
		if p < bingoMinChance || p > bingoMaxChance {
			continue
		}
		from := "Exch. " + c.exch
		if c.kind == champPlay {
			from = c.m.BoardCoords() + " " + annotatedWord(toDetailedMove(c.m, snap.bd, alph).Tiles)
		}
		cands = append(cands, cand{bingoOption{Leave: c.leave, Chance: math.Round(p*1000) / 1000, From: from, Examples: ex}})
	}
	var pairs [][2]int
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if gap := math.Abs(cands[i].opt.Chance - cands[j].opt.Chance); gap >= bingoMinGap && gap <= bingoMaxGap {
				pairs = append(pairs, [2]int{i, j})
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	pr := pairs[rng.Intn(len(pairs))]
	if rng.Intn(2) == 0 {
		pr[0], pr[1] = pr[1], pr[0]
	}
	q := &bingoQuestion{Unseen: sortedPool(snap.unseen), BagCount: snap.bagCount, Options: [2]bingoOption{cands[pr[0]].opt, cands[pr[1]].opt}}
	if q.Options[1].Chance > q.Options[0].Chance {
		q.Answer = 1
	}
	return q
}

// annotatedWord writes a play the way the app shows it: played-through
// letters in parentheses, blanks in lowercase - e.g. "(J)UNK".
func annotatedWord(tiles []MoveTile) string {
	var b strings.Builder
	open := false
	for _, t := range tiles {
		if !t.IsNew && !open {
			b.WriteByte('(')
			open = true
		} else if t.IsNew && open {
			b.WriteByte(')')
			open = false
		}
		l := strings.ToUpper(t.Letter)
		if t.IsBlank {
			l = strings.ToLower(l)
		}
		b.WriteString(l)
	}
	if open {
		b.WriteByte(')')
	}
	return b.String()
}

func sortedPool(pool []rune) string {
	p := []rune(string(pool))
	sort.Slice(p, func(i, j int) bool {
		if (p[i] == '?') != (p[j] == '?') {
			return p[i] == '?'
		}
		return p[i] < p[j]
	})
	return string(p)
}

func TestGenerateBingoDrills(t *testing.T) {
	runDrillGenerator(t, "bingo", "bgo", func(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) drillQuestion {
		if q := bingoQuestionFrom(gd, snap, samples, rng); q != nil {
			return q
		}
		return nil
	})
}
