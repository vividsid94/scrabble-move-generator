package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// proxyHandler is the Railway home for what was netlify_functions/proxy.js -
// a generic CORS relay so the browser can fetch cross-tables.com annotated
// games and the Woogles API, neither of which sends CORS headers of their
// own (verified directly: cross-tables' annotated-GCG endpoint returns no
// Access-Control-* headers at all, and Woogles responds 405 to an OPTIONS
// preflight, meaning it doesn't implement CORS handling for that route).
//
// Unlike the old Netlify version (which JSON.stringify'd the target
// response and relied on both ends' axios auto-parsing to reconstruct the
// original shape), this forwards the target's raw body with its real
// Content-Type - text/plain GCG stays text, Woogles' application/json
// stays JSON, no double-encoding round trip needed.
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		writeProxyError(w, http.StatusBadRequest, "URL parameter is required")
		return
	}

	method := strings.ToUpper(r.URL.Query().Get("method"))
	if method == "" {
		method = http.MethodGet
	}

	var reqBody io.Reader
	if method == http.MethodPost {
		if body := r.URL.Query().Get("body"); body != "" {
			reqBody = strings.NewReader(body)
		}
	}

	proxyReq, err := http.NewRequest(method, targetURL, reqBody)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway, err.Error())
		return
	}

	if method == http.MethodPost {
		proxyReq.Header.Set("Content-Type", "application/json")
		proxyReq.Header.Set("Accept", "application/json")
	} else {
		proxyReq.Header.Set("Accept", "*/*")
		proxyReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Whiffers/1.0)")
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		proxyReq.Header.Set("Authorization", auth)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway, err.Error())
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func writeProxyError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
