package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	zaiQuotaPath       = "/api/monitor/usage/quota/limit"
	zaiQuotaHTTPClient = 5 * time.Second
)

// ZAIQuotaResponse is the response returned by the GLM Coding Plan quota API.
type ZAIQuotaResponse struct {
	Msg     string       `json:"msg"`
	Data    ZAIQuotaData `json:"data"`
	Code    int          `json:"code"`
	Success bool         `json:"success"`
}

// ZAIQuotaData contains plan-level quota details.
type ZAIQuotaData struct {
	Level  string          `json:"level"`
	Limits []ZAIQuotaLimit `json:"limits"`
}

// ZAIQuotaLimit contains one quota bucket from the GLM Coding Plan quota API.
type ZAIQuotaLimit struct {
	Type          string `json:"type"`
	Percentage    int    `json:"percentage"`
	Usage         int    `json:"usage,omitempty"`
	CurrentValue  int    `json:"currentValue,omitempty"`
	Remaining     int    `json:"remaining,omitempty"`
	NextResetTime int64  `json:"nextResetTime,omitempty"`
}

// ZAIQuotaURL returns the GLM Coding Plan quota endpoint for a Z.AI base URL.
func ZAIQuotaURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse ZAI base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid ZAI base URL %q", baseURL)
	}
	return parsed.Scheme + "://" + parsed.Host + zaiQuotaPath, nil
}

// QueryZAIQuota fetches GLM Coding Plan quota using a Z.AI API key.
func QueryZAIQuota(ctx context.Context, client *http.Client, baseURL, apiKey string) (*ZAIQuotaResponse, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("ZAI API key is empty")
	}
	endpoint, err := ZAIQuotaURL(baseURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: zaiQuotaHTTPClient}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create ZAI quota request: %w", err)
	}
	req.Header.Set("Authorization", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cc-relay")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query ZAI quota: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("ZAI quota request failed with status %d", resp.StatusCode)
	}

	var quota ZAIQuotaResponse
	if err := json.NewDecoder(resp.Body).Decode(&quota); err != nil {
		return nil, fmt.Errorf("decode ZAI quota response: %w", err)
	}
	return &quota, nil
}
