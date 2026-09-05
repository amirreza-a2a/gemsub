package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	targetURL := "https://gemini.google.com/app"
	outputFile := "gemini_response.html"

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		return
	}

	// Set browser headers to receive the complete page response.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	fmt.Println("Sending request to:", targetURL)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	fmt.Printf("HTTP Status: %s\n", resp.Status)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("Error reading body: %v\n", err)
		return
	}

	err = os.WriteFile(outputFile, body, 0644)
	if err != nil {
		fmt.Printf("Error saving to file: %v\n", err)
		return
	}

	fmt.Printf("Response successfully saved to %s (%d bytes)\n", outputFile, len(body))
}
