package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/router"
)

type ResponsesHandler struct {
	router           router.ProviderRouter
	providers        ProviderInfoFunc
	getProviderPools KeyPoolsFunc
	getProviderKeys  KeysFunc
	configProvider   config.RuntimeConfigGetter
	debugOpts        config.DebugOptions
	logger           zerolog.Logger
	httpClient       *http.Client
}

func NewResponsesHandler(opts *OpenAIHandlerOptions) (*ResponsesHandler, error) {
	if opts == nil {
		return nil, errors.New("responses handler options are required")
	}
	if opts.Router == nil {
		return nil, errors.New("responses handler: router is required")
	}
	if opts.Providers == nil {
		return nil, errors.New("responses handler: providers function is required")
	}

	return &ResponsesHandler{
		router:           opts.Router,
		providers:        opts.Providers,
		getProviderPools: opts.GetProviderPools,
		getProviderKeys:  opts.GetProviderKeys,
		configProvider:   opts.ConfigProvider,
		debugOpts:        opts.DebugOptions,
		logger:           zerolog.Nop(),
		httpClient:       &http.Client{Timeout: 180 * time.Second},
	}, nil
}

func (h *ResponsesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			"Only POST method is supported for /v1/responses")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if closeErr := r.Body.Close(); closeErr != nil {
		h.logger.Warn().Err(closeErr).Msg("failed to close request body")
	}
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("failed to read request body: %v", err))
		return
	}

	var responsesReq ResponsesAPIRequest
	if err := json.Unmarshal(bodyBytes, &responsesReq); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("failed to parse JSON request: %v", err))
		return
	}

	if responsesReq.Model == "" {
		WriteError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}

	if len(responsesReq.Input) == 0 {
		WriteError(w, http.StatusBadRequest, "invalid_request", "input is required and cannot be empty")
		return
	}

	chatReq, err := ConvertRequest(&responsesReq)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal_error",
			fmt.Sprintf("failed to convert request: %v", err))
		return
	}

	isStreaming := chatReq.Stream

	chatReqBytes, err := json.Marshal(chatReq)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal_error",
			fmt.Sprintf("failed to marshal converted request: %v", err))
		return
	}

	infos := h.providers()
	if len(infos) == 0 {
		WriteError(w, http.StatusServiceUnavailable, "server_error", "no providers available")
		return
	}

	eligible := h.filterEligibleProviders(infos)
	if len(eligible) == 0 {
		WriteError(w, http.StatusTooManyRequests, "rate_limit_error", "all provider keys exhausted")
		return
	}

	selected, err := h.router.Select(r.Context(), eligible)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "server_error",
			fmt.Sprintf("failed to select provider: %v", err))
		return
	}

	prov := selected.Provider

	selectedKey, keyID, err := h.selectKey(prov)
	if err != nil {
		if errors.Is(err, keypool.ErrAllKeysExhausted) {
			WriteError(w, http.StatusTooManyRequests, "rate_limit_error", "all keys exhausted for provider")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal_error",
			fmt.Sprintf("failed to select key: %v", err))
		return
	}

	upstreamURL := strings.TrimRight(prov.BaseURL(), "/") + "/chat/completions"

	if mapping := prov.GetModelMapping(); len(mapping) > 0 {
		rewriter := NewModelRewriter(mapping)
		chatReq.Model = rewriter.RewriteModel(chatReq.Model)
		chatReqBytes, err = json.Marshal(chatReq)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal_error",
				fmt.Sprintf("failed to re-marshal request after model rewrite: %v", err))
			return
		}
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(chatReqBytes))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal_error",
			fmt.Sprintf("failed to create upstream request: %v", err))
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+selectedKey)

	h.logger.Info().
		Str("provider", prov.Name()).
		Str("model", responsesReq.Model).
		Str("key_id", keyID).
		Bool("streaming", isStreaming).
		Msg("responses handler: forwarding translated request")

	w.Header().Set("X-Relay-Provider", prov.Name())
	w.Header().Set("X-Relay-Key-ID", keyID)

	upstreamResp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "api_error",
			fmt.Sprintf("upstream connection failed: %v", err))
		return
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode >= 400 {
		h.forwardUpstreamError(w, upstreamResp)
		return
	}

	if isStreaming {
		h.handleStreamingResponse(w, upstreamResp)
	} else {
		h.handleNonStreamingResponse(w, upstreamResp)
	}
}

