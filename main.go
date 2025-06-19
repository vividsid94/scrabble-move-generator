package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

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

func handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"service": "scrabble-move-generator",
		"status": "running",
		"endpoints": "POST /generate-moves, GET /health",
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
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
	fmt.Println("⚡ This should be much faster than JavaScript!")

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fmt.Printf("❌ Error parsing request: %v\n", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	fmt.Printf("📊 Received request - Letters: %v\n", req.Letters)
	fmt.Printf("📊 Board dimensions: %d x %d\n", len(req.Board), len(req.Board[0]))

	// For now, return dummy moves to test
	fmt.Println("🔍 Returning dummy moves for testing...")
	dummyMoves := []Move{
		{
			Word:      "GO",
			Score:     5,
			Tiles:     []Tile{{Row: 7, Col: 7, Letter: "G", IsNew: true, IsBlank: false}},
			Direction: "horizontal",
			StartRow:  7,
			StartCol:  7,
			TotalValue: 5.0,
		},
		{
			Word:      "WORKS",
			Score:     12,
			Tiles:     []Tile{{Row: 8, Col: 7, Letter: "W", IsNew: true, IsBlank: false}},
			Direction: "horizontal",
			StartRow:  8,
			StartCol:  7,
			TotalValue: 12.0,
		},
		{
			Word:      "FAST",
			Score:     7,
			Tiles:     []Tile{{Row: 9, Col: 7, Letter: "F", IsNew: true, IsBlank: false}},
			Direction: "horizontal",
			StartRow:  9,
			StartCol:  7,
			TotalValue: 7.0,
		},
	}

	response := Response{Moves: dummyMoves}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
	
	fmt.Println("✅ Go move generator completed successfully!")
} 