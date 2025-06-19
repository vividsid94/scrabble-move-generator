# Scrabble Move Generator

Fast Go-based Scrabble move generator service for Render deployment.

## Features

- ⚡ **Fast move generation** using Go
- 🚀 **Render deployment** - Simple and reliable
- 📊 **JSON API** - Easy to integrate with existing apps
- 🔧 **Health check endpoint** - For monitoring

## API Endpoints

### POST `/generate-moves`
Generate Scrabble moves for a given board and rack.

**Request:**
```json
{
  "board": [["A", "B", null, ...], ...],
  "letters": ["A", "B", "C", "D", "E", "F", "G"],
  "pool": ["A", "B", "C", ...]
}
```

**Response:**
```json
{
  "moves": [
    {
      "word": "GO",
      "score": 5,
      "tiles": [{"row": 7, "col": 7, "letter": "G", "isNew": true, "isBlank": false}],
      "direction": "horizontal",
      "startRow": 7,
      "startCol": 7,
      "totalValue": 5.0
    }
  ]
}
```

### GET `/health`
Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "service": "scrabble-move-generator",
  "version": "1.0.0"
}
```

### GET `/`
Root endpoint with service info.

**Response:**
```json
{
  "service": "scrabble-move-generator",
  "status": "running",
  "endpoints": "POST /generate-moves, GET /health"
}
```

## Deployment

1. Push to GitHub
2. Connect to Render
3. Render auto-deploys
4. Get the URL and update your Netlify functions

## Local Development

```bash
# Run locally
go run main.go

# Test endpoints
curl http://localhost:8080/health
curl -X POST http://localhost:8080/generate-moves \
  -H "Content-Type: application/json" \
  -d '{"board":[],"letters":["A","B","C"],"pool":[]}'
```

## Integration

Update your Netlify functions to call this service instead of local move generation:

```javascript
const response = await fetch('https://your-service.onrender.com/generate-moves', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ board, letters, pool })
});
```