func (h *ResponsesHandler) handleNonStreamingResponse(w http.ResponseWriter, upstreamResp *http.Response) {
	respBody, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "api_error",
			fmt.Sprintf("failed to read upstream response: %v", err))
		return
	}

	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		WriteError(w, http.StatusBadGateway, "api_error",
			fmt.Sprintf("failed to parse upstream response: %v", err))
		return
	}

	responsesResp, err := ConvertResponse(&chatResp)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal_error",
			fmt.Sprintf("failed to convert response: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(responsesResp); err != nil {
		h.logger.Error().Err(err).Msg("failed to write responses API response")
	}
}

func (h *ResponsesHandler) handleStreamingResponse(w http.ResponseWriter, upstreamResp *http.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	converter := NewStreamConverter(w)

	scanner := bufio.NewScanner(upstreamResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		chunk := []byte(line + "\n\n")
		if err := converter.ProcessChunk(chunk); err != nil {
			h.logger.Error().Err(err).Msg("failed to process SSE chunk")
			break
		}

		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil {
		h.logger.Error().Err(err).Msg("error reading upstream SSE stream")
	}
}

func (h *ResponsesHandler) forwardUpstreamError(w http.ResponseWriter, upstreamResp *http.Response) {
	respBody, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "api_error", "upstream returned error")
		return
	}

	var errResp map[string]any
	if json.Unmarshal(respBody, &errResp) == nil {
		if errMsg, ok := errResp["error"].(map[string]any); ok {
			if msg, ok := errMsg["message"].(string); ok {
				WriteError(w, upstreamResp.StatusCode, "upstream_error", msg)
				return
			}
		}
	}

	WriteError(w, upstreamResp.StatusCode, "upstream_error", string(respBody))
}

func (h *ResponsesHandler) filterEligibleProviders(infos []router.ProviderInfo) []router.ProviderInfo {
	eligible := make([]router.ProviderInfo, 0, len(infos))
	for _, info := range infos {
		if h.hasAvailableKeys(info.Provider.Name()) {
			eligible = append(eligible, info)
		}
	}
	return eligible
}

func (h *ResponsesHandler) hasAvailableKeys(provName string) bool {
	pools := h.resolvePools()
	if pools == nil {
		return true
	}
	pool, ok := pools[provName]
	if !ok || pool == nil {
		return true
	}
	return pool.GetStats().AvailableKeys > 0
}

func (h *ResponsesHandler) resolvePools() map[string]*keypool.KeyPool {
	if h.getProviderPools != nil {
		return h.getProviderPools()
	}
	return nil
}

func (h *ResponsesHandler) resolveKeys() map[string]string {
	if h.getProviderKeys != nil {
		return h.getProviderKeys()
	}
	return nil
}

func (h *ResponsesHandler) selectKey(prov providers.Provider) (apiKey, keyID string, err error) {
	pools := h.resolvePools()
	keys := h.resolveKeys()

	if pools != nil {
		if pool, ok := pools[prov.Name()]; ok && pool != nil {
			id, key, poolErr := pool.GetKey(context.Background())
			if poolErr != nil {
				return "", "", poolErr
			}
			return key, id, nil
		}
	}

	if keys != nil {
		if key, ok := keys[prov.Name()]; ok {
			return key, "default", nil
		}
	}

	return "", "", fmt.Errorf("no API key configured for provider %q", prov.Name())
}
