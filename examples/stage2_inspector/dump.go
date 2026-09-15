package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "" {
		fmt.Fprintf(os.Stderr, "Usage: dump <url>\n")
		os.Exit(1)
	}
	targetURL := os.Args[1]

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	fmt.Println("Sending request to:", targetURL)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	fmt.Printf("HTTP Status: %s\n", resp.Status)
	fmt.Printf("Content-Type: %s\n", contentType)
	fmt.Printf("Content-Length: %s\n", resp.Header.Get("Content-Length"))
	fmt.Printf("Final URL: %s\n", resp.Request.URL.String())

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading body: %v\n", err)
		os.Exit(1)
	}

	outputFile := "response.html"
	if strings.Contains(contentType, "application/json") {
		outputFile = "response.json"
	} else if strings.Contains(contentType, "text/plain") {
		outputFile = "response.txt"
	}

	if err := os.WriteFile(outputFile, body, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving to file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Response saved to %s (%d bytes)\n", outputFile, len(body))
}
