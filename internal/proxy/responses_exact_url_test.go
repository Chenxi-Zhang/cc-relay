package proxy_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/proxy"
	"github.com/omarluq/cc-relay/internal/router"
)

func TestResponsesPassthrough_UsesExactResponsesURLWhenConfigured(t *testing.T) {
	// Given a Chat Completions base URL and a separate exact Responses endpoint.
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":"resp_test","object":"response","status":"completed","output":[]}`))
		require.NoError(t, err)
	}))
	defer backend.Close()

	var cfg config.Config
	configYAML := fmt.Sprintf("openai_providers:\n  - name: zhipu\n    responses_url: %s/api/v1/responses\n", backend.URL)
	require.NoError(t, yaml.Unmarshal([]byte(configYAML), &cfg))

	prov := providers.NewOpenAIProviderWithMapping("zhipu", backend.URL+"/api/coding/paas/v4", []string{"opus"}, nil)
	mux := http.NewServeMux()
	require.NoError(t, proxy.SetupResponsesRoutes(mux, &proxy.OpenAIRoutesOptions{
		ProviderRouter:    router.NewFailoverRouter(0),
		ConfigProvider:    staticConfigGetter{cfg: &cfg},
		ProviderInfosFunc: func() []router.ProviderInfo { return []router.ProviderInfo{proxy.TestProviderInfo(prov)} },
		GetProviderKeys:   func() map[string]string { return map[string]string{"zhipu": "sk-upstream"} },
		DebugOptions:      proxy.TestDebugOptions(),
	}))

	// When a client sends a Responses request.
	rec := serveResponsesPassthrough(t, mux, `{"model":"opus","input":"hello"}`)

	// Then the upstream sees the configured path exactly once.
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "/api/v1/responses", receivedPath)
}
