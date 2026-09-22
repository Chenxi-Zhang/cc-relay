package proxy_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/proxy"
	"github.com/omarluq/cc-relay/internal/router"
)

type staticConfigGetter struct {
	cfg *config.Config
}

func (g staticConfigGetter) Get() *config.Config { return g.cfg }

type capturedUpstreamRequest struct {
	Method        string
	Path          string
	Authorization string
	ContentType   string
	Body          []byte
}

func newResponsesPassthroughMux(
	t *testing.T,
	backendURL string,
	modelMapping map[string]string,
) http.Handler {
	t.Helper()

	providerName := "responses-test"
	prov := providers.NewOpenAIProviderWithMapping(
		providerName,
		backendURL+"/v1",
		[]string{"gpt-test", "client-model"},
		modelMapping,
	)
	infos := []router.ProviderInfo{proxy.TestProviderInfo(prov)}

	mux := http.NewServeMux()
	err := proxy.SetupResponsesRoutes(mux, &proxy.OpenAIRoutesOptions{
		ProviderRouter:    router.NewFailoverRouter(0),
		ConfigProvider:    staticConfigGetter{cfg: &config.Config{}},
		ProviderInfosFunc: func() []router.ProviderInfo { return infos },
		GetProviderKeys: func() map[string]string {
			return map[string]string{providerName: "sk-upstream"}
		},
		DebugOptions: proxy.TestDebugOptions(),
	})
	require.NoError(t, err)

	return mux
}

func serveResponsesPassthrough(
	t *testing.T,
	mux http.Handler,
	requestBody string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodPost,
		"/openai/v1/responses",
		bytes.NewBufferString(requestBody),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-secret")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func captureUpstreamRequest(t *testing.T, w http.ResponseWriter, r *http.Request) capturedUpstreamRequest {
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	return capturedUpstreamRequest{
		Method:        r.Method,
		Path:          r.URL.Path,
		Authorization: r.Header.Get("Authorization"),
		ContentType:   r.Header.Get("Content-Type"),
		Body:          body,
	}
}

func TestResponsesPassthrough_StringInput_ReachesUpstreamUnchanged(t *testing.T) {
	t.Parallel()

	const requestBody = `{"model":"gpt-test","input":"hello","stream":false}`
	const responseBody = `{"id":"resp_test","object":"response","status":"completed","output":[]}`

	var captured capturedUpstreamRequest
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = captureUpstreamRequest(t, w, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer backend.Close()

	rec := serveResponsesPassthrough(t, newResponsesPassthroughMux(t, backend.URL, nil), requestBody)

	require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())
	assert.Equal(t, http.MethodPost, captured.Method)
	assert.Equal(t, "/v1/responses", captured.Path)
	assert.Equal(t, "application/json", captured.ContentType)
	assert.Equal(t, "Bearer sk-upstream", captured.Authorization)
	assert.Equal(t, []byte(requestBody), captured.Body)
	assert.Equal(t, responseBody, rec.Body.String())
}

func TestResponsesPassthrough_ArrayInput_ReachesUpstreamUnchanged(t *testing.T) {
	t.Parallel()

	const requestBody = `{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"stream":false}`
	const responseBody = `{"id":"resp_test","object":"response","status":"completed","output":[]}`

	var captured capturedUpstreamRequest
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = captureUpstreamRequest(t, w, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer backend.Close()

	rec := serveResponsesPassthrough(t, newResponsesPassthroughMux(t, backend.URL, nil), requestBody)

	require.Equal(t, http.StatusOK, rec.Code, "response body: %s", rec.Body.String())
	assert.Equal(t, http.MethodPost, captured.Method)
	assert.Equal(t, "/v1/responses", captured.Path)
	assert.Equal(t, "application/json", captured.ContentType)
	assert.Equal(t, "Bearer sk-upstream", captured.Authorization)
	assert.Equal(t, []byte(requestBody), captured.Body)
	assert.Equal(t, responseBody, rec.Body.String())
}
