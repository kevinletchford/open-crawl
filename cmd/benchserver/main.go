package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
)

// Config parameters for the synthetic server
const (
	port          = ":8080"
	linksPerPage  = 10     // Number of outgoing links per page generated
	maxPages      = 100000 // Determines how large the synthetic space is realistically
	payloadSizeKB = 5      // Amount of junk text to simulate real page sizes
)

var junkText string

func init() {
	// Generate some junk text once
	junkText = strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", (payloadSizeKB*1024)/55+1)
}

func main() {
	http.HandleFunc("/", handleRequest)

	fmt.Printf("Starting BenchServer on port %s...\n", port)
	fmt.Printf("Serving pages with %d simulated links each, approx %dKB payload\n", linksPerPage, payloadSizeKB)
	fmt.Printf("Test URL: http://localhost%s/page/1\n", port)

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	// Generate deterministic but random-looking links based on the requested URL
	// so the crawler has consistent paths to follow.
	path := r.URL.Path
	seed := int64(0)

	if path != "/" {
		// Use the path hash as a seed so the same URL always gives the same links
		seed = int64(hashString(path))
	}

	rng := rand.New(rand.NewSource(seed))

	// Generate HTML
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html><html><head><title>Test Page</title></head><body>")
	sb.WriteString(fmt.Sprintf("<h1>Page: %s</h1>", path))

	// Add links
	sb.WriteString("<ul>")
	for i := 0; i < linksPerPage; i++ {
		// Generate a random next page ID between 1 and maxPages
		nextPageID := rng.Intn(maxPages) + 1
		nextPath := fmt.Sprintf("/page/%d", nextPageID)
		sb.WriteString(fmt.Sprintf(`<li><a href="%s">Link to %d</a></li>`, nextPath, nextPageID))
	}
	sb.WriteString("</ul>")

	// Add payload
	sb.WriteString("<p>")
	sb.WriteString(junkText)
	sb.WriteString("</p>")

	sb.WriteString("</body></html>")

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(sb.String()))
}

// Simple hash to convert string path to int64 seed
func hashString(s string) int {
	h := 0
	for i := 0; i < len(s); i++ {
		h = 31*h + int(s[i])
	}
	return h
}
