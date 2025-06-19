package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Move represents a possible move
type Move struct {
	Word       string   `json:"word"`
	Score      int      `json:"score"`
	Tiles      []Tile   `json:"tiles"`
	Direction  string   `json:"direction"`
	StartRow   int      `json:"startRow"`
	StartCol   int      `json:"startCol"`
	TotalValue float64  `json:"totalValue"`
}

// Tile represents a tile placement
type Tile struct {
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	Letter  string `json:"letter"`
	IsNew   bool   `json:"isNew"`
	IsBlank bool   `json:"isBlank"`
}

// Request represents the incoming request
type Request struct {
	Board   [][]interface{} `json:"board"`
	Letters []string        `json:"letters"`
	Pool    []string        `json:"pool"`
}

// Response represents the response
type Response struct {
	Moves []Move `json:"moves"`
}

// GADDAGNode represents a node in the GADDAG
type GADDAGNode struct {
	Children   map[string]*GADDAGNode `json:"children"`
	IsTerminal bool                   `json:"isTerminal"`
}

// Letter scores for Scrabble
var letterScores = map[string]int{
	"A": 1, "B": 3, "C": 3, "D": 2, "E": 1, "F": 4, "G": 2, "H": 4, "I": 1, "J": 8, "K": 5,
	"L": 1, "M": 3, "N": 1, "O": 1, "P": 3, "Q": 10, "R": 1, "S": 1, "T": 1, "U": 1, "V": 4,
	"W": 4, "X": 8, "Y": 4, "Z": 10,
}

