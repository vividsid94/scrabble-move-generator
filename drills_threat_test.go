package main

// Find the threat (drill generator, local only - see drills_gen_test.go):
// "You just played this. Tap where the opponent's best reply lands."
//
//	go test -run TestGenerateThreatDrills -v -timeout 0
//
// The move is one of your top 5 by equity. The opponent answers it with
// Wally's best reply against random racks, and the squares those replies
// cover make a heat map. A position becomes a question only when YOUR move
// created a real threat: a 3x3 zone that at least 40% of replies land in
// after the move, at least 25 percentage points more than with the same
// racks on the board before your move, and averaging 25+ points.

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/kwg"
)

const (
	threatCandidates   = 5
	threatMinZoneShare = 0.40
	threatMinOpened    = 0.25
	threatMinZoneAvg   = 25.0
)

type threatHeatCell struct {
	Row int     `json:"row"`
	Col int     `json:"col"`
	V   float64 `json:"v"` // 0-1, relative to the hottest square
}

type threatQuestion struct {
	ID         string           `json:"id"`
	Board      [][]string       `json:"board"` // before your move; lowercase = blank
	Rack       string           `json:"rack"`
	BagCount   int              `json:"bagCount"`
	MyScore    int              `json:"myScore"`
	OppScore   int              `json:"oppScore"`
	Move       defenseOption    `json:"move"` // the move you played (oppAvg = what it gives up)
	Heat       []threatHeatCell `json:"heat"`
	Hot        drillHotSquare   `json:"hot"`        // the hottest square; Share = share of replies in its 3x3 zone
	ZoneBefore float64          `json:"zoneBefore"` // that share if you hadn't played the move
	ZoneAvg    float64          `json:"zoneAvg"`    // average score of the replies in the zone
	TopReplies []drillReply     `json:"topReplies"` // the commonest replies in the zone
	Samples    int              `json:"samples"`
}

func (q *threatQuestion) setID(id string) { q.ID = id }

type replySample struct {
	score   int
	key     string
	squares [][2]int
	reply   drillReply
}

// replySamples answers each rack with Wally's best play on bd.
func replySamples(gd *kwg.KWG, bd *board.GameBoard, racks []string, bagAfter int) []replySample {
	gen := movegen.NewGordonGenerator(gd, bd, ld)
	out := make([]replySample, 0, len(racks))
	cache := map[string]replySample{}
	for _, rack := range racks {
		c := champStaticBest(gen, rack, bagAfter, 0)
		if c.kind != champPlay {
			out = append(out, replySample{})
			continue
		}
		key := c.m.ShortDescription()
		if rs, ok := cache[key]; ok {
			out = append(out, rs)
			continue
		}
		d := toDetailedMove(c.m, bd, alph)
		rs := replySample{score: c.score, key: key, reply: drillReply{Position: c.m.BoardCoords(), Word: moveWordFromString(c.m.String()), Score: c.score, Tiles: d.Tiles}}
		for _, tile := range d.Tiles {
			if tile.IsNew {
				rs.squares = append(rs.squares, [2]int{tile.Row, tile.Col})
			}
		}
		cache[key] = rs
		out = append(out, rs)
	}
	return out
}

func intAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func inZone(rs replySample, r0, c0 int) bool {
	for _, sq := range rs.squares {
		if intAbs(sq[0]-r0) <= 1 && intAbs(sq[1]-c0) <= 1 {
			return true
		}
	}
	return false
}

