# Scrabble Move Generator - Render Deployment Guide

## Files to Copy to Your GitHub Repo

### Required Files:
1. `main-for-scrabble.go` - Main service file
2. `go.mod` - Dependencies (already updated for production)
3. `go.sum` - Dependency checksums
4. `lexica/gaddag/NWL23.kwg` - Scrabble dictionary (4.5MB)
5. `letterdistributions/` directory - Letter distribution data

### Optional Files:
- `main.go` - Demo version (not needed for production)

## Render Deployment Steps

### 1. Create GitHub Repository
- Create a new repo called `scrabble-move-generator`
- Copy all the required files above

### 2. Render Setup
- Go to [render.com](https://render.com)
- Create a new **Web Service**
- Connect your GitHub repo
- Set the following configuration:

**Build Command:**
```bash
go build -o scrabble-move-generator main-for-scrabble.go
```

**Start Command:**
```bash
./scrabble-move-generator
```

**Environment Variables:**
- `PORT` = `8080` (or leave empty to use default)

### 3. Important Notes
- The service will be available at `https://your-app-name.onrender.com`
- Health check endpoint: `GET /health`
- Move generation endpoint: `POST /generate-moves`

### 4. Testing
Once deployed, test with:
```bash
curl -X POST https://your-app-name.onrender.com/generate-moves \
  -H "Content-Type: application/json" \
  -d '{
    "rack": "AABDELT",
    "board": [["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","H","E","L","L","O","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""],["","","","","","","","","","","","","","",""]],
    "topN": 10
  }'
```

## File Structure for GitHub Repo
```
scrabble-move-generator/
├── main-for-scrabble.go
├── go.mod
├── go.sum
├── lexica/
│   └── gaddag/
│       └── NWL23.kwg
└── letterdistributions/
    └── english
```

## Troubleshooting
- If you get dependency errors, make sure `go.mod` doesn't have the local replace directive
- The dictionary file (NWL23.kwg) is large but required - Render can handle it
- Make sure all data files are in the correct relative paths 