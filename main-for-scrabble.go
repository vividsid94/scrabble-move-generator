package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/domino14/word-golib/kwg"
	"github.com/domino14/word-golib/tilemapping"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/config"
	"github.com/domino14/macondo/cross_set"
	macondomove "github.com/domino14/macondo/move"
	"github.com/domino14/macondo/movegen"
)

// Request/Response structures
// Board is a 15x15 array of strings ("" for empty, or a single letter)

var localhostOriginRe = regexp.MustCompile(`^http://localhost:\d+$`)

func setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	allowedOrigins := map[string]bool{
		"https://tileturnover.com": true,
		"https://whiffers230.com":  true,
	}
	// Vite (and old Netlify Dev) don't stick to one port, so any localhost
	// port is allowed in dev rather than chasing whichever one got picked.
	if allowedOrigins[origin] || localhostOriginRe.MatchString(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Max-Age", "86400") // 24 hours
}

type PremiumSquare struct {
	Row    int    `json:"row"`    // 0-14
	Col    int    `json:"col"`    // 0-14
	Type   string `json:"type"`   // "DLS", "TLS", "DWS", "TWS", "CENTER"
}

type GenerateMovesRequest struct {
	Rack          string          `json:"rack"`
	Board         [][]string      `json:"board"`         // 15x15 board as strings
	PremiumSquares []PremiumSquare `json:"premiumSquares,omitempty"` // Optional custom premium squares
	TopN          int             `json:"topN,omitempty"`
	PoolSize      int             `json:"poolSize,omitempty"` // Tiles remaining in the bag; exchange candidates are only generated when this is >= 7
	// PauseMode, when set, asks this endpoint to also compute Puzzle Mode's
	// own "should we pause here" decision (see decideGeneratePauseReason) in
	// this same request/response round-trip, rather than that decision
	// happening as a separate step against the frontend's own local state
	// afterward. Omitted entirely by every other caller (Play mode's bot
	// move, Endgame mode, "ask for best moves") - PauseReason then stays
	// unset too, so their response shape is byte-identical to before this
	// field existed.
	PauseMode string `json:"pauseMode,omitempty"`
	// Lexicon selects which loaded KWG to use ("NWL23" or "CSW24") -
	// resolveLexicon defaults to defaultLexicon when omitted, so existing
	// callers that don't send this yet are unaffected.
	Lexicon string `json:"lexicon,omitempty"`
}

// Move is a single ranked candidate: a word play, or (when IsExchange is
// true) an exchange. Direction/StartPosition/Tiles are only populated for
// exchange entries - word plays keep their existing Position/Word string
// format, which the client's display pipeline already depends on.
type Move struct {
	Position      string     `json:"position"`
	Word          string     `json:"word"`
	Score         int        `json:"score"`
	Leave         string     `json:"leave"`
	LeaveValue    float64    `json:"leaveValue"`
	TotalValue    float64    `json:"totalValue"`
	IsExchange    bool       `json:"isExchange,omitempty"`
	Direction     string     `json:"direction,omitempty"`
	StartPosition string     `json:"startPosition,omitempty"`
	Tiles         []MoveTile `json:"tiles,omitempty"`
}

type GenerateMovesResponse struct {
	Moves []Move `json:"moves"`
	Total int    `json:"total"`
	// PauseReason is only ever set when the request included PauseMode -
	// see decideGeneratePauseReason. omitempty keeps every other caller's
	// response identical to before this field existed.
	PauseReason string `json:"pauseReason,omitempty"`
	// Lexicon echoes back which lexicon actually generated these moves,
	// matching every other endpoint's response.
	Lexicon string `json:"lexicon,omitempty"`
}

type ValidateWordRequest struct {
	Word    string `json:"word"`
	Lexicon string `json:"lexicon,omitempty"`
}

type ValidateWordResponse struct {
	Word      string `json:"word"`
	IsValid   bool   `json:"isValid"`
	Lexicon   string `json:"lexicon"`
}

type SubanagramSearchRequest struct {
	Letters string `json:"letters"`
	Lexicon string `json:"lexicon,omitempty"`
}

type SubanagramSearchResponse struct {
	Letters      string   `json:"letters"`
	Subanagrams  []string `json:"subanagrams"`
	Count        int      `json:"count"`
	Lexicon      string   `json:"lexicon"`
}

type AnagramSearchRequest struct {
	Letters string `json:"letters"`
	Lexicon string `json:"lexicon,omitempty"`
}

type AnagramSearchResponse struct {
	Letters   string   `json:"letters"`
	Anagrams  []string `json:"anagrams"`
	Count     int      `json:"count"`
	Lexicon   string   `json:"lexicon"`
}

type BulkMoveGenRequest struct {
	Board              [][]string      `json:"board"`                        // 15x15 board as strings
	TilePool           string          `json:"tilePool"`                     // Available tiles (e.g., "AABCDEFG..."); "?" = blank
	PremiumSquares     []PremiumSquare `json:"premiumSquares,omitempty"`     // Optional custom premium squares
	Iterations         int             `json:"iterations,omitempty"`         // Number of iterations (default 1000)
	IncludeMoveDetails bool            `json:"includeMoveDetails,omitempty"` // When true, include per-iteration move details
	OurLeave           string          `json:"ourLeave,omitempty"`           // Optional fixed tiles for ourReply rack (rest drawn randomly)
	Lexicon            string          `json:"lexicon,omitempty"`
}

