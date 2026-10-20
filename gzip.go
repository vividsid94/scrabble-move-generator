package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipHandler compresses every response for clients that accept gzip
// (every browser does). The JSON these endpoints return is very repetitive,
// so it shrinks by roughly 85-90% - e.g. /generate-moves with topN 1000
// (Play's Drawbacks mode) goes from ~400 KB to ~50 KB. That cuts Railway
// egress and makes the app quicker on mobile data. Clients that don't send
// Accept-Encoding: gzip get the plain response, unchanged.
//
// BestSpeed: the payloads are small and latency matters more than the last
// few percent of size; writers are pooled so each request doesn't allocate
// a fresh compressor.
var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.gz.Write(b)
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	// A length set by a handler would be the uncompressed size.
	w.ResponseWriter.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(status)
}

func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzipWriters.Get().(*gzip.Writer)
		gz.Reset(w)
		defer func() {
			gz.Close()
			gzipWriters.Put(gz)
		}()
		w.Header().Set("Content-Encoding", "gzip")
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}
