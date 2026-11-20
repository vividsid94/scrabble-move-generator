package main

// Sim it (drill generator, local only - see drills_gen_test.go):
// "Which of these two plays wins more often?" Answered by Wilma's sims
// (champ_sim.go), so the generator works hard to make the answer real:
//
//  1. Quick pass: the top 8 moves by equity, 400 playouts each.
//  2. Pick a pair close on equity (within 6 points, so score + leave don't
//     give it away) but 3-10 points apart on win chance.
//  3. Confirm: re-sim just those two at 4,000 playouts each. Keep the
//     question only if the gap is still 3-10 points and at least 2.8x its
//     standard error.
//
//	go test -run TestGenerateSimDrills -v -timeout 0
//
// Each question also records what each move gives up (the Defense
// numbers), so the reveal can say why, not just which.

import (
	"math"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/domino14/word-golib/kwg"
)

const (
	simQuickPlayouts   = 400
	simConfirmPlayouts = 4000
	simCandidates      = 8
	simMaxEquityGap    = 6.0
	simMinWinGap       = 0.03
	simMaxWinGap       = 0.10
	simMinZ            = 2.8
	simGivenUpSamples  = 200
)

type simOption struct {
	defenseOption
	WinChance  float64 `json:"winChance"`
	SpreadGain float64 `json:"spreadGain"` // average spread change over the playouts
	Playouts   int     `json:"playouts"`
}

type simQuestion struct {
	ID       string       `json:"id"`
	Board    [][]string   `json:"board"`
	Rack     string       `json:"rack"`
	BagCount int          `json:"bagCount"`
	MyScore  int          `json:"myScore"`
	OppScore int          `json:"oppScore"`
	Plies    int          `json:"plies"` // 2, or -1 = played to the end of the game
	Options  [2]simOption `json:"options"`
	Answer   int          `json:"answer"`
}

func (q *simQuestion) setID(id string) { q.ID = id }

func simQuestionFrom(gd *kwg.KWG, snap drillSnapshot, _ int, rng *rand.Rand) *simQuestion {
	if snap.bagCount < 15 {
		return nil
	}
	var plays []champChoice
	for _, c := range champCandidates(gd, snap.bd, snap.rack, snap.bagCount) {
		if c.kind == champPlay && c.score >= defenseMinScore {
			plays = append(plays, c)
		}
		if len(plays) == simCandidates {
			break
		}
	}
	if len(plays) < 2 {
		return nil
	}
	root := &champRoot{gd: gd, bd: snap.bd, rack: snap.rack, unseen: snap.unseen, myScore: snap.myScore, oppScore: snap.oppScore, plies: 2}
	if snap.bagCount <= 14 {
		root.plies = -1
	}
	sim := func(cands []champChoice, n int) []*champStat {
		ranked, _ := champRank(root, cands, champConfig{candidates: len(cands), minIterations: n, maxIterations: n, budget: 10 * time.Minute})
		return ranked
	}

	// 1-2. Quick pass and a candidate pair.
	quick := sim(plays, simQuickPlayouts)
	var pairs [][2]*champStat
	for i := 0; i < len(quick); i++ {
		for j := i + 1; j < len(quick); j++ {
			a, b := quick[i], quick[j]
			gap := math.Abs(a.win() - b.win())
			if math.Abs(a.choice.total-b.choice.total) <= simMaxEquityGap && gap >= simMinWinGap && gap <= simMaxWinGap {
				pairs = append(pairs, [2]*champStat{a, b})
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	pick := pairs[rng.Intn(len(pairs))]

	// 3. Confirm with many more playouts.
	confirmed := sim([]champChoice{pick[0].choice, pick[1].choice}, simConfirmPlayouts)
	if len(confirmed) != 2 {
		return nil
	}
	a, b := confirmed[0], confirmed[1]
	pa, pb := a.win(), b.win()
	gap := math.Abs(pa - pb)
	se := math.Sqrt(pa*(1-pa)/float64(a.n) + pb*(1-pb)/float64(b.n))
	if gap < simMinWinGap || gap > simMaxWinGap || se == 0 || gap/se < simMinZ {
		return nil
	}
	// Skip positions that are all but decided either way - dull questions.
	if math.Min(pa, pb) < 0.15 || math.Max(pa, pb) > 0.85 {
		return nil
	}

	opt := func(st *champStat) simOption {
		givenUp, _, hot := givenUp(gd, snap, st.choice, simGivenUpSamples, rng)
		d := defenseOptionFor(gd, snap, st.choice, givenUp, nil, hot)
		gain := 0.0
		if st.n > 0 {
			gain = st.spreadSum/float64(st.n) - float64(snap.myScore-snap.oppScore)
		}
		return simOption{defenseOption: d, WinChance: math.Round(st.win()*1000) / 1000, SpreadGain: math.Round(gain*10) / 10, Playouts: st.n}
	}
	q := &simQuestion{Board: boardStrings(snap.bd), Rack: snap.rack, BagCount: snap.bagCount, MyScore: snap.myScore, OppScore: snap.oppScore, Plies: root.plies}
	first, second := a, b
	if rng.Intn(2) == 0 {
		first, second = b, a
	}
	q.Options = [2]simOption{opt(first), opt(second)}
	if q.Options[1].WinChance > q.Options[0].WinChance {
		q.Answer = 1
	}
	return q
}

func TestGenerateSimDrills(t *testing.T) {
	// Each sim already spreads across half the cores, so fewer positions
	// at once than the other drills.
	if os.Getenv("DRILLS_BUILDERS") == "" {
		t.Setenv("DRILLS_BUILDERS", strconv.Itoa(max(2, runtime.NumCPU()/4)))
	}
	runDrillGenerator(t, "sim", "sim", func(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) drillQuestion {
		if q := simQuestionFrom(gd, snap, samples, rng); q != nil {
			return q
		}
		return nil
	})
}
