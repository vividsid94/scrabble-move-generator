package main

// Hotspot values for 90s Wally, the rules bot (hotspots_for_rules_bot.go): the
// average non-bingo score of plays, by the premium squares their new tiles
// covered. Measured once (2026-10-20/21) from about 70,000 plays each in
// Wally-vs-Wally games; fixed at play time. Only keys with enough plays are
// kept; a shape looks up the most detailed key that exists.

// hotspotFlaggedValues: single premium type, or a conjunct pattern (see
// hotspotPattern), plus whether any covered premium square sits next to a
// tile already on the board and whether any such tile is a vowel. 40+ plays.
var hotspotFlaggedValues = map[string]float64{
	"DL _ DL (3 tiles) · next to a tile · no vowel": 23.391892, // 74 plays
	"DL _ DL (3 tiles) · next to a tile · vowel": 29.329694, // 458 plays
	"DL _ DL (4 tiles) · next to a tile · no vowel": 23.980583, // 103 plays
	"DL _ DL (4 tiles) · next to a tile · vowel": 29.466912, // 272 plays
	"DL _ DL (4 tiles) · not next to a tile": 25.086957, // 46 plays
	"DL _ DL (5 tiles) · next to a tile · no vowel": 26.098361, // 61 plays
	"DL _ DL (5 tiles) · next to a tile · vowel": 30.275362, // 138 plays
	"DL _ _ DW (4 tiles) · next to a tile · no vowel": 34.271574, // 394 plays
	"DL _ _ DW (4 tiles) · next to a tile · vowel": 35.349345, // 458 plays
	"DL _ _ DW (5 tiles) · next to a tile · no vowel": 35.037594, // 133 plays
	"DL _ _ DW (5 tiles) · next to a tile · vowel": 33.205479, // 73 plays
	"DL _ _ DW (5 tiles) · not next to a tile": 32.410667, // 375 plays
	"DL _ _ DW (6 tiles) · not next to a tile": 32.623853, // 109 plays
	"DL _ _ TW (4 tiles) · next to a tile · no vowel": 36.422368, // 760 plays
	"DL _ _ TW (4 tiles) · next to a tile · vowel": 42.341333, // 750 plays
	"DL _ _ TW (4 tiles) · not next to a tile": 43.840796, // 201 plays
	"DL _ _ TW (5 tiles) · next to a tile · no vowel": 39.423256, // 215 plays
	"DL _ _ TW (5 tiles) · next to a tile · vowel": 42.210526, // 190 plays
	"DL _ _ TW (5 tiles) · not next to a tile": 39.025445, // 786 plays
	"DL _ _ TW (6 tiles) · not next to a tile": 41.513317, // 413 plays
	"DL _ _ _ DL (5 tiles) · next to a tile · no vowel": 22.666667, // 48 plays
	"DL _ _ _ DL (5 tiles) · next to a tile · vowel": 28.719512, // 82 plays
	"DL _ _ _ DW (5 tiles) · next to a tile · no vowel": 30.955665, // 203 plays
	"DL _ _ _ DW (5 tiles) · next to a tile · vowel": 36.313480, // 319 plays
	"DL _ _ _ DW (5 tiles) · not next to a tile": 31.711409, // 149 plays
	"DL _ _ _ DW (6 tiles) · next to a tile · no vowel": 32.089888, // 89 plays
	"DL _ _ _ DW (6 tiles) · next to a tile · vowel": 35.728571, // 70 plays
	"DL _ _ _ DW (6 tiles) · not next to a tile": 32.323353, // 167 plays
	"DL _ _ _ TW (5 tiles) · next to a tile · no vowel": 43.191964, // 224 plays
	"DL _ _ _ TW (5 tiles) · next to a tile · vowel": 48.207143, // 140 plays
	"DL _ _ _ TW (5 tiles) · not next to a tile": 49.822222, // 45 plays
	"DL _ _ _ TW (6 tiles) · next to a tile · no vowel": 42.544715, // 123 plays
	"DL _ _ _ TW (6 tiles) · next to a tile · vowel": 44.385714, // 70 plays
	"DL _ _ _ TW (6 tiles) · not next to a tile": 43.424870, // 193 plays
	"DL _ _ x DW (4 tiles) · next to a tile · no vowel": 31.163934, // 122 plays
	"DL _ _ x DW (4 tiles) · next to a tile · vowel": 34.479675, // 123 plays
	"DL _ _ x DW (5 tiles) · next to a tile · no vowel": 29.573034, // 89 plays
	"DL _ _ x DW (5 tiles) · next to a tile · vowel": 32.333333, // 63 plays
	"DL _ x DW (3 tiles) · next to a tile · no vowel": 29.511278, // 133 plays
	"DL _ x DW (3 tiles) · next to a tile · vowel": 31.469136, // 162 plays
	"DL _ x DW (4 tiles) · next to a tile · no vowel": 29.868852, // 61 plays
	"DL _ x TW (3 tiles) · next to a tile · no vowel": 38.702454, // 326 plays
	"DL _ x TW (3 tiles) · next to a tile · vowel": 40.566434, // 143 plays
	"DL _ x TW (4 tiles) · next to a tile · no vowel": 38.548387, // 124 plays
	"DL _ x TW (4 tiles) · next to a tile · vowel": 40.904762, // 63 plays
	"DL _ x TW (5 tiles) · next to a tile · no vowel": 41.916667, // 60 plays
	"DL _ x _ DW (4 tiles) · not next to a tile": 30.765957, // 141 plays
	"DL _ x _ DW (5 tiles) · not next to a tile": 30.036585, // 82 plays
	"DL _ x _ TW (4 tiles) · not next to a tile": 41.838710, // 62 plays
	"DL x DL (2 tiles) · next to a tile · vowel": 23.160000, // 275 plays
	"DL x DL (3 tiles) · next to a tile · no vowel": 18.476190, // 42 plays
	"DL x DL (3 tiles) · next to a tile · vowel": 24.647959, // 196 plays
	"DL x DL (4 tiles) · next to a tile · vowel": 26.250000, // 72 plays
	"DL x DL _ _ _ DW (6 tiles) · next to a tile · vowel": 33.160714, // 56 plays
	"DL x _ DW (3 tiles) · next to a tile · no vowel": 26.776119, // 67 plays
	"DL x _ DW (3 tiles) · next to a tile · vowel": 34.552239, // 134 plays
	"DL x _ DW (4 tiles) · next to a tile · vowel": 33.439024, // 41 plays
	"DL x _ TW (3 tiles) · next to a tile · no vowel": 33.063559, // 236 plays
	"DL x _ TW (3 tiles) · next to a tile · vowel": 41.267399, // 273 plays
	"DL x _ TW (4 tiles) · next to a tile · no vowel": 34.821429, // 140 plays
	"DL x _ TW (4 tiles) · next to a tile · vowel": 42.578947, // 76 plays
	"DL x _ TW (5 tiles) · next to a tile · no vowel": 36.298507, // 67 plays
	"DL x _ _ DW (4 tiles) · next to a tile · no vowel": 28.191489, // 47 plays
	"DL x _ _ DW (4 tiles) · next to a tile · vowel": 32.504065, // 123 plays
	"DL x _ _ DW (5 tiles) · next to a tile · vowel": 32.851351, // 74 plays
	"DL · next to a tile · no vowel": 17.932283, // 1270 plays
	"DL · next to a tile · vowel": 23.190044, // 3194 plays
	"DL · not next to a tile": 20.066406, // 1536 plays
	"DW _ _ _ TL (5 tiles) · next to a tile · no vowel": 33.609489, // 274 plays
	"DW _ _ _ TL (5 tiles) · next to a tile · vowel": 41.107843, // 306 plays
	"DW _ _ _ TL (5 tiles) · not next to a tile": 43.933333, // 75 plays
	"DW _ _ _ TL (6 tiles) · next to a tile · no vowel": 33.388350, // 103 plays
	"DW _ _ _ TL (6 tiles) · next to a tile · vowel": 41.890000, // 100 plays
	"DW _ _ _ TL (6 tiles) · not next to a tile": 36.850220, // 227 plays
	"DW _ _ _ _ x DW (6 tiles) · next to a tile · vowel": 45.636364, // 44 plays
	"DW _ _ _ x _ DW (6 tiles) · not next to a tile": 43.576923, // 52 plays
	"DW _ _ x TL (4 tiles) · next to a tile · no vowel": 33.692308, // 65 plays
	"DW _ _ x TL (4 tiles) · next to a tile · vowel": 42.373494, // 83 plays
	"DW _ _ x TL (5 tiles) · next to a tile · vowel": 38.340909, // 44 plays
	"DW _ _ x _ _ DW (6 tiles) · not next to a tile": 44.848276, // 145 plays
	"DW _ x _ TL (4 tiles) · not next to a tile": 37.318681, // 182 plays
	"DW _ x _ TL (5 tiles) · not next to a tile": 35.182692, // 104 plays
	"DW x _ _ TL (4 tiles) · next to a tile · no vowel": 36.737864, // 103 plays
	"DW x _ _ TL (4 tiles) · next to a tile · vowel": 38.902655, // 113 plays
	"DW x _ _ TL (5 tiles) · next to a tile · no vowel": 34.957746, // 71 plays
	"DW x _ _ TL (5 tiles) · next to a tile · vowel": 34.414634, // 41 plays
	"DW · next to a tile · no vowel": 24.965245, // 4057 plays
	"DW · next to a tile · vowel": 26.650837, // 5433 plays
	"DW · not next to a tile": 25.083262, // 5825 plays
	"TL _ _ _ TL (5 tiles) · next to a tile · no vowel": 31.026549, // 113 plays
	"TL _ _ _ TL (5 tiles) · next to a tile · vowel": 35.784906, // 265 plays
	"TL _ _ _ TL (5 tiles) · not next to a tile": 33.250000, // 40 plays
	"TL _ _ _ TL (6 tiles) · next to a tile · vowel": 31.408163, // 49 plays
	"TL _ _ _ TL (6 tiles) · not next to a tile": 28.886792, // 53 plays
	"TL _ _ x TL (4 tiles) · next to a tile · no vowel": 27.385714, // 70 plays
	"TL _ _ x TL (4 tiles) · next to a tile · vowel": 31.186992, // 123 plays
	"TL _ x _ TL (4 tiles) · next to a tile · vowel": 34.744681, // 47 plays
	"TL _ x _ TL (4 tiles) · not next to a tile": 26.329609, // 179 plays
	"TL _ x _ TL (5 tiles) · not next to a tile": 25.211268, // 71 plays
	"TL · next to a tile · no vowel": 19.589453, // 1498 plays
	"TL · next to a tile · vowel": 30.321690, // 4094 plays
	"TL · not next to a tile": 25.537590, // 3179 plays
	"TW · next to a tile · no vowel": 32.522629, // 928 plays
	"TW · next to a tile · vowel": 33.658635, // 498 plays
	"TW · not next to a tile": 32.217748, // 4271 plays
	"none": 12.482565, // 5535 plays
}

