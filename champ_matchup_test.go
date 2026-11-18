package main

// Local-only matchup: Wilma (champ_sim.go's sims) against Wally (rank 1,
// score + leave) over full games. A test file, so it's never part of the
// server build - run it on your own machine:
//
//	MATCHUP_GAMES=200 go test -run TestChampMatchup -v -timeout 0
//
// Optional: MATCHUP_MIN / MATCHUP_MAX (playouts per candidate, default
// Quackle's 132 / 330), MATCHUP_PARALLEL (games at once, default 2 - each
// sim already uses half the cores), MATCHUP_LEXICON.
//
// Both sides play the same static move once the bag has fewer than 3
// tiles (Wilma doesn't do preendgames or endgames), so any difference
// comes from her middle-game sims. Wilma moves first in half the games.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/kwg"
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

type matchupResult struct {
	wilmaScore, wallyScore int
	wilmaMoves             int
	simTime                time.Duration
}

// playMatchupGame plays one game; wilmaFirst decides who opens.
func playMatchupGame(gd *kwg.KWG, cfg champConfig, wilmaFirst bool, rng *rand.Rand, logf func(string, ...any)) matchupResult {
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
		if n <= 0 {
			return ""
		}
		s := string(bag[len(bag)-n:])
		bag = bag[:len(bag)-n]
		return s
	}
	// Player 0 is Wilma, player 1 is Wally.
	racks := [2]string{draw(7), draw(7)}
	scores := [2]int{}
	scoreless := 0
	p := 1
	if wilmaFirst {
		p = 0
	}
	var res matchupResult

	for turn := 0; turn < 200; turn++ {
		var c champChoice
		if p == 0 && len(bag) >= 3 {
			// Wilma: sims, with the bag + Wally's rack as the unseen tiles.
			start := time.Now()
			root := &champRoot{gd: gd, bd: bd, rack: racks[0], unseen: []rune(string(bag) + racks[1]), myScore: scores[0], oppScore: scores[1], plies: 2}
			if len(bag) <= 14 {
				root.plies = -1
			}
			ranked, _ := champRank(root, champCandidates(gd, bd, racks[0], len(bag)), cfg)
			if len(ranked) > 0 {
				c = ranked[0].choice
			} else {
				c = champChoice{kind: champPass, leave: sortLeaveString(racks[0])}
			}
			res.wilmaMoves++
			res.simTime += time.Since(start)
		} else {
			c = champStaticBest(gen, racks[p], len(bag), rackPointValue(racks[1-p]))
		}

		if logf != nil {
			desc := "pass"
			if c.kind == champPlay {
				desc = c.m.ShortDescription()
			} else if c.kind == champExchange {
				desc = "exch " + c.exch
			}
			logf("%s rack %-7s bag %2d  %-20s %3d  scores %v", []string{"WILMA", "WALLY"}[p], racks[p], len(bag), desc, c.score, scores)
		}
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
		if len(racks[p]) == 0 && len(bag) == 0 {
			scores[p] += 2 * rackPointValue(racks[1-p])
			break
		}
		if scoreless >= 6 {
			scores[0] -= rackPointValue(racks[0])
			scores[1] -= rackPointValue(racks[1])
			break
		}
		p = 1 - p
	}
	res.wilmaScore, res.wallyScore = scores[0], scores[1]
	return res
}

func TestChampMatchup(t *testing.T) {
	if err := initService(); err != nil {
		t.Fatal(err)
	}
	games := envInt("MATCHUP_GAMES", 50)
	parallel := envInt("MATCHUP_PARALLEL", 2)
	cfg := champConfig{
		candidates:    champDefaultCandidates,
		minIterations: envInt("MATCHUP_MIN", 132),
		maxIterations: envInt("MATCHUP_MAX", 330),
		budget:        60 * time.Second,
	}
	gd, lexName, err := resolveLexicon(os.Getenv("MATCHUP_LEXICON"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Wilma (%d/%d playouts) vs Wally, %d games, %s, %d at a time", cfg.minIterations, cfg.maxIterations, games, lexName, parallel)

	var mu sync.Mutex
	var wins, draws, losses float64
	var spreadSum, spreadSq float64
	var simTime time.Duration
	var wilmaMoves, done int
	start := time.Now()

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < parallel; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)*104729))
			for g := range jobs {
				var logf func(string, ...any)
				if os.Getenv("MATCHUP_VERBOSE") != "" {
					logf = t.Logf
				}
				r := playMatchupGame(gd, cfg, g%2 == 0, rng, logf)
				spread := float64(r.wilmaScore - r.wallyScore)
				mu.Lock()
				switch {
				case spread > 0:
					wins++
				case spread < 0:
					losses++
				default:
					draws++
				}
				spreadSum += spread
				spreadSq += spread * spread
				simTime += r.simTime
				wilmaMoves += r.wilmaMoves
				done++
				if done%10 == 0 || done == games {
					n := float64(done)
					t.Logf("%4d games  Wilma %.1f%% wins  avg spread %+.1f  (%.0fs elapsed)", done, 100*(wins+draws/2)/n, spreadSum/n, time.Since(start).Seconds())
				}
				mu.Unlock()
			}
		}(w)
	}
	for g := 0; g < games; g++ {
		jobs <- g
	}
	close(jobs)
	wg.Wait()

	n := float64(games)
	winRate := (wins + draws/2) / n
	se := math.Sqrt(winRate * (1 - winRate) / n)
	mean := spreadSum / n
	sd := math.Sqrt(spreadSq/n - mean*mean)
	fmt.Printf("\nRESULT: Wilma %.0f-%.0f-%.0f vs Wally (W-L-D) = %.1f%% ± %.1f%% (95%%: ±%.1f%%)\n", wins, losses, draws, 100*winRate, 100*se, 196*se)
	fmt.Printf("RESULT: average spread %+.1f ± %.1f per game; %.2fs per Wilma move over %d moves; %.0fs total\n", mean, 1.96*sd/math.Sqrt(n), simTime.Seconds()/float64(max(wilmaMoves, 1)), wilmaMoves, time.Since(start).Seconds())
}