func threatQuestionFrom(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) *threatQuestion {
	var plays []champChoice
	for _, c := range champCandidates(gd, snap.bd, snap.rack, snap.bagCount) {
		if c.kind == champPlay && c.score >= defenseMinScore {
			plays = append(plays, c)
		}
		if len(plays) == threatCandidates {
			break
		}
	}
	if len(plays) == 0 {
		return nil
	}
	// The same random racks for every candidate and for "before".
	pool := make([]rune, len(snap.unseen))
	copy(pool, snap.unseen)
	racks := make([]string, samples)
	for i := range racks {
		rng.Shuffle(len(pool), func(a, b int) { pool[a], pool[b] = pool[b], pool[a] })
		racks[i] = string(pool[:min(7, len(pool))])
	}
	before := replySamples(gd, snap.bd, racks, snap.bagCount)

	var best *threatQuestion
	bestOpened := 0.0
	for _, cand := range plays {
		bd := snap.bd.Copy()
		bd.PlayMove(cand.m)
		cross_set.UpdateCrossSetsForMove(bd, cand.m, gd, ld)
		after := replySamples(gd, bd, racks, max(0, snap.bagCount-cand.m.TilesPlayed()))

		var heat [15][15]int
		total := 0
		for _, rs := range after {
			total += rs.score
			for _, sq := range rs.squares {
				heat[sq[0]][sq[1]]++
			}
		}
		hr, hc, hmax := 0, 0, 0
		for r := 0; r < 15; r++ {
			for c := 0; c < 15; c++ {
				if heat[r][c] > hmax {
					hr, hc, hmax = r, c, heat[r][c]
				}
			}
		}
		if hmax == 0 {
			continue
		}
		zoneAfter, zoneBefore, zoneScore := 0, 0, 0
		tally := map[string]drillReply{}
		counts := map[string]int{}
		for i, rs := range after {
			if inZone(rs, hr, hc) {
				zoneAfter++
				zoneScore += rs.score
				tally[rs.key] = rs.reply
				counts[rs.key]++
			}
			if inZone(before[i], hr, hc) {
				zoneBefore++
			}
		}
		if zoneAfter == 0 {
			continue
		}
		shareAfter := float64(zoneAfter) / float64(samples)
		shareBefore := float64(zoneBefore) / float64(samples)
		opened := shareAfter - shareBefore
		zoneAvg := float64(zoneScore) / float64(zoneAfter)
		if shareAfter < threatMinZoneShare || opened < threatMinOpened || zoneAvg < threatMinZoneAvg || opened <= bestOpened {
			continue
		}
		keys := make([]string, 0, len(tally))
		for k := range tally {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if counts[keys[i]] != counts[keys[j]] {
				return counts[keys[i]] > counts[keys[j]]
			}
			return tally[keys[i]].Score > tally[keys[j]].Score
		})
		var top []drillReply
		for _, k := range keys[:min(3, len(keys))] {
			r := tally[k]
			r.Share = math.Round(float64(counts[k])/float64(samples)*100) / 100
			top = append(top, r)
		}
		var cells []threatHeatCell
		for r := 0; r < 15; r++ {
			for c := 0; c < 15; c++ {
				if v := float64(heat[r][c]) / float64(hmax); v >= 0.05 {
					cells = append(cells, threatHeatCell{Row: r, Col: c, V: math.Round(v*100) / 100})
				}
			}
		}
		bestOpened = opened
		best = &threatQuestion{
			Board: boardStrings(snap.bd), Rack: snap.rack, BagCount: snap.bagCount, MyScore: snap.myScore, OppScore: snap.oppScore,
			Move:       defenseOptionFor(gd, snap, cand, float64(total)/float64(samples), nil, nil),
			Heat:       cells,
			Hot:        drillHotSquare{Square: fmt.Sprintf("%d%c", hr+1, 'A'+hc), Row: hr, Col: hc, Kind: premiumKind[[2]int{hr, hc}], Share: math.Round(shareAfter*100) / 100},
			ZoneBefore: math.Round(shareBefore*100) / 100,
			ZoneAvg:    math.Round(zoneAvg*10) / 10,
			TopReplies: top,
			Samples:    samples,
		}
	}
	return best
}

func TestGenerateThreatDrills(t *testing.T) {
	runDrillGenerator(t, "threat", "thr", func(gd *kwg.KWG, snap drillSnapshot, samples int, rng *rand.Rand) drillQuestion {
		if q := threatQuestionFrom(gd, snap, samples, rng); q != nil {
			return q
		}
		return nil
	})
}
