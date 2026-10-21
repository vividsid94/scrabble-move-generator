package main

// Opponent hotspot score for 90s Wally, the rules bot: a fixed table, measured
// once (2026-10-21) from about 15,600 non-bingo turns of Wally-vs-Wally games.
// Each point is the board's hotspot load after a move (hotspots_for_rules_bot.go)
// and the opponent's average score on the very next turn, smoothed so it never
// goes down as load rises. Non-bingo turns only - bingos are bingo defense's
// job. The same curve held early, middle and late in the game.
var opponentHotspotCurve = [][2]float64{
	{144, 22.72},
	{231, 25.21},
	{294, 25.45},
	{345, 26.88},
	{390, 27.7},
	{430, 27.85},
	{489, 29.06},
	{568, 30.54},
	{634, 31.79},
	{688, 32.5},
	{758, 32.51},
	{852, 33.66},
	{1037, 36.15},
}

// opponentHotspotScore reads the curve, with straight lines between points
// and flat beyond the ends.
func opponentHotspotScore(load float64) float64 {
	curve := opponentHotspotCurve
	if load <= curve[0][0] {
		return curve[0][1]
	}
	for i := 1; i < len(curve); i++ {
		if load <= curve[i][0] {
			x0, y0, x1, y1 := curve[i-1][0], curve[i-1][1], curve[i][0], curve[i][1]
			return y0 + (y1-y0)*(load-x0)/(x1-x0)
		}
	}
	return curve[len(curve)-1][1]
}