// hotspotPatternValues: conjunct patterns without the flags. 40+ plays.
var hotspotPatternValues = map[string]float64{
	"DL": 21.322863, // 5826 plays
	"DL _ DL (3 tiles)": 28.158645, // 561 plays
	"DL _ DL (4 tiles)": 28.071429, // 434 plays
	"DL _ DL (5 tiles)": 27.289157, // 166 plays
	"DL _ DL (6 tiles)": 27.181818, // 55 plays
	"DL _ _ DW (4 tiles)": 34.509229, // 921 plays
	"DL _ _ DW (5 tiles)": 34.027869, // 610 plays
	"DL _ _ DW (6 tiles)": 34.551724, // 145 plays
	"DL _ _ TW (4 tiles)": 40.184316, // 1747 plays
	"DL _ _ TW (5 tiles)": 40.321485, // 1266 plays
	"DL _ _ TW (6 tiles)": 41.590476, // 420 plays
	"DL _ _ _ DL (5 tiles)": 27.247619, // 105 plays
	"DL _ _ _ DL (6 tiles)": 25.490909, // 55 plays
	"DL _ _ _ DW (5 tiles)": 33.236311, // 694 plays
	"DL _ _ _ DW (6 tiles)": 31.758410, // 327 plays
	"DL _ _ _ TW (5 tiles)": 42.214286, // 420 plays
	"DL _ _ _ TW (6 tiles)": 43.270195, // 359 plays
	"DL _ _ x DW (4 tiles)": 32.958333, // 240 plays
	"DL _ _ x DW (5 tiles)": 30.591241, // 137 plays
	"DL _ _ x DW (6 tiles)": 31.836735, // 49 plays
	"DL _ _ x TW (4 tiles)": 40.588235, // 51 plays
	"DL _ x DW (3 tiles)": 30.683636, // 275 plays
	"DL _ x DW (4 tiles)": 31.206897, // 87 plays
	"DL _ x TW (3 tiles)": 38.599182, // 489 plays
	"DL _ x TW (4 tiles)": 41.468354, // 158 plays
	"DL _ x TW (5 tiles)": 39.493671, // 79 plays
	"DL _ x _ DW (4 tiles)": 32.677966, // 177 plays
	"DL _ x _ DW (5 tiles)": 31.831325, // 83 plays
	"DL _ x _ TW (4 tiles)": 38.791667, // 72 plays
	"DL _ x _ TW (5 tiles)": 39.854839, // 62 plays
	"DL x DL (2 tiles)": 22.606154, // 325 plays
	"DL x DL (3 tiles)": 21.870968, // 217 plays
	"DL x DL (4 tiles)": 23.365854, // 123 plays
	"DL x DL (5 tiles)": 21.132075, // 53 plays
	"DL x DL _ _ _ DW (6 tiles)": 34.900000, // 70 plays
	"DL x _ DW (3 tiles)": 30.568182, // 176 plays
	"DL x _ DW (4 tiles)": 29.363636, // 55 plays
	"DL x _ TW (3 tiles)": 36.404000, // 500 plays
	"DL x _ TW (4 tiles)": 36.955446, // 202 plays
	"DL x _ TW (5 tiles)": 39.021739, // 92 plays
	"DL x _ _ DW (4 tiles)": 31.348718, // 195 plays
	"DL x _ _ DW (5 tiles)": 30.271028, // 107 plays
	"DL x _ _ TW (4 tiles)": 41.461538, // 65 plays
	"DL x _ _ TW (5 tiles)": 37.045455, // 44 plays
	"DW": 25.481986, // 15266 plays
	"DW _ _ _ TL (5 tiles)": 38.692185, // 627 plays
	"DW _ _ _ TL (6 tiles)": 36.668555, // 353 plays
	"DW _ _ _ _ x DW (6 tiles)": 44.444444, // 81 plays
	"DW _ _ _ x _ DW (6 tiles)": 41.739130, // 69 plays
	"DW _ _ x TL (4 tiles)": 36.240602, // 133 plays
	"DW _ _ x TL (5 tiles)": 35.260274, // 73 plays
	"DW _ _ x _ _ DW (6 tiles)": 44.268657, // 134 plays
	"DW _ x _ TL (4 tiles)": 38.285199, // 277 plays
	"DW _ x _ TL (5 tiles)": 38.914063, // 128 plays
	"DW x _ _ TL (4 tiles)": 38.478261, // 207 plays
	"DW x _ _ TL (5 tiles)": 36.076923, // 104 plays
	"TL": 27.108966, // 8755 plays
	"TL _ _ _ TL (5 tiles)": 34.036458, // 384 plays
	"TL _ _ _ TL (6 tiles)": 30.015748, // 127 plays
	"TL _ _ x TL (4 tiles)": 29.004878, // 205 plays
	"TL _ _ x TL (5 tiles)": 27.377049, // 61 plays
	"TL _ x _ TL (4 tiles)": 28.513834, // 253 plays
	"TL _ x _ TL (5 tiles)": 26.305882, // 85 plays
	"TW": 32.609865, // 5616 plays
	"none": 12.426059, // 5572 plays
}

// hotspotComboValues: just the premium types covered, TW/DW/TL/DL order. 30+
// plays.
var hotspotComboValues = map[string]float64{
	"DL": 21.579177, // 5955 plays
	"DL + DL": 26.013977, // 2218 plays
	"DW": 25.586092, // 15257 plays
	"DW + DL": 32.951642, // 4508 plays
	"DW + DL + DL": 34.597403, // 154 plays
	"DW + DW": 44.459893, // 374 plays
	"DW + TL": 37.916348, // 2092 plays
	"TL": 26.791585, // 8651 plays
	"TL + TL": 30.002517, // 1192 plays
	"TW": 32.391055, // 5746 plays
	"TW + DL": 40.480426, // 6105 plays
	"none": 12.377305, // 5693 plays
}