// Board multipliers
var wordMultipliers = [][]int{
	{3, 1, 1, 1, 1, 1, 1, 3, 1, 1, 1, 1, 1, 1, 3},
	{1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 1},
	{1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1},
	{1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1},
	{1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 2, 1, 1, 1, 1},
	{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
	{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
	{3, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 3},
	{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
	{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
	{1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 2, 1, 1, 1, 1},
	{1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1},
	{1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1},
	{1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 1},
	{3, 1, 1, 1, 1, 1, 1, 3, 1, 1, 1, 1, 1, 1, 3},
}

var letterMultipliers = [][]int{
	{1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1},
	{1, 1, 1, 1, 1, 3, 1, 1, 1, 3, 1, 1, 1, 1, 1},
	{1, 1, 1, 1, 1, 1, 2, 1, 2, 1, 1, 1, 1, 1, 1},
	{2, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 2},
	{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
	{1, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 3, 1},
	{1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1},
	{1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1},
	{1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1},
	{1, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 3, 1},
	{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
	{2, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 2},
	{1, 1, 1, 1, 1, 1, 2, 1, 2, 1, 1, 1, 1, 1, 1},
	{1, 1, 1, 1, 1, 3, 1, 1, 1, 3, 1, 1, 1, 1, 1},
	{1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, 1, 1},
}

var gaddag *GADDAGNode
var wordCache = make(map[string]bool)
var crossChecks = make(map[string]map[string]bool)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	// Load GADDAG
	loadGADDAG()

	http.HandleFunc("/generate-moves", handleGenerateMoves)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/", handleRoot)

	fmt.Printf("🚀 Scrabble Move Generator starting on port %s\n", port)
	fmt.Println("⚡ Fast Go-based move generation for Scrabble!")
	fmt.Println("📍 Endpoints:")
	fmt.Println("   - POST /generate-moves - Generate moves")
	fmt.Println("   - GET  /health - Health check")
	fmt.Println("   - GET  / - Root endpoint")

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func loadGADDAG() {
	// Try to load from file first
	data, err := ioutil.ReadFile("gaddag.json")
	if err != nil {
		fmt.Println("⚠️ No gaddag.json found, creating basic GADDAG...")
		createBasicGADDAG()
		return
	}

	err = json.Unmarshal(data, &gaddag)
	if err != nil {
		fmt.Printf("❌ Error parsing GADDAG: %v\n", err)
		createBasicGADDAG()
		return
	}

	// Build word cache and cross-checks
	buildWordCache()
	buildCrossChecks()
	
	fmt.Println("✅ GADDAG loaded successfully from file")
}

func createBasicGADDAG() {
	gaddag = &GADDAGNode{
		Children: make(map[string]*GADDAGNode),
	}
	
	// Add common words for testing
	words := []string{
		"HELLO", "WORLD", "GAME", "PLAY", "WORD", "TILE", "GO", "AT", "IT", "IS", "BE", "TO", "OF", "IN", "ON", 
		"AN", "AS", "OR", "IF", "SO", "UP", "WE", "ME", "HE", "SHE", "THE", "AND", "FOR", "ARE", "BUT", "NOT", 
		"YOU", "ALL", "CAN", "HAD", "HER", "WAS", "ONE", "OUR", "OUT", "DAY", "GET", "HAS", "HIM", "HIS", "HOW", 
		"MAN", "NEW", "NOW", "OLD", "SEE", "TWO", "WAY", "WHO", "BOY", "DID", "ITS", "LET", "PUT", "SAY", "TOO", "USE",
		"SCRABBLE", "LETTER", "SCORE", "BOARD", "RACK", "MOVE", "TURN", "PLAYER", "GAME", "WIN", "LOSE", "DRAW",
		"QUICK", "FAST", "SLOW", "BIG", "SMALL", "GOOD", "BAD", "HOT", "COLD", "WARM", "COOL", "LIGHT", "DARK",
		"EASY", "HARD", "SIMPLE", "COMPLEX", "CLEAR", "FOGGY", "BRIGHT", "DIM", "LOUD", "QUIET", "SOFT", "HARD",
		"SWEET", "SOUR", "SALTY", "BITTER", "FRESH", "STALE", "NEW", "OLD", "YOUNG", "ANCIENT", "MODERN", "CLASSIC",
		"TRADITIONAL", "INNOVATIVE", "CREATIVE", "ORIGINAL", "UNIQUE", "SPECIAL", "COMMON", "RARE", "PRECIOUS",
		"VALUABLE", "EXPENSIVE", "CHEAP", "AFFORDABLE", "LUXURIOUS", "ECONOMICAL", "EFFICIENT", "EFFECTIVE",
		"POWERFUL", "STRONG", "WEAK", "HEALTHY", "SICK", "HAPPY", "SAD", "ANGRY", "CALM", "EXCITED", "BORED",
		"INTERESTING", "FASCINATING", "AMAZING", "WONDERFUL", "BEAUTIFUL", "UGLY", "PRETTY", "HANDSOME", "CUTE",
		"ADORABLE", "LOVELY", "CHARMING", "ATTRACTIVE", "ELEGANT", "GRACEFUL", "CLUMSY", "AWKWARD", "SMOOTH",
		"ROUGH", "SILKY", "COARSE", "FINE", "THICK", "THIN", "WIDE", "NARROW", "LONG", "SHORT", "TALL", "SMALL",
		"LARGE", "HUGE", "TINY", "MASSIVE", "ENORMOUS", "GIGANTIC", "MICROSCOPIC", "VISIBLE", "INVISIBLE",
		"TRANSPARENT", "OPAQUE", "SOLID", "LIQUID", "GAS", "FLUID", "RIGID", "FLEXIBLE", "STIFF", "LOOSE",
		"TIGHT", "FIT", "LOOSE", "TIGHT", "COMFORTABLE", "UNCOMFORTABLE", "CONVENIENT", "INCONVENIENT",
		"ACCESSIBLE", "INACCESSIBLE", "AVAILABLE", "UNAVAILABLE", "POSSIBLE", "IMPOSSIBLE", "PROBABLE",
		"IMPROBABLE", "LIKELY", "UNLIKELY", "CERTAIN", "UNCERTAIN", "SURE", "UNSURE", "CONFIDENT", "INSECURE",
		"BRAVE", "COWARDLY", "BOLD", "SHY", "OUTGOING", "INTROVERTED", "SOCIAL", "ANTISOCIAL", "FRIENDLY",
		"UNFRIENDLY", "KIND", "MEAN", "GENEROUS", "SELFISH", "HELPFUL", "HURTFUL", "CARING", "CARELESS",
		"CAREFUL", "RECKLESS", "CAUTIOUS", "BOLD", "TIMID", "AGGRESSIVE", "PASSIVE", "ACTIVE", "INACTIVE",
		"ENERGETIC", "TIRED", "SLEEPY", "AWAKE", "ALERT", "DROWSY", "FOCUSED", "DISTRACTED", "CONCENTRATED",
		"SCATTERED", "ORGANIZED", "DISORGANIZED", "NEAT", "MESSY", "CLEAN", "DIRTY", "TIDY", "UNTIDY",
		"ORDERLY", "CHAOTIC", "STRUCTURED", "UNSTRUCTURED", "SYSTEMATIC", "RANDOM", "LOGICAL", "ILLOGICAL",
		"REASONABLE", "UNREASONABLE", "SENSIBLE", "FOOLISH", "WISE", "STUPID", "INTELLIGENT", "DUMB",
		"SMART", "CLEVER", "SLOW", "QUICK", "FAST", "RAPID", "SPEEDY", "SWIFT", "SLOW", "GRADUAL", "SUDDEN",
		"INSTANT", "IMMEDIATE", "DELAYED", "LATE", "EARLY", "ON_TIME", "PUNCTUAL", "TARDY", "PROMPT",
		"RESPONSIVE", "RESPONSIBLE", "IRRESPONSIBLE", "RELIABLE", "UNRELIABLE", "TRUSTWORTHY", "DISHONEST",
		"HONEST", "TRUTHFUL", "LYING", "SINCERE", "INSINCERE", "GENUINE", "FAKE", "REAL", "ARTIFICIAL",
		"NATURAL", "MANMADE", "ORGANIC", "INORGANIC", "BIOLOGICAL", "CHEMICAL", "PHYSICAL", "MENTAL",
		"EMOTIONAL", "SPIRITUAL", "MATERIAL", "IMMATERIAL", "CONCRETE", "ABSTRACT", "SPECIFIC", "GENERAL",
		"DETAILED", "VAGUE", "PRECISE", "IMPRECISE", "ACCURATE", "INACCURATE", "CORRECT", "INCORRECT",
		"RIGHT", "WRONG", "TRUE", "FALSE", "VALID", "INVALID", "LEGAL", "ILLEGAL", "LAWFUL", "UNLAWFUL",
		"PERMITTED", "FORBIDDEN", "ALLOWED", "PROHIBITED", "ACCEPTABLE", "UNACCEPTABLE", "APPROPRIATE",
		"INAPPROPRIATE", "SUITABLE", "UNSUITABLE", "COMPATIBLE", "INCOMPATIBLE", "SIMILAR", "DIFFERENT",
		"ALIKE", "UNLIKE", "IDENTICAL", "UNIQUE", "SAME", "VARIOUS", "DIVERSE", "UNIFORM", "VARIED",
		"CONSISTENT", "INCONSISTENT", "STABLE", "UNSTABLE", "STEADY", "UNSTEADY", "BALANCED", "UNBALANCED",
		"SYMMETRICAL", "ASYMMETRICAL", "REGULAR", "IRREGULAR", "NORMAL", "ABNORMAL", "TYPICAL", "ATYPICAL",
		"STANDARD", "NONSTANDARD", "CONVENTIONAL", "UNCONVENTIONAL", "TRADITIONAL", "MODERN", "CLASSICAL",
		"CONTEMPORARY", "CURRENT", "OUTDATED", "OBSOLETE", "ARCHAIC", "ANCIENT", "PREHISTORIC", "FUTURISTIC",
		"ADVANCED", "PRIMITIVE", "SOPHISTICATED", "SIMPLE", "COMPLEX", "BASIC", "ADVANCED", "ELEMENTARY",
		"ADVANCED", "BEGINNER", "EXPERT", "NOVICE", "PROFESSIONAL", "AMATEUR", "SKILLED", "UNSKILLED",
		"EXPERIENCED", "INEXPERIENCED", "QUALIFIED", "UNQUALIFIED", "CERTIFIED", "UNCERTIFIED", "LICENSED",
		"UNLICENSED", "AUTHORIZED", "UNAUTHORIZED", "OFFICIAL", "UNOFFICIAL", "FORMAL", "INFORMAL",
		"CEREMONIAL", "CASUAL", "SERIOUS", "PLAYFUL", "HUMOROUS", "FUNNY", "SILLY", "GOOFY", "WITTY",
		"CLEVER", "AMUSING", "ENTERTAINING", "BORING", "DULL", "EXCITING", "THRILLING", "ADVENTUROUS",
		"DANGEROUS", "SAFE", "SECURE", "UNSAFE", "RISKY", "HAZARDOUS", "HARMLESS", "INNOCENT", "GUILTY",
		"BLAMELESS", "FAULTY", "PERFECT", "FLAWLESS", "DEFECTIVE", "DAMAGED", "BROKEN", "FIXED", "REPAIRED",
		"MAINTAINED", "SERVICED", "CLEANED", "WASHED", "DIRTY", "SOILED", "STAINED", "SPOTTED", "MARKED",
		"SCARRED", "DAMAGED", "HURT", "INJURED", "WOUNDED", "HEALED", "CURED", "TREATED", "MEDICATED",
		"HEALTHY", "SICK", "ILL", "DISEASED", "INFECTED", "CONTAGIOUS", "INFECTIOUS", "VIRAL", "BACTERIAL",
		"FUNGAL", "PARASITIC", "TOXIC", "POISONOUS", "VENOMOUS", "DEADLY", "LETHAL", "FATAL", "MORTAL",
		"IMMORTAL", "ETERNAL", "PERMANENT", "TEMPORARY", "BRIEF", "LENGTHY", "SHORT", "LONG", "ENDLESS",
		"FINITE", "LIMITED", "UNLIMITED", "BOUNDLESS", "RESTRICTED", "UNRESTRICTED", "FREE", "CAPTIVE",
		"LIBERATED", "ENSLAVED", "INDEPENDENT", "DEPENDENT", "AUTONOMOUS", "CONTROLLED", "GOVERNED",
		"RULED", "LEAD", "FOLLOWED", "GUIDED", "DIRECTED", "MANAGED", "ADMINISTERED", "SUPERVISED",
		"MONITORED", "OBSERVED", "WATCHED", "GUARDED", "PROTECTED", "DEFENDED", "ATTACKED", "INVADED",
		"CONQUERED", "DEFEATED", "VICTORIOUS", "TRIUMPHANT", "SUCCESSFUL", "UNSUCCESSFUL", "FAILED",
		"ACHIEVED", "ACCOMPLISHED", "COMPLETED", "FINISHED", "STARTED", "BEGUN", "INITIATED", "LAUNCHED",
		"INTRODUCED", "PRESENTED", "SHOWN", "DISPLAYED", "EXHIBITED", "DEMONSTRATED", "ILLUSTRATED",
		"EXPLAINED", "DESCRIBED", "DEFINED", "CLARIFIED", "SIMPLIFIED", "COMPLICATED", "CONFUSED",
		"PUZZLED", "PERPLEXED", "BAFFLED", "MYSTIFIED", "CONFOUNDED", "STUMPED", "STUMPED", "STUMPED",
	}
	
	for _, word := range words {
		addWordToGADDAG(word)
	}
	
	// Build word cache and cross-checks
	buildWordCache()
	buildCrossChecks()
	
	fmt.Printf("✅ Created basic GADDAG with %d words\n", len(words))
}

func addWordToGADDAG(word string) {
	// Add word in both directions (prefix and suffix)
	for i := 0; i <= len(word); i++ {
		prefix := word[:i]
		suffix := word[i:]
		
		// Add prefix direction
		current := gaddag
		for j := len(prefix) - 1; j >= 0; j-- {
			letter := string(prefix[j])
			if current.Children[letter] == nil {
				current.Children[letter] = &GADDAGNode{
					Children: make(map[string]*GADDAGNode),
				}
			}
			current = current.Children[letter]
		}
		
		// Add ^ transition
		if current.Children["^"] == nil {
			current.Children["^"] = &GADDAGNode{
				Children: make(map[string]*GADDAGNode),
			}
		}
		current = current.Children["^"]
		
		// Add suffix direction
		for _, letter := range suffix {
			letterStr := string(letter)
			if current.Children[letterStr] == nil {
				current.Children[letterStr] = &GADDAGNode{
					Children: make(map[string]*GADDAGNode),
				}
			}
			current = current.Children[letterStr]
		}
		current.IsTerminal = true
	}
}

func buildWordCache() {
	wordCache = make(map[string]bool)
	collectWords(gaddag, "", wordCache)
	fmt.Printf("📚 Word cache built with %d words\n", len(wordCache))
}

func collectWords(node *GADDAGNode, prefix string, cache map[string]bool) {
	if node.IsTerminal {
		cache[prefix] = true
	}
	
	for letter, child := range node.Children {
		if letter != "^" {
			collectWords(child, prefix+letter, cache)
		}
	}
}

func buildCrossChecks() {
	crossChecks = make(map[string]map[string]bool)
	// For now, allow all letters at all positions
	// In a full implementation, this would be more sophisticated
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"service":   "scrabble-move-generator",
		"status":    "running",
		"endpoints": "POST /generate-moves, GET /health",
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "healthy",
		"service": "scrabble-move-generator",
		"version": "1.0.0",
	})
}

func handleGenerateMoves(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fmt.Println("🚀 GO MOVE GENERATOR CALLED!")
	fmt.Println("⚡ Generating moves with full GADDAG implementation!")

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fmt.Printf("❌ Error parsing request: %v\n", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Normalize board
	board := normalizeBoard(req.Board)
	
	// Generate moves
	fmt.Println("🔍 Generating moves...")
	startTime := time.Now()
	moves := generateMoves(board, req.Letters)
	duration := time.Since(startTime)
	
	fmt.Printf("✅ Generated %d moves in %v\n", len(moves), duration)

	response := Response{Moves: moves}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	fmt.Println("✅ Go move generator completed successfully!")
}

func normalizeBoard(rawBoard [][]interface{}) [][]string {
	board := make([][]string, 15)
	for i := range board {
		board[i] = make([]string, 15)
		for j := range board[i] {
			if i < len(rawBoard) && j < len(rawBoard[i]) && rawBoard[i][j] != nil {
				if str, ok := rawBoard[i][j].(string); ok {
					board[i][j] = str
				}
			}
		}
	}
	return board
}

func generateMoves(board [][]string, rack []string) []Move {
	var moves []Move
	moveSet := make(map[string]bool)
	
	// Convert rack to uppercase and handle blanks
	rackArr := make([]string, len(rack))
	for i, tile := range rack {
		if tile == "*" {
			rackArr[i] = "?"
		} else {
			rackArr[i] = strings.ToUpper(tile)
		}
	}

	fmt.Printf("🔍 Rack: %v\n", rackArr)

	// Check if board is empty (first move)
	isEmpty := true
	for row := 0; row < 15; row++ {
		for col := 0; col < 15; col++ {
			if board[row][col] != "" {
				isEmpty = false
				break
			}
		}
		if !isEmpty {
			break
		}
	}
	
	if isEmpty {
		fmt.Println("🎯 Board is empty - generating first move at center")
		// For first move, allow placement at center (7,7)
		centerAnchor := struct{ row, col int }{7, 7}
		anchorMoves := generateMovesAtAnchor(board, rackArr, centerAnchor, moveSet)
		moves = append(moves, anchorMoves...)
		fmt.Printf("✅ Generated %d first moves\n", len(anchorMoves))
		return moves
	}

	// Find anchors
	anchors := findAnchors(board)
	fmt.Printf("📍 Found %d anchors\n", len(anchors))
	
	// Generate moves at each anchor
	for i, anchor := range anchors {
		fmt.Printf("🎯 Processing anchor %d at (%d, %d)\n", i+1, anchor.row, anchor.col)
		anchorMoves := generateMovesAtAnchor(board, rackArr, anchor, moveSet)
		fmt.Printf("   Generated %d moves at this anchor\n", len(anchorMoves))
		moves = append(moves, anchorMoves...)
	}

	fmt.Printf("✅ Total moves generated: %d\n", len(moves))
	return moves
}

func findAnchors(board [][]string) []struct{ row, col int } {
	var anchors []struct{ row, col int }
	
	for row := 0; row < 15; row++ {
		for col := 0; col < 15; col++ {
			if board[row][col] == "" && isAnchor(board, row, col) {
				anchors = append(anchors, struct{ row, col int }{row, col})
			}
		}
	}
	
	return anchors
}

func isAnchor(board [][]string, row, col int) bool {
	// Check if position is adjacent to existing tiles
	for dr := -1; dr <= 1; dr++ {
		for dc := -1; dc <= 1; dc++ {
			if dr == 0 && dc == 0 {
				continue
			}
			nr, nc := row+dr, col+dc
			if nr >= 0 && nr < 15 && nc >= 0 && nc < 15 && board[nr][nc] != "" {
				return true
			}
		}
	}
	return false
}

func generateMovesAtAnchor(board [][]string, rack []string, anchor struct{ row, col int }, moveSet map[string]bool) []Move {
	var moves []Move
	
	// Try horizontal and vertical directions
	for _, direction := range []string{"horizontal", "vertical"} {
		// Get existing word at this position
		existingWord := getExistingWord(board, anchor.row, anchor.col, direction)
		
		// Generate words using GADDAG traversal
		words := generateWordsWithGADDAG(board, rack, anchor.row, anchor.col, direction, existingWord)
		
		for _, word := range words {
			move := createMove(board, word, anchor.row, anchor.col, direction, rack)
			if move != nil {
				moveKey := fmt.Sprintf("%s-%d,%d-%s", move.Word, move.StartRow, move.StartCol, move.Direction)
				if !moveSet[moveKey] {
					moves = append(moves, *move)
					moveSet[moveKey] = true
				}
			}
		}
	}
	
	return moves
}

func getExistingWord(board [][]string, row, col int, direction string) string {
	var word string
	
	if direction == "horizontal" {
		// Find start of word
		startCol := col
		for startCol > 0 && board[row][startCol-1] != "" {
			startCol--
		}
		
		// Build word
		for c := startCol; c < 15 && board[row][c] != ""; c++ {
			word += board[row][c]
		}
	} else {
		// Find start of word
		startRow := row
		for startRow > 0 && board[startRow-1][col] != "" {
			startRow--
		}
		
		// Build word
		for r := startRow; r < 15 && board[r][col] != ""; r++ {
			word += board[r][col]
		}
	}
	
	return word
}

func generateWordsWithGADDAG(board [][]string, rack []string, row, col int, direction, existingWord string) []string {
	var words []string
	
	fmt.Printf("   🔤 Generating words at (%d, %d) %s, existing: '%s'\n", row, col, direction, existingWord)
	
	// Find the leftmost/topmost position for potential words through this anchor
	leftLimit := col
	topLimit := row
	
	if direction == "horizontal" {
		for leftLimit > 0 && board[row][leftLimit-1] == "" {
			leftLimit--
		}
	} else {
		for topLimit > 0 && board[topLimit-1][col] == "" {
			topLimit--
		}
	}
	
	// Try all possible starting positions
	if direction == "horizontal" {
		for startCol := leftLimit; startCol <= col; startCol++ {
			words = append(words, generateWordsFromPosition(board, rack, row, startCol, direction)...)
		}
	} else {
		for startRow := topLimit; startRow <= row; startRow++ {
			words = append(words, generateWordsFromPosition(board, rack, startRow, col, direction)...)
		}
	}
	
	fmt.Printf("   📝 Generated %d valid words\n", len(words))
	return words
}

func generateWordsFromPosition(board [][]string, rack []string, row, col int, direction string) []string {
	var words []string
	
	// Create a copy of the rack for this position
	rackCopy := make([]string, len(rack))
	copy(rackCopy, rack)
	
	// Start GADDAG traversal
	traverseGADDAG(gaddag, "", board, rackCopy, row, col, direction, &words)
	
	return words
}

func traverseGADDAG(node *GADDAGNode, currentWord string, board [][]string, rack []string, row, col int, direction string, words *[]string) {
	// Check if we've reached a terminal node
	if node.IsTerminal && len(currentWord) > 0 {
		// Validate the word can be placed
		if canPlaceWord(board, currentWord, row, col, direction, rack) {
			*words = append(*words, currentWord)
		}
	}
	
	// Try all possible letters from the rack
	for i, tile := range rack {
		if tile == "?" {
			// Try all letters for blank
			for letter := 'A'; letter <= 'Z'; letter++ {
				letterStr := string(letter)
				if child, exists := node.Children[letterStr]; exists {
					// Remove blank from rack
					newRack := make([]string, len(rack))
					copy(newRack, rack)
					newRack = append(newRack[:i], newRack[i+1:]...)
					
					// Continue traversal
					traverseGADDAG(child, currentWord+letterStr, board, newRack, row, col, direction, words)
				}
			}
		} else {
			// Try specific letter
			if child, exists := node.Children[tile]; exists {
				// Remove tile from rack
				newRack := make([]string, len(rack))
				copy(newRack, rack)
				newRack = append(newRack[:i], newRack[i+1:]...)
				
				// Continue traversal
				traverseGADDAG(child, currentWord+tile, board, newRack, row, col, direction, words)
			}
		}
	}
	
	// Also try existing letters on the board
	if direction == "horizontal" {
		for c := col; c < 15 && board[row][c] != ""; c++ {
			letter := board[row][c]
			if child, exists := node.Children[letter]; exists {
				traverseGADDAG(child, currentWord+letter, board, rack, row, c+1, direction, words)
			} else {
				break
			}
		}
	} else {
		for r := row; r < 15 && board[r][col] != ""; r++ {
			letter := board[r][col]
			if child, exists := node.Children[letter]; exists {
				traverseGADDAG(child, currentWord+letter, board, rack, r+1, col, direction, words)
			} else {
				break
			}
		}
	}
}

func canPlaceWord(board [][]string, word string, row, col int, direction string, rack []string) bool {
	// Check if word can be placed at position
	rackCopy := make([]string, len(rack))
	copy(rackCopy, rack)
	
	if direction == "horizontal" {
		for i, letter := range word {
			if col+i >= 15 {
				return false
			}
			if board[row][col+i] == "" {
				// Need to use a tile from rack
				found := false
				for j, rackTile := range rackCopy {
					if rackTile == string(letter) || rackTile == "?" {
						rackCopy = append(rackCopy[:j], rackCopy[j+1:]...)
						found = true
						break
					}
				}
				if !found {
					return false
				}
			} else if board[row][col+i] != string(letter) {
				return false
			}
		}
	} else {
		for i, letter := range word {
			if row+i >= 15 {
				return false
			}
			if board[row+i][col] == "" {
				// Need to use a tile from rack
				found := false
				for j, rackTile := range rackCopy {
					if rackTile == string(letter) || rackTile == "?" {
						rackCopy = append(rackCopy[:j], rackCopy[j+1:]...)
						found = true
						break
					}
				}
				if !found {
					return false
				}
			} else if board[row+i][col] != string(letter) {
				return false
			}
		}
	}
	
	return true
}

func createMove(board [][]string, word string, row, col int, direction string, rack []string) *Move {
	// Find starting position
	startRow, startCol := row, col
	
	if direction == "horizontal" {
		// Find leftmost position
		for startCol > 0 && board[row][startCol-1] != "" {
			startCol--
		}
	} else {
		// Find topmost position
		for startRow > 0 && board[startRow-1][col] != "" {
			startRow--
		}
	}
	
	// Create tiles
	var tiles []Tile
	rackCopy := make([]string, len(rack))
	copy(rackCopy, rack)
	
	if direction == "horizontal" {
		for i, letter := range word {
			if startCol+i >= 15 {
				return nil
			}
			if board[row][startCol+i] == "" {
				// Use tile from rack
				found := false
				for j, rackTile := range rackCopy {
					if rackTile == string(letter) || rackTile == "?" {
						tiles = append(tiles, Tile{
							Row:     row,
							Col:     startCol + i,
							Letter:  string(letter),
							IsNew:   true,
							IsBlank: rackTile == "?",
						})
						rackCopy = append(rackCopy[:j], rackCopy[j+1:]...)
						found = true
						break
					}
				}
				if !found {
					return nil
				}
			}
		}
	} else {
		for i, letter := range word {
			if startRow+i >= 15 {
				return nil
			}
			if board[startRow+i][col] == "" {
				// Use tile from rack
				found := false
				for j, rackTile := range rackCopy {
					if rackTile == string(letter) || rackTile == "?" {
						tiles = append(tiles, Tile{
							Row:     startRow + i,
							Col:     col,
							Letter:  string(letter),
							IsNew:   true,
							IsBlank: rackTile == "?",
						})
						rackCopy = append(rackCopy[:j], rackCopy[j+1:]...)
						found = true
						break
					}
				}
				if !found {
					return nil
				}
			}
		}
	}
	
	if len(tiles) == 0 {
		return nil
	}
	
	// Calculate score
	score := calculateScore(board, tiles, startRow, startCol, direction)
	
	return &Move{
		Word:       word,
		Score:      score,
		Tiles:      tiles,
		Direction:  direction,
		StartRow:   startRow,
		StartCol:   startCol,
		TotalValue: float64(score),
	}
}

func calculateScore(board [][]string, tiles []Tile, startRow, startCol int, direction string) int {
	score := 0
	wordMultiplier := 1
	
	if direction == "horizontal" {
		for _, tile := range tiles {
			letterScore := letterScores[tile.Letter]
			letterMultiplier := letterMultipliers[tile.Row][tile.Col]
			score += letterScore * letterMultiplier
			wordMultiplier *= wordMultipliers[tile.Row][tile.Col]
		}
	} else {
		for _, tile := range tiles {
			letterScore := letterScores[tile.Letter]
			letterMultiplier := letterMultipliers[tile.Row][tile.Col]
			score += letterScore * letterMultiplier
			wordMultiplier *= wordMultipliers[tile.Row][tile.Col]
		}
	}
	
	return score * wordMultiplier
} 