// MoveTile describes one square in a played word (new tiles and play-throughs).
type MoveTile struct {
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	Letter  string `json:"letter"`
	IsNew   bool   `json:"isNew"`
	IsBlank bool   `json:"isBlank"`
}

// DetailedMove is the renderable move shape used by clients that need
// word/position/tiles (not just score aggregates). IsExchange marks an
// entry built from an exchange candidate rather than a word play - Tiles
// then holds the exchanged letters (via exchangeTilesToMoveTiles), all
// IsNew:false since nothing gets placed on the board.
type DetailedMove struct {
	Word          string     `json:"word"`
	Score         int        `json:"score"`
	Direction     string     `json:"direction"` // "right", "down", or "exchange"
	StartPosition string     `json:"startPosition"`
	Tiles         []MoveTile `json:"tiles"`
	IsExchange    bool       `json:"isExchange,omitempty"`
	// IsOutplay is only ever set by /solve-endgame (endgame_solver.go) -
	// true for the one move in a solved line that empties its player's
	// rack and ends the game right then. omitempty keeps every other
	// endpoint's response byte-identical to before this field existed.
	IsOutplay bool `json:"isOutplay,omitempty"`
}

type BulkIterationDetail struct {
	OpponentMove *DetailedMove `json:"opponentMove"`
	OurReply     *DetailedMove `json:"ourReply"`
}

type BulkMoveGenResponse struct {
	Iterations       int                   `json:"iterations"`
	AverageScore     float64               `json:"averageScore"`
	BingoPercent     float64               `json:"bingoPercent"`
	TotalBingos      int                   `json:"totalBingos"`
	TotalScore       int                   `json:"totalScore"`
	Lexicon          string                `json:"lexicon"`
	IterationDetails []BulkIterationDetail `json:"iterationDetails,omitempty"`
}

type ValidateWordsRequest struct {
	Words   []string `json:"words"`
	Lexicon string   `json:"lexicon,omitempty"`
}

type WordValidation struct {
	Word    string `json:"word"`
	IsValid bool   `json:"isValid"`
}

type ValidateWordsResponse struct {
	Words    []WordValidation `json:"words"`
	Count    int              `json:"count"`
	Valid    int              `json:"valid"`
	Invalid  int              `json:"invalid"`
	Lexicon  string           `json:"lexicon"`
}

// Global state (safe for demo, not for production concurrency). lexica is
// the one exception to "not for production concurrency" - it's populated
// once in initService() and only ever READ afterward (never mutated per
// request), so concurrent requests resolving different lexicons out of it
// via resolveLexicon is safe. alph/ld stay single, shared values rather
// than per-lexicon maps: NWL and CSW both use the same English tile set
// (26 letters + 2 blanks, same distribution/point values), so there's
// nothing lexicon-specific about either one.
var (
	lexica map[string]*kwg.KWG
	alph   *tilemapping.TileMapping
	ld     *tilemapping.LetterDistribution
)

// defaultLexicon is what every existing caller gets when a request omits
// Lexicon entirely - keeps every pre-existing integration (frontend calls
// that don't send the field yet, this file's own comments before this
// feature existed) working unchanged.
const defaultLexicon = "NWL23"

// resolveLexicon looks up the requested lexicon's KWG, defaulting to
// defaultLexicon when name is empty. Returns the resolved name alongside
// the KWG so callers can echo back which lexicon actually applied (several
// response structs already have their own Lexicon field for this).
func resolveLexicon(name string) (*kwg.KWG, string, error) {
	if name == "" {
		name = defaultLexicon
	}
	g, ok := lexica[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown lexicon %q", name)
	}
	return g, name, nil
}

// createCustomBoardLayout creates a board layout from premium square definitions
// If premiumSquares is nil or empty, returns the default CrosswordGameBoard
// Premium square types: "DLS" (double letter = '), "TLS" (triple letter = -), 
// "DWS" (double word = "), "TWS" (triple word = =), "CENTER" (center square = -)
// Format based on Macondo board layout: ' = DLS, - = DWS (SWITCHED), " = TLS (SWITCHED), = = TWS
func createCustomBoardLayout(premiumSquares []PremiumSquare) []string {
	// If no custom premium squares, use default
	if len(premiumSquares) == 0 {
		return board.CrosswordGameBoard
	}
	
	// Start with a base layout - use default board as base
	layout := make([]string, 15)
	baseLayout := board.CrosswordGameBoard
	
	// Copy the base layout
	for i := 0; i < 15; i++ {
		layout[i] = baseLayout[i]
	}
	
	// Convert layout strings to byte slices for modification
	layoutBytes := make([][]byte, 15)
	for i := 0; i < 15; i++ {
		layoutBytes[i] = []byte(layout[i])
	}
	
	// Apply custom premium squares (overrides default board squares)
	for _, ps := range premiumSquares {
		if ps.Row < 0 || ps.Row >= 15 || ps.Col < 0 || ps.Col >= 15 {
			continue // Skip invalid coordinates
		}
		
		var char byte
		switch strings.ToUpper(ps.Type) {
			case "DLS": // Double Letter Score
				char = '\'' // single quote
			case "TLS": // Triple Letter Score
				char = '"' // double quote (SWITCHED - was DWS)
			case "DWS": // Double Word Score
				char = '-' // hyphen (SWITCHED - was TLS)
			case "TWS": // Triple Word Score
				char = '=' // equals sign
		case "CENTER": // Center square (DWS for standard Scrabble)
			char = '-' // hyphen (DWS after switch)
		case "REGULAR", "": // Regular square (no premium)
			char = ' ' // space
		default:
			continue // Skip unknown types
		}
		
		layoutBytes[ps.Row][ps.Col] = char
	}
	
	// Convert back to strings
	for i := 0; i < 15; i++ {
		layout[i] = string(layoutBytes[i])
	}
	
	return layout
}

