package api

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// GetRawAPIShaderData fetches the JSON data for a given Shadertoy ID.
// It sends a POST request to the Shadertoy API endpoint with specific
// browser-like headers and returns the raw JSON string response.
func GetRawAPIShaderData(shaderID string) (string, error) {
	// The endpoint for fetching shader data.
	apiURL := "https://www.shadertoy.com/shadertoy"

	// Construct the JSON payload required by the API.
	// The payload is a JSON string within a URL-encoded form value.
	// Example: s={"shaders":["4lSGRV"]}
	data := url.Values{}
	jsonPayload := fmt.Sprintf(`{"shaders":["%s"]}`, shaderID)
	data.Set("s", jsonPayload)

	// Create the HTTP request.
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Set the necessary headers to mimic the provided curl command.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_10_3) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/43.0.2357.124 Safari/537.36")
	req.Header.Set("Origin", "https://www.shadertoy.com")
	req.Header.Set("Referer", "https://www.shadertoy.com/browse")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	req.Header.Set("Cache-Control", "max-age=0")
	// The Go http client handles encoding and keep-alive automatically.

	// Execute the request.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Check for non-successful status codes.
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bad response status: %s", resp.Status)
	}

	// Read the response body.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	// Return the body content as a string.
	return string(body), nil
}
