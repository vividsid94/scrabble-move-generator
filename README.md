# Scrabble Move Generator API

A Go-based HTTP service for Scrabble move generation using the Macondo library and NWL23 lexicon.

## Features

- Generate optimal moves for a given rack and board state
- Validate words against NWL23 lexicon
- Find anagrams and subanagrams
- Bulk move generation for statistical analysis
- **Custom premium square locations** - customize double/triple word/letter score positions

## Endpoints

### POST `/generate-moves`

Generate the best moves for a given rack and board state.

**Request:**
```json
{
  "rack": "ABCDEFG",
  "board": [15x15 array of strings],
  "premiumSquares": [
    {"row": 7, "col": 7, "type": "REGULAR"},
    {"row": 0, "col": 0, "type": "TWS"}
  ],
  "topN": 10
}
```

**Response:**
```json
{
  "moves": [
    {
      "position": "8H",
      "word": "DECAF",
      "score": 15,
      "leave": "BG"
    }
  ],
  "total": 186
}
```

**Premium Square Types:**
- `"DLS"` - Double Letter Score
- `"TLS"` - Triple Letter Score
- `"DWS"` - Double Word Score
- `"TWS"` - Triple Word Score
- `"REGULAR"` - Regular square (no premium)
- `"CENTER"` - Center square (typically TLS in standard layout)

**Note:** If `premiumSquares` is omitted, the standard Scrabble board layout is used.

### POST `/validate-word`

Validate a single word against the NWL23 lexicon.

**Request:**
```json
{
  "word": "CAT"
}
```

**Response:**
```json
{
  "word": "CAT",
  "isValid": true,
  "lexicon": "NWL23"
}
```

### POST `/validate-words`

Validate multiple words in a single request.

**Request:**
```json
{
  "words": ["CAT", "DOG", "XYZ"]
}
```

**Response:**
```json
{
  "words": [
    {"word": "CAT", "isValid": true},
    {"word": "DOG", "isValid": true},
    {"word": "XYZ", "isValid": false}
  ],
  "count": 3,
  "valid": 2,
  "invalid": 1,
  "lexicon": "NWL23"
}
```

### POST `/find-anagrams`

Find all anagrams (words using all letters) from given letters.

**Request:**
```json
{
  "letters": "CAT"
}
```

**Response:**
```json
{
  "letters": "CAT",
  "anagrams": ["ACT", "CAT"],
  "count": 2,
  "lexicon": "NWL23"
}
```

### POST `/find-subanagrams`

Find all words that can be formed from the given letters (subanagrams).

**Request:**
```json
{
  "letters": "CAT"
}
```

**Response:**
```json
{
  "letters": "CAT",
  "subanagrams": ["ACT", "AT", "CAT", "TA"],
  "count": 4,
  "lexicon": "NWL23"
}
```

### POST `/bulk-move-gen`

Generate statistics on move quality by testing random racks from a tile pool.

**Request:**
```json
{
  "board": [15x15 array of strings],
  "tilePool": "AABCDEFGHIJKLMNOPQRSTUVWXYZ",
  "premiumSquares": [
    {"row": 7, "col": 7, "type": "REGULAR"}
  ],
  "iterations": 1000
}
```

**Response:**
```json
{
  "iterations": 1000,
  "averageScore": 28.07,
  "bingoPercent": 9.80,
  "totalBingos": 98,
  "totalScore": 28074,
  "lexicon": "NWL23"
}
```

**Note:** `premiumSquares` is optional. If omitted, uses standard board layout.

### GET `/health`

Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "service": "macondo-movegen"
}
```

## Custom Premium Squares

You can customize premium square locations by providing a `premiumSquares` array in your request. This allows you to:

- Override default premium square positions
- Set specific squares to regular (no premium)
- Create custom board layouts for analysis

**Example - Remove center DWS:**
```json
{
  "premiumSquares": [
    {"row": 7, "col": 7, "type": "REGULAR"}
  ]
}
```

**Example - Custom board layout:**
```json
{
  "premiumSquares": [
    {"row": 0, "col": 0, "type": "TWS"},
    {"row": 0, "col": 7, "type": "TWS"},
    {"row": 7, "col": 0, "type": "TWS"},
    {"row": 7, "col": 7, "type": "DWS"},
    {"row": 7, "col": 14, "type": "TWS"},
    {"row": 14, "col": 0, "type": "TWS"},
    {"row": 14, "col": 7, "type": "TWS"},
    {"row": 14, "col": 14, "type": "TWS"}
  ]
}
```

## Board Format

The board is represented as a 15x15 array of strings:
- Empty string `""` = empty square
- Single letter string `"A"` = letter on that square

## Running the Service

```bash
go run main-for-scrabble.go
```

The service runs on port 8080 by default, or the port specified by the `PORT` environment variable.

## Dependencies

- [Macondo](https://github.com/domino14/macondo) - Scrabble move generation engine
- [word-golib](https://github.com/domino14/word-golib) - Word graph and tile mapping libraries
- NWL23 lexicon - NASPA Word List 2023

## License

[Add your license here]