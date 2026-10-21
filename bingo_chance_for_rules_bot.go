package main

// Opponent bingo chance for 90s Wally, the rules bot: a fixed table, measured
// once (2026-10-20) from about 118,000 turns of Wally-vs-Wally games. Each
// point is a board openness (7-letter + 8-letter openness after a move) and
// how often the opponent played a bingo on the very next turn, smoothed so it
// never goes down as openness rises. Looked up at play time; no simulation.
var opponentBingoCurve = [][2]float64{
	{0, 0.0634},
	{0.048, 0.0684},
	{0.15, 0.1111},
	{0.25, 0.1395},
	{0.4, 0.153},
	{0.574, 0.1747},
	{0.726, 0.1776},
	{0.898, 0.2008},
	{1.121, 0.2074},
	{1.371, 0.2257},
	{1.622, 0.2365},
	{1.871, 0.2425},
	{2.232, 0.2481},
	{2.737, 0.261},
	{3.443, 0.2649},
	{4.738, 0.2715},
}

// netBingoCost: what an opponent bingo costs in points - their average
// next-turn score with a bingo (78.6) minus without one (29.5), measured from
// the same games. Steady through the game (49.5 early, 48.6 middle, 49.4 late).
const netBingoCost = 49.1

// opponentBingoChance reads the curve, with straight lines between points
// and flat beyond the ends.
func opponentBingoChance(openness float64) float64 {
	curve := opponentBingoCurve
	if openness <= curve[0][0] {
		return curve[0][1]
	}
	for i := 1; i < len(curve); i++ {
		if openness <= curve[i][0] {
			x0, p0, x1, p1 := curve[i-1][0], curve[i-1][1], curve[i][0], curve[i][1]
			return p0 + (p1-p0)*(openness-x0)/(x1-x0)
		}
	}
	return curve[len(curve)-1][1]
}