func main() {
	if err := initService(); err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/generate-moves", generateMovesHandler)
	http.HandleFunc("/validate-word", validateWordHandler)
	http.HandleFunc("/validate-words", validateWordsHandler)
	http.HandleFunc("/find-subanagrams", findSubanagramsHandler)
	http.HandleFunc("/find-anagrams", findAnagramsHandler)
	http.HandleFunc("/bulk-move-gen", bulkMoveGenHandler)
	http.HandleFunc("/solve-endgame", solveEndgameHandler)
	http.HandleFunc("/simulate-series", simulateSeriesHandler)
	http.HandleFunc("/rulesbot-debug", rulesBotDebugHandler)
	http.HandleFunc("/proxy", proxyHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Printf("\n🚀 Macondo MoveGen Service running on :%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func initService() error {
	fmt.Println("=== Initializing Macondo Move Generation Service ===")
	cfg := config.DefaultConfig()
	cfg.Set("data-path", ".")

	lexica = make(map[string]*kwg.KWG)
	for _, name := range []string{"NWL23", "CSW24"} {
		g, err := kwg.GetKWG(cfg.WGLConfig(), name)
		if err != nil {
			return fmt.Errorf("failed to load lexicon %s: %v", name, err)
		}
		lexica[name] = g
		fmt.Printf("✓ Loaded lexicon %s\n", name)
	}
	// Alphabet is lexicon-independent (see the lexica var's own comment) -
	// any loaded KWG's GetAlphabet() gives the same result, so this just
	// picks the default rather than needing its own per-lexicon slot.
	alph = lexica[defaultLexicon].GetAlphabet()

	var err error
	ld, err = tilemapping.EnglishLetterDistribution(cfg.WGLConfig())
	if err != nil {
		return fmt.Errorf("failed to load letter distribution: %v", err)
	}
	fmt.Println("✓ Loaded letter distribution")
	return nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "healthy",
		"service": "macondo-movegen",
	})
}

// isBingoWord mirrors moveHistoryFunctions.js's isBingoMove exactly - a
// bingo is defined by how many NEW (rack) tiles a play uses, not the
// resulting word's length, so a play that hooks through an existing letter
// (e.g. an 8-letter word made from 7 new tiles) still counts.
func isBingoWord(m Move) bool {
	if m.IsExchange {
		return false
	}
	newTiles := 0
	for _, t := range m.Tiles {
		if t.IsNew {
			newTiles++
		}
	}
	return newTiles == 7
}

func combinedMoveValue(m Move) float64 {
	return float64(m.Score) + m.LeaveValue
}

// decideGeneratePauseReason ports puzzleFunctions.js's decidePauseReason
// exactly (same four pauseMode values, same >=10 significant-gap
// threshold), moved server-side so Puzzle Mode's "should we pause here"
// decision happens in the SAME request/response round-trip as the
// candidate list itself, rather than as a separate step the frontend used
// to perform afterward against its own local state. moves must already be
// sorted best-first (generateMovesHandler's own ranked/responseMoves
// already are) - moves[0] is trusted as "the best move" without
// re-sorting, same convention the original frontend version relied on.
func decideGeneratePauseReason(moves []Move, pauseMode string) string {
	if pauseMode == "" || len(moves) == 0 {
		return ""
	}
	best := moves[0]
	if best.IsExchange {
		return ""
	}

	isBestBingo := isBingoWord(best)
	otherBingo := false
	for _, m := range moves[1:] {
		if isBingoWord(m) {
			otherBingo = true
			break
		}
	}
	hasSignificantGap := len(moves) > 1 && (combinedMoveValue(best)-combinedMoveValue(moves[1])) >= 10

	switch {
	case pauseMode == "bingo" && isBestBingo:
		return "bingo"
	case pauseMode == "only-bingo" && isBestBingo && !otherBingo:
		return "only-bingo"
	case pauseMode == "significant-best" && hasSignificantGap:
		return "significant-best"
	case pauseMode == "non-bingo-significant" && hasSignificantGap && !isBestBingo:
		return "non-bingo-significant"
	}
	return ""
}

func generateMovesHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req GenerateMovesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Rack == "" {
		http.Error(w, "Rack is required", http.StatusBadRequest)
		return
	}
	if len(req.Board) != 15 {
		http.Error(w, "Board must have 15 rows", http.StatusBadRequest)
		return
	}
	for i := range req.Board {
		if len(req.Board[i]) != 15 {
			http.Error(w, "Each board row must have 15 columns", http.StatusBadRequest)
			return
		}
	}
	if req.TopN <= 0 {
		req.TopN = 10
	}

	// Shadows the removed package-level gd - every gd reference below this
	// point in the function resolves to this request-scoped local instead,
	// so nothing past this line needed to change.
	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Create and initialize the board with custom premium squares if provided
	boardLayout := createCustomBoardLayout(req.PremiumSquares)
	bd := board.MakeBoard(boardLayout)

	// Set letters on the board
	tilesPlayed := 0
	for row := 0; row < 15; row++ {
		for col := 0; col < 15; col++ {
			tile := req.Board[row][col]
			if tile != "" {
				if ml, err := alph.Val(tile); err == nil {
					bd.SetLetter(row, col, ml)
					tilesPlayed++
				}
			}
		}
	}

	// Manually set the tiles played count since SetLetter doesn't do this
	bd.TestSetTilesPlayed(tilesPlayed)

	// Generate cross-sets and update anchors
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()
	
	rack := tilemapping.RackFromString(req.Rack, alph)
	generator := movegen.NewGordonGenerator(gd, bd, ld)
	moves := generator.GenAll(rack, false)
	
	fmt.Printf("Generated %d moves for rack '%s'\n", len(moves), req.Rack)

	// rankedMove is a lightweight local scoring wrapper - unlike simulate.go's
	// scoredCandidate, this never needs to actually play a move on a board, so
	// it doesn't carry Move/DetailedMove pointers.
	type rankedMove struct {
		position, word, leave, exchangeTiles string
		score                                int
		leaveValue, total                    float64
		isExchange                           bool
		detailed                             *DetailedMove
	}

	ranked := make([]rankedMove, 0, len(moves))
	for _, m := range moves {
		moveStr := m.String()
		if !strings.Contains(moveStr, "play word:") {
			continue
		}

		// Extract word from move string
		// Format: "<action: play word: POSITION WORD score: SCORE tp: TILES_PLAYED leave: LEAVE>"
		word := ""
		parts := strings.Split(moveStr, "play word:")
		if len(parts) > 1 {
			wordPart := strings.TrimSpace(parts[1])
			// Split by spaces and find the word (skip position)
			wordFields := strings.Fields(wordPart)
			for _, field := range wordFields {
				// Skip position-like strings (like "8D") and score info
				if len(field) >= 2 && !strings.ContainsAny(field, "0123456789") &&
					!strings.HasPrefix(field, "score:") &&
					!strings.HasPrefix(field, "tp:") &&
					!strings.HasPrefix(field, "leave:") {
					// Found the word, but check if it's not just dots
					if !strings.HasPrefix(field, ".....") {
						word = field
					}
					break
				}
			}
		}

		// Recompute the leave from the actual tiles used rather than trusting
		// Move.Leave().UserVisible's blank/casing convention - same rationale
		// as simulate.go's simulateSeriesHandler.
		detailed := toDetailedMove(m, bd, alph)
		var used []string
		for _, t := range detailed.Tiles {
			if t.IsNew {
				if t.IsBlank {
					used = append(used, "?")
				} else {
					used = append(used, t.Letter)
				}
			}
		}
		leave := sortLeaveString(removeRackFromPool(req.Rack, strings.Join(used, "")))
		leaveValue := getLeaveValue(leave)

		ranked = append(ranked, rankedMove{
			position: m.BoardCoords(), word: word, score: m.Score(),
			leave: leave, leaveValue: leaveValue, total: float64(m.Score()) + leaveValue,
			detailed: detailed,
		})
	}

	if req.PoolSize >= 7 {
		for _, ex := range allExchangeCandidates(req.Rack) {
			ranked = append(ranked, rankedMove{
				isExchange: true, exchangeTiles: ex.exchangeTiles,
				leave: ex.leave, leaveValue: ex.total, total: ex.total,
			})
		}
	}

	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].total > ranked[j].total })
	if len(ranked) > req.TopN {
		ranked = ranked[:req.TopN]
	}

	responseMoves := make([]Move, 0, len(ranked))
	for _, rm := range ranked {
		if rm.isExchange {
			responseMoves = append(responseMoves, Move{
				Position: "Exchange", Word: "Exchange " + rm.exchangeTiles, Score: 0,
				Leave: rm.leave, LeaveValue: rm.leaveValue, TotalValue: rm.total,
				IsExchange: true, Direction: "exchange", StartPosition: "Exchange",
				Tiles: exchangeTilesToMoveTiles(rm.exchangeTiles),
			})
			continue
		}
		responseMoves = append(responseMoves, Move{
			Position: rm.position, Word: rm.word, Score: rm.score,
			Leave: rm.leave, LeaveValue: rm.leaveValue, TotalValue: rm.total,
			Direction: rm.detailed.Direction, StartPosition: rm.detailed.StartPosition,
			Tiles: rm.detailed.Tiles,
		})
	}
	resp := GenerateMovesResponse{
		Moves:       responseMoves,
		Total:       len(moves),
		PauseReason: decideGeneratePauseReason(responseMoves, req.PauseMode),
		Lexicon:     lexiconName,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func validateWordHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req ValidateWordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Word == "" {
		http.Error(w, "Word is required", http.StatusBadRequest)
		return
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Convert word to uppercase for consistency with lexicon
	word := strings.ToUpper(strings.TrimSpace(req.Word))

	// Check if word exists in the loaded lexicon using existing logic
	// We'll use the move generator to validate - if we can generate moves for this word, it's valid
	var isValid bool

	// Create an empty board
	bd := board.MakeBoard(board.CrosswordGameBoard)

	// Try to create a rack with the letters from the word
	// This will fail if the word contains invalid characters
	rack := tilemapping.RackFromString(word, alph)
	if rack == nil {
		isValid = false
	} else {
		// Generate cross-sets for the empty board
		cross_set.GenAllCrossSets(bd, gd, ld)
		bd.UpdateAllAnchors()
		
		// Try to generate moves - if any moves are generated, the word is valid
		generator := movegen.NewGordonGenerator(gd, bd, ld)
		moves := generator.GenAll(rack, false)
		
		// Check if any of the generated moves contain our word as a playable word
		for _, m := range moves {
			moveStr := m.String()
			// Only consider moves that are actual word plays, not passes
			if strings.Contains(moveStr, "play word:") {
				// Extract the word from the move string using the same logic as other handlers
				if strings.Contains(moveStr, "play word:") {
					parts := strings.Split(moveStr, "play word:")
					if len(parts) > 1 {
						wordPart := strings.TrimSpace(parts[1])
						wordFields := strings.Fields(wordPart)
						for _, field := range wordFields {
							if len(field) >= 2 && !strings.ContainsAny(field, "0123456789") && 
							   !strings.HasPrefix(field, "score:") && 
							   !strings.HasPrefix(field, "tp:") && 
							   !strings.HasPrefix(field, "leave:") {
								if !strings.HasPrefix(field, ".....") {
									if field == word {
										isValid = true
										break
									}
									break
								}
								break
							}
						}
					}
				}
				if isValid {
					break
				}
			}
		}
	}
	
	response := ValidateWordResponse{
		Word:    word,
		IsValid: isValid,
		Lexicon: lexiconName,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func validateWordsHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req ValidateWordsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if len(req.Words) == 0 {
		http.Error(w, "Words array is required", http.StatusBadRequest)
		return
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Validate each word using the same logic as validateWordHandler
	validations := make([]WordValidation, 0, len(req.Words))
	validCount := 0
	invalidCount := 0
	
	for _, word := range req.Words {
		// Convert word to uppercase for consistency with lexicon
		word = strings.ToUpper(strings.TrimSpace(word))
		
		// Check if word exists in the loaded lexicon using the same logic as validateWordHandler
		var isValid bool
		
		// Create an empty board
		bd := board.MakeBoard(board.CrosswordGameBoard)
		
		// Try to create a rack with the letters from the word
		rack := tilemapping.RackFromString(word, alph)
		if rack != nil {
			// Generate cross-sets for the empty board
			cross_set.GenAllCrossSets(bd, gd, ld)
			bd.UpdateAllAnchors()
			
			// Try to generate moves - if any moves are generated, the word is valid
			generator := movegen.NewGordonGenerator(gd, bd, ld)
			moves := generator.GenAll(rack, false)
			
			// Check if any of the generated moves contain our word as a playable word
			for _, m := range moves {
				moveStr := m.String()
				// Only consider moves that are actual word plays, not passes
				if strings.Contains(moveStr, "play word:") {
					// Extract the word from the move string using the same logic as other handlers
					if strings.Contains(moveStr, "play word:") {
						parts := strings.Split(moveStr, "play word:")
						if len(parts) > 1 {
							wordPart := strings.TrimSpace(parts[1])
							wordFields := strings.Fields(wordPart)
							for _, field := range wordFields {
								if len(field) >= 2 && !strings.ContainsAny(field, "0123456789") && 
								   !strings.HasPrefix(field, "score:") && 
								   !strings.HasPrefix(field, "tp:") && 
								   !strings.HasPrefix(field, "leave:") {
									if !strings.HasPrefix(field, ".....") {
										if field == word {
											isValid = true
											break
										}
										break
									}
									break
								}
							}
						}
					}
					if isValid {
						break
					}
				}
			}
		}
		
		validations = append(validations, WordValidation{
			Word:    word,
			IsValid: isValid,
		})
		
		if isValid {
			validCount++
		} else {
			invalidCount++
		}
	}
	
	response := ValidateWordsResponse{
		Words:   validations,
		Count:   len(req.Words),
		Valid:   validCount,
		Invalid: invalidCount,
		Lexicon: lexiconName,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func findSubanagramsHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req SubanagramSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Letters == "" {
		http.Error(w, "Letters are required", http.StatusBadRequest)
		return
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Convert letters to uppercase and remove spaces
	letters := strings.ToUpper(strings.ReplaceAll(req.Letters, " ", ""))

	// Create an empty board
	bd := board.MakeBoard(board.CrosswordGameBoard)

	// Try to create a rack with the letters
	rack := tilemapping.RackFromString(letters, alph)
	if rack == nil {
		http.Error(w, "Invalid letters provided", http.StatusBadRequest)
		return
	}

	// Generate cross-sets for the empty board
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	// Generate all possible moves
	generator := movegen.NewGordonGenerator(gd, bd, ld)
	moves := generator.GenAll(rack, false)

	// Extract unique words from the moves
	subanagrams := make(map[string]bool)
	for _, m := range moves {
		moveStr := m.String()
		// Parse move string to extract word (same logic as generateMovesHandler)
		if strings.Contains(moveStr, "play word:") {
			parts := strings.Split(moveStr, "play word:")
			if len(parts) > 1 {
				wordPart := strings.TrimSpace(parts[1])
				wordFields := strings.Fields(wordPart)
				for _, field := range wordFields {
					if len(field) >= 2 && !strings.ContainsAny(field, "0123456789") && 
					   !strings.HasPrefix(field, "score:") && 
					   !strings.HasPrefix(field, "tp:") && 
					   !strings.HasPrefix(field, "leave:") {
						if !strings.HasPrefix(field, ".....") {
							subanagrams[field] = true
							break
						}
						break
					}
				}
			}
		}
	}
	
	// Convert map keys to slice and sort
	subanagramList := make([]string, 0, len(subanagrams))
	for word := range subanagrams {
		subanagramList = append(subanagramList, word)
	}
	sort.Strings(subanagramList)
	
	response := SubanagramSearchResponse{
		Letters:     letters,
		Subanagrams: subanagramList,
		Count:       len(subanagramList),
		Lexicon:     lexiconName,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func findAnagramsHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	var req AnagramSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Letters == "" {
		http.Error(w, "Letters are required", http.StatusBadRequest)
		return
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Convert letters to uppercase and remove spaces
	letters := strings.ToUpper(strings.ReplaceAll(req.Letters, " ", ""))
	inputLength := len(letters)

	// Create an empty board
	bd := board.MakeBoard(board.CrosswordGameBoard)

	// Try to create a rack with the letters
	rack := tilemapping.RackFromString(letters, alph)
	if rack == nil {
		http.Error(w, "Invalid letters provided", http.StatusBadRequest)
		return
	}

	// Generate cross-sets for the empty board
	cross_set.GenAllCrossSets(bd, gd, ld)
	bd.UpdateAllAnchors()

	// Generate all possible moves
	generator := movegen.NewGordonGenerator(gd, bd, ld)
	moves := generator.GenAll(rack, false)

	// Extract unique words from the moves that are the exact same length
	anagrams := make(map[string]bool)
	for _, m := range moves {
		moveStr := m.String()
		// Parse move string to extract word (same logic as generateMovesHandler)
		if strings.Contains(moveStr, "play word:") {
			parts := strings.Split(moveStr, "play word:")
			if len(parts) > 1 {
				wordPart := strings.TrimSpace(parts[1])
				wordFields := strings.Fields(wordPart)
				for _, field := range wordFields {
					if len(field) >= 2 && !strings.ContainsAny(field, "0123456789") && 
					   !strings.HasPrefix(field, "score:") && 
					   !strings.HasPrefix(field, "tp:") && 
					   !strings.HasPrefix(field, "leave:") {
						if !strings.HasPrefix(field, ".....") {
							// Only include words of the exact same length
							if len(field) == inputLength {
								anagrams[field] = true
							}
							break
						}
						break
					}
				}
			}
		}
	}
	
	// Convert map keys to slice and sort
	anagramList := make([]string, 0, len(anagrams))
	for word := range anagrams {
		anagramList = append(anagramList, word)
	}
	sort.Strings(anagramList)
	
	response := AnagramSearchResponse{
		Letters:  letters,
		Anagrams: anagramList,
		Count:    len(anagramList),
		Lexicon:  lexiconName,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// pickBestCandidate scans rawMoves for word plays and, when poolForExchange
// has >= 7 tiles, also considers exchanging (via allExchangeCandidates),
// returning whichever single option scores highest by score+leaveValue -
// same candidate-list-then-sort approach as simulateOneGame's per-turn
// logic (and reuses its scoredCandidate type directly), just narrowed to
// only the winner since bulk-move-gen only ever needs rank 1. Leave values
// are recomputed from toDetailedMove's tiles rather than trusting
// Move.Leave().UserVisible's blank/casing convention - same rationale as
// simulate.go's header comment and generateMovesHandler's word-play
// scoring. poolForExchange must already exclude currentRack (the bag
// remaining, not counting the rack being chosen for) - same semantics as
// simulateOneGame's own pool variable at its canExchange check. Returns nil
// if rawMoves has no word plays and no exchange is available.
func pickBestCandidate(rawMoves []*macondomove.Move, bd *board.GameBoard, alph *tilemapping.TileMapping, currentRack string, poolForExchange string) *scoredCandidate {
	var best *scoredCandidate

	for _, m := range rawMoves {
		if !strings.Contains(m.String(), "play word:") {
			continue
		}
		detailed := toDetailedMove(m, bd, alph)

		var used []string
		for _, t := range detailed.Tiles {
			if t.IsNew {
				if t.IsBlank {
					used = append(used, "?")
				} else {
					used = append(used, t.Letter)
				}
			}
		}
		leave := sortLeaveString(removeRackFromPool(currentRack, strings.Join(used, "")))
		total := float64(detailed.Score) + getLeaveValue(leave)

		if best == nil || total > best.total {
			best = &scoredCandidate{move: m, detailed: detailed, leave: leave, total: total}
		}
	}

	if len(poolForExchange) >= 7 {
		for _, ex := range allExchangeCandidates(currentRack) {
			exCopy := ex
			if best == nil || exCopy.total > best.total {
				best = &exCopy
			}
		}
	}

	return best
}

// detailedMoveForExchange builds the client-facing DetailedMove shape for a
// chosen exchange candidate - mirrors generateMovesHandler's exchange Move
// construction (word/direction/startPosition conventions, tiles via
// exchangeTilesToMoveTiles) so exchange entries look the same whether they
// came from /generate-moves or /bulk-move-gen.
func detailedMoveForExchange(exchangeTiles string) *DetailedMove {
	return &DetailedMove{
		Word:          "Exchange " + exchangeTiles,
		Score:         0,
		Direction:     "exchange",
		StartPosition: "Exchange",
		Tiles:         exchangeTilesToMoveTiles(exchangeTiles),
		IsExchange:    true,
	}
}

func bulkMoveGenHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req BulkMoveGenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.TilePool == "" {
		http.Error(w, "TilePool is required", http.StatusBadRequest)
		return
	}

	if len(req.Board) != 15 {
		http.Error(w, "Board must have 15 rows", http.StatusBadRequest)
		return
	}
	for i := range req.Board {
		if len(req.Board[i]) != 15 {
			http.Error(w, "Each board row must have 15 columns", http.StatusBadRequest)
			return
		}
	}

	if req.Iterations <= 0 {
		req.Iterations = 1000
	}

	gd, lexiconName, err := resolveLexicon(req.Lexicon)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Convert tile pool to uppercase and remove spaces
	tilePool := strings.ToUpper(strings.ReplaceAll(req.TilePool, " ", ""))
	ourLeave := strings.ToUpper(strings.ReplaceAll(req.OurLeave, " ", ""))
	// Preserve blank encoding: ToUpper leaves '?' alone.

	// Create and initialize the board with custom premium squares if provided
	boardLayout := createCustomBoardLayout(req.PremiumSquares)
	baseBd := board.MakeBoard(boardLayout)

	// Set letters on the board
	tilesPlayed := 0
	for row := 0; row < 15; row++ {
		for col := 0; col < 15; col++ {
			tile := req.Board[row][col]
			if tile != "" {
				if ml, err := alph.Val(tile); err == nil {
					baseBd.SetLetter(row, col, ml)
					tilesPlayed++
				}
			}
		}
	}

	// Manually set the tiles played count since SetLetter doesn't do this
	baseBd.TestSetTilesPlayed(tilesPlayed)

	// Generate cross-sets for the board
	cross_set.GenAllCrossSets(baseBd, gd, ld)
	baseBd.UpdateAllAnchors()

	// Initialize random seed
	rand.Seed(time.Now().UnixNano())

	totalScore := 0
	totalBingos := 0
	var iterationDetails []BulkIterationDetail

	fmt.Printf("Starting bulk move generation with %d iterations (includeMoveDetails=%v)...\n",
		req.Iterations, req.IncludeMoveDetails)

	if req.IncludeMoveDetails {
		// Two-ply path: opponent's best reply on the given board, then our best reply after.
		// Aggregates are taken from our reply. Details are returned for both plays.
		iterationDetails = make([]BulkIterationDetail, 0, req.Iterations)

		for i := 0; i < req.Iterations; i++ {
			var opponentDetail *DetailedMove
			var ourDetail *DetailedMove

			opponentRackStr, opponentRack := generateRandomRack(tilePool, 7, alph)
			if opponentRack == nil {
				iterationDetails = append(iterationDetails, BulkIterationDetail{})
				continue
			}

			// Bag remaining excluding the opponent's own rack - same
			// semantics simulateOneGame uses for its canExchange check, and
			// what's actually left over for our own rack draw either way.
			remainingPool := removeRackFromPool(tilePool, opponentRackStr)

			opponentGen := movegen.NewGordonGenerator(gd, baseBd, ld)
			opponentRawMoves := opponentGen.GenAll(opponentRack, false)
			opponentChosen := pickBestCandidate(opponentRawMoves, baseBd, alph, opponentRackStr, remainingPool)
			if opponentChosen == nil {
				iterationDetails = append(iterationDetails, BulkIterationDetail{})
				continue
			}

			iterBd := baseBd.Copy()
			if opponentChosen.isExchange {
				opponentDetail = detailedMoveForExchange(opponentChosen.exchangeTiles)
				// No board mutation - exchanging doesn't touch the board.
			} else {
				opponentDetail = opponentChosen.detailed
				iterBd.PlayMove(opponentChosen.move)
				cross_set.UpdateCrossSetsForMove(iterBd, opponentChosen.move, gd, ld)
			}

			var ourRackStr string
			var ourRack *tilemapping.Rack
			if ourLeave != "" {
				ourRackStr, ourRack = generateRackWithFixedLeave(remainingPool, ourLeave, 7, alph)
			} else {
				ourRackStr, ourRack = generateRandomRack(remainingPool, 7, alph)
			}
			if ourRack == nil {
				iterationDetails = append(iterationDetails, BulkIterationDetail{
					OpponentMove: opponentDetail,
				})
				continue
			}

			// Bag remaining excluding both racks - our own canExchange check.
			poolAfterBoth := removeRackFromPool(remainingPool, ourRackStr)

			ourGen := movegen.NewGordonGenerator(gd, iterBd, ld)
			ourRawMoves := ourGen.GenAll(ourRack, false)
			ourChosen := pickBestCandidate(ourRawMoves, iterBd, alph, ourRackStr, poolAfterBoth)
			if ourChosen != nil {
				if ourChosen.isExchange {
					ourDetail = detailedMoveForExchange(ourChosen.exchangeTiles)
					// Exchanges score 0 and aren't a bingo - nothing to add.
				} else {
					totalScore += ourChosen.detailed.Score
					if ourChosen.move.BingoPlayed() {
						totalBingos++
					}
					ourDetail = ourChosen.detailed
				}
			}

			iterationDetails = append(iterationDetails, BulkIterationDetail{
				OpponentMove: opponentDetail,
				OurReply:     ourDetail,
			})

			if (i+1)%100 == 0 {
				fmt.Printf("Completed %d/%d iterations...\n", i+1, req.Iterations)
			}
		}
	} else {
		// Aggregate-only path: preserve the existing single-rack top-move behavior
		// so callers requesting thousands of iterations are unaffected.
		for i := 0; i < req.Iterations; i++ {
			rackStr, rack := generateRandomRack(tilePool, 7, alph)
			if rack == nil {
				continue
			}

			// Bag remaining excluding this rack - same canExchange semantics
			// as the two-ply path and simulateOneGame.
			poolForExchange := removeRackFromPool(tilePool, rackStr)

			generator := movegen.NewGordonGenerator(gd, baseBd, ld)
			rawMoves := generator.GenAll(rack, false)
			chosen := pickBestCandidate(rawMoves, baseBd, alph, rackStr, poolForExchange)

			if chosen != nil && !chosen.isExchange {
				totalScore += chosen.detailed.Score // raw points - the selection criterion changed, the aggregate metric didn't
				if len(strings.TrimSpace(chosen.leave)) <= 1 {
					totalBingos++
				}
			}
			// Exchanges score 0 and aren't a bingo - nothing to add for that case.

			if (i+1)%100 == 0 {
				fmt.Printf("Completed %d/%d iterations...\n", i+1, req.Iterations)
			}
		}
	}

	averageScore := float64(totalScore) / float64(req.Iterations)
	bingoPercent := float64(totalBingos) / float64(req.Iterations) * 100.0

	response := BulkMoveGenResponse{
		Iterations:       req.Iterations,
		AverageScore:     averageScore,
		BingoPercent:     bingoPercent,
		TotalBingos:      totalBingos,
		TotalScore:       totalScore,
		Lexicon:          lexiconName,
		IterationDetails: iterationDetails,
	}

	fmt.Printf("Bulk move generation complete. Average score: %.2f, Bingo rate: %.2f%%\n",
		averageScore, bingoPercent)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// toDetailedMove converts a Macondo move into the client-facing renderable shape.
func toDetailedMove(m *macondomove.Move, bd *board.GameBoard, alph *tilemapping.TileMapping) *DetailedMove {
	row, col, vertical := m.CoordsAndVertical()
	direction := "right"
	if vertical {
		direction = "down"
	}

	tiles := m.Tiles()
	moveTiles := make([]MoveTile, 0, len(tiles))
	var word strings.Builder

	for idx, tile := range tiles {
		r, c := row, col
		if vertical {
			r = row + idx
		} else {
			c = col + idx
		}

		isNew := tile != 0
		var ml tilemapping.MachineLetter
		if isNew {
			ml = tile
		} else {
			ml = bd.GetLetter(r, c)
		}

		isBlank := ml.IsBlanked()
		letter := ""
		if ml != 0 {
			letter = strings.ToUpper(alph.Letter(ml.Unblank()))
		}

		word.WriteString(letter)
		moveTiles = append(moveTiles, MoveTile{
			Row:     r,
			Col:     c,
			Letter:  letter,
			IsNew:   isNew,
			IsBlank: isBlank,
		})
	}

	return &DetailedMove{
		Word:          word.String(),
		Score:         m.Score(),
		Direction:     direction,
		StartPosition: m.BoardCoords(),
		Tiles:         moveTiles,
	}
}

// exchangeTilesToMoveTiles converts an exchange candidate's tile string (e.g.
// "AB?") into the MoveTile shape clients already know how to render, so an
// exchange entry needs no special-case reconstruction downstream.
func exchangeTilesToMoveTiles(s string) []MoveTile {
	runes := []rune(s)
	tiles := make([]MoveTile, 0, len(runes))
	for _, r := range runes {
		tiles = append(tiles, MoveTile{Letter: string(r), IsNew: false, IsBlank: r == '?'})
	}
	return tiles
}

// removeRackFromPool removes one occurrence of each rack letter from the pool.
func removeRackFromPool(pool, rack string) string {
	counts := make(map[rune]int, len(rack))
	for _, r := range rack {
		counts[r]++
	}

	var remaining strings.Builder
	remaining.Grow(len(pool))
	for _, r := range pool {
		if counts[r] > 0 {
			counts[r]--
			continue
		}
		remaining.WriteRune(r)
	}
	return remaining.String()
}

// generateRackWithFixedLeave builds a rack that always includes fixedLeave, then
// fills with random tiles from pool up to targetSize (or fewer if the pool is short).
// Returns the plain rack string alongside the *tilemapping.Rack since leave-value
// scoring (removeRackFromPool) needs the string, not the tilemapping type.
func generateRackWithFixedLeave(pool, fixedLeave string, targetSize int, alph *tilemapping.TileMapping) (string, *tilemapping.Rack) {
	if fixedLeave == "" {
		return generateRandomRack(pool, targetSize, alph)
	}

	leaveRunes := []rune(fixedLeave)
	if len(leaveRunes) >= targetSize {
		s := string(leaveRunes[:targetSize])
		return s, tilemapping.RackFromString(s, alph)
	}

	need := targetSize - len(leaveRunes)
	fill := drawRandomTiles(pool, need)
	s := fixedLeave + fill
	return s, tilemapping.RackFromString(s, alph)
}

// drawRandomTiles returns up to n random tiles from pool (fewer if pool is shorter).
func drawRandomTiles(pool string, n int) string {
	if n <= 0 || pool == "" {
		return ""
	}

	tiles := []rune(pool)
	if len(tiles) <= n {
		return string(tiles)
	}

	shuffled := make([]rune, len(tiles))
	copy(shuffled, tiles)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return string(shuffled[:n])
}

// generateRandomRack creates a random rack of specified size from the given tile pool.
// Returns the plain rack string alongside the *tilemapping.Rack (see generateRackWithFixedLeave).
func generateRandomRack(tilePool string, size int, alph *tilemapping.TileMapping) (string, *tilemapping.Rack) {
	fill := drawRandomTiles(tilePool, size)
	if len([]rune(fill)) < size {
		return "", nil // Not enough tiles in pool
	}
	return fill, tilemapping.RackFromString(fill, alph)
}