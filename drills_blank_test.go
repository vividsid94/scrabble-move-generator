package main

// Blank odds (drill generator, local only - see drills_gen_test.go):
// "You play this big turnover. How likely are you to draw a blank?"
//
//	go test -run TestGenerateBlankDrills -v -timeout 0
//
// Positions with 20-50 tiles in the bag and at least one blank unseen; the
// move is the best one (by equity) that plays at least 5 tiles. The answer
// is exact: drawing k tiles from U unseen tiles with b blanks among them,
// P(no blank) = (U-b)/U x (U-b-1)/(U-1) x ... (k factors). The opponent's
// rack is part of the unseen pool, but every unseen tile is equally likely
// to be in the bag, so that's the right count. Four answer choices: the
// true chance and three spread-out wrong ones.

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/domino14/word-golib/kwg"
)

const (
	blankMinBag   = 20
	blankMaxBag   = 50
	blankMinTiles = 5
)

type blankQuestion struct {
	ID       string        `json:"id"`
	Board    [][]string    `json:"board"`
	Rack     string        `json:"rack"`
	BagCount int           `json:"bagCount"`
	MyScore  int           `json:"myScore"`
	OppScore int           `json:"oppScore"`
	Move     defenseOption `json:"move"`
	Drawn    int           `json:"drawn"`  // tiles you'll draw (k)
	Unseen   string        `json:"unseen"` // sorted, blanks as '?'
	Blanks   int           `json:"blanks"` // blanks among the unseen (b)
	Chance   float64       `json:"chance"` // exact chance of at least one blank, 0-1
	Choices  []int         `json:"choices"`
	Answer   int           `json:"answer"` // index into Choices
}

func (q *blankQuestion) setID(id string) { q.ID = id }

// chanceOfABlank: at least one of b blanks in k draws from u tiles.
func chanceOfABlank(u, b, k int) float64 {
	none := 1.0
	for i := 0; i < k; i++ {
		none *= float64(u-b-i) / float64(u-i)
	}
	return 1 - none
}

func blankQuestionFrom(gd *kwg.KWG, snap drillSnapshot, _ int, rng *rand.Rand) *blankQuestion {
	if snap.bagCount < blankMinBag || snap.bagCount > blankMaxBag {
		return nil
	}
	b := strings.Count(string(snap.unseen), "?")
	if b == 0 {
		return nil
	}
	var move *champChoice
	for _, c := range champCandidates(gd, snap.bd, snap.rack, snap.bagCount) {
		if c.kind == champPlay && c.m.TilesPlayed() >= blankMinTiles {
			cc := c
			move = &cc
			break
		}
	}
	if move == nil {
		return nil
	}
	u, k := len(snap.unseen), move.m.TilesPlayed()
	p := chanceOfABlank(u, b, k)
	correct := int(math.Round(p * 100))

	// Three wrong answers, spread around the right one and kept apart.
	choices := []int{correct}
	for tries := 0; len(choices) < 4 && tries < 200; tries++ {
		offset := (6 + rng.Intn(15)) * []int{-1, 1}[rng.Intn(2)]
		v := correct + offset
		if v < 2 || v > 98 {
			continue
		}
		ok := true
		for _, c := range choices {
			if intAbs(c-v) < 5 {
				ok = false
			}
		}
		if ok {
			choices = append(choices, v)
		}
	}
	if len(choices) < 4 {
		return nil
	}
	sort.Ints(choices)
	answer := sort.SearchInts(choices, correct)

	unseen := []rune(string(snap.unseen))
	sort.Slice(unseen, func(i, j int) bool {
		if (unseen[i] == '?') != (unseen[j] == '?') {
			return unseen[i] == '?' // blanks first
		}
		return unseen[i] < unseen[j]
	})
	return &blankQuestion{
		Board: boardStrings(snap.bd), Rack: snap.rack, BagCount: snap.bagCount, MyScore: snap.myScore, OppScore: snap.oppScore,
		Move:   defenseOptionFor(gd, snap, *move, 0, nil, nil),
		Drawn:  k,
		Unseen: string(unseen),
		Blanks: b,
		Chance: math.Round(p*1000) / 1000,
		Choices: choices,
		Answer:  answer,
	}
}

func TestGenerateBlankDrills(t *testing.T) {
	runDrillGenerator(t, "blank", "blk", func(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) drillQuestion {
		if q := blankQuestionFrom(gd, snap, samples, rng); q != nil {
			return q
		}
		return nil
	})
}
