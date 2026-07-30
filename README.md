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
  "iterations": 1000,
  "includeMoveDetails": false
}
```

**Response (default / `includeMoveDetails` absent or false):**
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

When `includeMoveDetails` is omitted or `false`, behavior matches the original aggregate-only path (single random rack → top move stats). No `iterationDetails` field is returned.

**Optional `ourLeave` (only used with `includeMoveDetails: true`):**

Pin tiles on our reply rack every iteration. Example: `"ourLeave": "QZ"` → each `ourReply` uses rack `QZ` + 5 random tiles drawn from `tilePool` minus that iteration’s opponent rack. Opponent racks stay fully random. If omitted, both sides still get fully random racks.

**Response (with `"includeMoveDetails": true`):**

Runs a two-ply simulation per iteration: opponent’s best reply on the given board, then our best reply after that play is placed. Aggregates (`totalScore`, `averageScore`, `totalBingos`, `bingoPercent`) are taken from **our reply**. Also returns `iterationDetails` with one entry per iteration:

```json
{
  "iterations": 2,
  "averageScore": 31.5,
  "bingoPercent": 0,
  "totalBingos": 0,
  "totalScore": 63,
  "lexicon": "NWL23",
  "iterationDetails": [
    {
      "opponentMove": {
        "word": "QI",
        "score": 22,
        "direction": "right",
        "startPosition": "8H",
        "tiles": [
          {"row": 7, "col": 7, "letter": "Q", "isNew": true, "isBlank": false},
          {"row": 7, "col": 8, "letter": "I", "isNew": true, "isBlank": false}
        ]
      },
      "ourReply": {
        "word": "QI",
        "score": 11,
        "direction": "down",
        "startPosition": "H8",
        "tiles": [
          {"row": 7, "col": 7, "letter": "Q", "isNew": false, "isBlank": false},
          {"row": 8, "col": 7, "letter": "I", "isNew": true, "isBlank": false}
        ]
      }
    }
  ]
}
```

If a side has no legal play in an iteration, that side’s move field is `null`. Prefer a small `iterations` value when requesting details (payload grows with each iteration).

**Note:** `premiumSquares` is optional. If omitted, uses standard board layout.

### POST `/simulate-series`

Plays out one or more complete games between two "static" bots - ones that
pick a fixed rank from a score+leaveValue-ranked candidate list every turn
(word plays and exchanges ranked together), using the embedded `leaves.json`
table - entirely server-side, returning the full turn-by-turn history for
each game in a single response. Built for bulk-simulating series without one
HTTP call per move. Rank 1 is "Theo" (best move); rank N is "Nth static".
Bots that need per-move opponent simulation (Tess) aren't supported here and
stay on the client-side loop.

**Request (legacy, rank only):**
```json
{
  "games": 10,
  "player1Rank": 1,
  "player2Rank": 5
}
```
`games` defaults to 1 and is capped at 500 per request. `player1Rank`/
`player2Rank` default to 1 (Theo) and fall back to the best available move
if the requested rank exceeds how many legal options exist on a given turn.

**Request (custom bot personalities):** `player1Bot`/`player2Bot` replace
`player1Rank`/`player2Rank` and take precedence if both are sent. Each bot
has a `rank` (same meaning as above, defaults to 1) plus an optional
`leaveRules` list - adjustments applied to every candidate leave's base
`leaves.json` value before ranking, letting two bots at the same rank play
genuinely differently:

```json
{
  "games": 10,
  "player1Bot": {
    "rank": 1,
    "leaveRules": [
      { "type": "containsLetter", "letter": "S", "bonus": 20 }
    ]
  },
  "player2Bot": { "rank": 1 }
}
```

`leaveRules` entries stack in order: `bonus` rules (flat, can be negative)
sum together, and `multiplier` rules scale the running total at the point
they appear. Supported `type` values:

| type | fields used | effect |
|---|---|---|
| `containsLetter` | `letter`, `bonus` | leave contains `letter` anywhere |
| `containsAny` | `letters`, `bonus` | leave contains any letter in `letters` |
| `containsAll` | `letters`, `bonus` | leave contains every letter in `letters` |
| `containsCount` | `letter`, `comparator`, `count`, `bonus` | count of `letter` compares to `count` |
| `vowelCount` | `comparator`, `count`, `bonus` | count of A/E/I/O/U compares to `count` |
| `consonantCount` | `comparator`, `count`, `bonus` | count of non-vowel, non-blank tiles compares to `count` |
| `hasBlank` | `bonus` | leave contains `?` |
| `lengthEquals` | `count`, `bonus` | leave length equals `count` |
| `multiplier` | `multiplier` | scales the running total so far |

`comparator` is `"gte"` (default), `"lte"`, or `"eq"`.

**Response:**
```json
{
  "games": [
    {
      "turns": [
        {
          "player": 1,
          "type": "play",
          "word": "CAT",
          "score": 10,
          "position": "8H",
          "direction": "right",
          "tiles": [{"row": 7, "col": 7, "letter": "C", "isNew": true, "isBlank": false}],
          "rackBefore": "ACHKMTZ",
          "runningTotal": 10
        }
      ],
      "player1Score": 245,
      "player2Score": 198,
      "winner": 1,
      "endReason": "emptied",
      "player1FinalRack": "",
      "player2FinalRack": "IU",
      "finalPool": ""
    }
  ]
}
```

`type` is `"play"`, `"exchange"`, or `"pass"`. Exchange turns carry
`tilesExchanged` instead of `tiles`/`word`/`position`/`direction`; pass turns
carry neither. `winner` is `1`, `2`, or `0` for a tie. `endReason` is
`"emptied"` (someone played their last tile with an empty pool) or
`"sixPasses"` (six consecutive scoreless turns).

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