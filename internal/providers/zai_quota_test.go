package providers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omarluq/cc-relay/internal/providers"
)

func TestZAIQuotaURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "international anthropic base",
			baseURL: "https://api.z.ai/api/anthropic",
			want:    "https://api.z.ai/api/monitor/usage/quota/limit",
		},
		{
			name:    "domestic anthropic base",
			baseURL: "https://open.bigmodel.cn/api/anthropic",
			want:    "https://open.bigmodel.cn/api/monitor/usage/quota/limit",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := providers.ZAIQuotaURL(tt.baseURL)
			if err != nil {
				t.Fatalf("ZAIQuotaURL returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ZAIQuotaURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQueryZAIQuota(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/monitor/usage/quota/limit" {
			t.Fatalf("path = %q, want quota endpoint", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "test-api-key" {
			t.Fatalf("Authorization = %q, want API key", r.Header.Get("Authorization"))
		}
		if r.Header.Get("User-Agent") != "cc-relay" {
			t.Fatalf("User-Agent = %q, want cc-relay", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":"ok","success":true,"data":{"level":"max","limits":[{"type":"TOKENS_LIMIT","percentage":44,"nextResetTime":1773734366338}]}}`))
	}))
	defer server.Close()

	quota, err := providers.QueryZAIQuota(context.Background(), server.Client(), server.URL+"/api/anthropic", "test-api-key")
	if err != nil {
		t.Fatalf("QueryZAIQuota returned error: %v", err)
	}
	if quota.Code != 200 || !quota.Success {
		t.Fatalf("unexpected quota response: %+v", quota)
	}
	if quota.Data.Level != "max" {
		t.Fatalf("level = %q, want max", quota.Data.Level)
	}
	if len(quota.Data.Limits) != 1 || quota.Data.Limits[0].Percentage != 44 {
		t.Fatalf("unexpected limits: %+v", quota.Data.Limits)
	}
}
