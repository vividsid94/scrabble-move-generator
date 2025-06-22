package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/domino14/word-golib/kwg"
	"github.com/domino14/word-golib/tilemapping"

	"github.com/domino14/macondo/board"
	"github.com/domino14/macondo/config"
	"github.com/domino14/macondo/cross_set"
	"github.com/domino14/macondo/movegen"
)

// Request/Response structures
// Board is a 15x15 array of strings ("" for empty, or a single letter)

func setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "http://localhost:8888" || origin == "https://tileturnover.com" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Max-Age", "86400") // 24 hours
}

type GenerateMovesRequest struct {
	Rack  string     `json:"rack"`
	Board [][]string `json:"board"` // 15x15 board as strings
	TopN  int        `json:"topN,omitempty"`
}

type Move struct {
	Position string `json:"position"`
	Word     string `json:"word"`
	Score    int    `json:"score"`
	Leave    string `json:"leave"`
}

type GenerateMovesResponse struct {
	Moves []Move `json:"moves"`
	Total int    `json:"total"`
}

// Global state (safe for demo, not for production concurrency)
var (
	gd   *kwg.KWG
	alph *tilemapping.TileMapping
	ld   *tilemapping.LetterDistribution
)

func main() {
	if err := initService(); err != nil {
		log.Fatalf("Failed to initialize service: %v", err)
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/generate-moves", generateMovesHandler)

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
	var err error
	gd, err = kwg.GetKWG(cfg.WGLConfig(), "NWL23")
	if err != nil {
		return fmt.Errorf("failed to load lexicon: %v", err)
	}
	alph = gd.GetAlphabet()
	ld, err = tilemapping.EnglishLetterDistribution(cfg.WGLConfig())
	if err != nil {
		return fmt.Errorf("failed to load letter distribution: %v", err)
	}
	fmt.Println("✓ Loaded lexicon and letter distribution")
	return nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "healthy",
		"service": "macondo-movegen",
	})
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
	
	// Create and initialize the board
	bd := board.MakeBoard(board.CrosswordGameBoard)
	
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
	
	responseMoves := make([]Move, 0, req.TopN)
	for i, m := range moves {
		if i >= req.TopN {
			break
		}
		
		// Extract word from move string
		moveStr := m.String()
		word := ""
		
		// Parse move string to extract word
		// Format: "<action: play word: POSITION WORD score: SCORE tp: TILES_PLAYED leave: LEAVE>"
		if strings.Contains(moveStr, "play word:") {
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
							break
						}
						break
					}
				}
			}
		}
		
		responseMoves = append(responseMoves, Move{
			Position: m.BoardCoords(),
			Word:     word,
			Score:    m.Score(),
			Leave:    m.Leave().UserVisible(alph),
		})
	}
	resp := GenerateMovesResponse{
		Moves: responseMoves,
		Total: len(moves),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}