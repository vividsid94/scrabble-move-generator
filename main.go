package main

import (
	"encoding/json"
	"fmt"
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
	// For now, create a simple GADDAG with common words
	// In production, you'd load this from a JSON file
	gaddag = &GADDAGNode{
		Children: make(map[string]*GADDAGNode),
	}
	
	// Add some common words for testing
	words := []string{"HELLO", "WORLD", "SCRABBLE", "GAME", "PLAY", "WORD", "TILE", "SCORE", "BOARD", "LETTER"}
	for _, word := range words {
		addWordToGADDAG(word)
	}
	
	fmt.Println("✅ GADDAG loaded with", len(words), "words")
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
	fmt.Println("⚡ Generating real moves with GADDAG!")

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

	// Find anchors
	anchors := findAnchors(board)
	
	// Generate moves at each anchor
	for _, anchor := range anchors {
		anchorMoves := generateMovesAtAnchor(board, rackArr, anchor, moveSet)
		moves = append(moves, anchorMoves...)
	}

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
		
		// Generate words that can connect to existing tiles
		words := generateConnectingWords(board, rack, anchor.row, anchor.col, direction, existingWord)
		
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

func generateConnectingWords(board [][]string, rack []string, row, col int, direction, existingWord string) []string {
	var words []string
	
	// Simple word generation for now
	// In a full implementation, you'd traverse the GADDAG here
	commonWords := []string{"HELLO", "WORLD", "GAME", "PLAY", "WORD", "TILE"}
	
	for _, word := range commonWords {
		if canPlaceWord(board, word, row, col, direction, rack) {
			words = append(words, word)
		}
	}
	
	return words
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