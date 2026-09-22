package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/omarluq/cc-relay/internal/codexauth"
	"github.com/omarluq/cc-relay/internal/config"
)

// codexResponsesPath is the Codex backend Responses endpoint.
const codexResponsesPath = "/backend-api/codex/responses"

// maxEditsUploadBytes caps the in-memory buffering of one uploaded file.
const maxEditsUploadBytes = 50 << 20 // 50 MiB

// CodexImagesTokenSource is the subset of codexauth.TokenSource the handler
// needs. It exists so tests can substitute a fake source.
type CodexImagesTokenSource interface {
	Get() (*codexauth.Token, error)
}

// ImagesHandler translates OpenAI Images API requests into Codex backend
// Responses requests using the local Codex CLI login state.
type ImagesHandler struct {
	configProvider config.RuntimeConfigGetter
	tokens         CodexImagesTokenSource
	customTokens   bool
	httpClient     *http.Client

	mu       sync.Mutex
	tokenSrc *codexauth.TokenSource
	authPath string
}

// ImagesHandlerOptions configures NewImagesHandler.
type ImagesHandlerOptions struct {
	ConfigProvider config.RuntimeConfigGetter
	// TokenSource overrides the default codexauth.TokenSource (tests only).
	TokenSource CodexImagesTokenSource
}

// NewImagesHandler creates the Codex image gateway handler.
func NewImagesHandler(opts *ImagesHandlerOptions) (*ImagesHandler, error) {
	if opts == nil || opts.ConfigProvider == nil {
		return nil, errors.New("images handler: config provider is required")
	}

	h := &ImagesHandler{
		configProvider: opts.ConfigProvider,
		tokens:         opts.TokenSource,
		customTokens:   opts.TokenSource != nil,
		httpClient: &http.Client{
			Transport: newCodexTransport(),
			// No client-level timeout: per-request contexts carry the
			// configured timeout so long image jobs are not cut short by a
			// shared deadline.
		},
	}

	if h.tokens == nil {
		// Fail fast on an unusable default path; per-request Get() reports
		// actionable errors when Codex is not logged in.
		src, err := codexauth.NewTokenSource("")
		if err != nil {
			return nil, fmt.Errorf("images handler: %w", err)
		}
		h.tokens = src
	}

	return h, nil
}

// HandleGenerations serves POST /v1/images/generations.
func (h *ImagesHandler) HandleGenerations(w http.ResponseWriter, r *http.Request) {
	var req ImagesGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeImagesError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("invalid JSON body: %v", err))
		return
	}

	h.serveImages(w, r, &req, nil)
}

// HandleEdits serves POST /v1/images/edits (multipart/form-data).
func (h *ImagesHandler) HandleEdits(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxEditsUploadBytes); err != nil {
		writeImagesError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("invalid multipart form: %v", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	req := ImagesGenerateRequest{
		Model:          r.FormValue("model"),
		Prompt:         r.FormValue("prompt"),
		Size:           r.FormValue("size"),
		Quality:        r.FormValue("quality"),
		Background:     r.FormValue("background"),
		Moderation:     r.FormValue("moderation"),
		OutputFormat:   r.FormValue("output_format"),
		ResponseFormat: r.FormValue("response_format"),
		User:           r.FormValue("user"),
	}
	if n := parseFormInt(r, "n"); n != nil {
		req.N = *n
	}
	if oc := parseFormInt(r, "output_compression"); oc != nil {
		req.OutputCompression = oc
	}

	payloads := collectEditImages(r)
	if len(payloads) == 0 {
		writeImagesError(w, http.StatusBadRequest, "invalid_request_error",
			"an image file is required in the 'image' (or 'image[]') form field")
		return
	}

	h.serveImages(w, r, &req, payloads)
}

// serveImages runs the shared pipeline: validate, translate, call the Codex
// backend n times, aggregate SSE results, answer in Images API format.
func (h *ImagesHandler) serveImages(w http.ResponseWriter, r *http.Request, req *ImagesGenerateRequest, edits []imagePayload) {
	cfg := h.configProvider.Get()
	if cfg == nil {
		writeImagesError(w, http.StatusInternalServerError, "api_error", "configuration is unavailable")
		return
	}
	ci := &cfg.CodexImages

	if strings.TrimSpace(req.Prompt) == "" {
		writeImagesError(w, http.StatusBadRequest, "invalid_request_error", "prompt is required and must be non-empty")
		return
	}
	if req.ResponseFormat == "url" {
		writeImagesError(w, http.StatusBadRequest, "invalid_request_error",
			"response_format 'url' is not supported; the Codex backend only returns base64 images (b64_json)")
		return
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	if n > ci.GetMaxImagesPerRequest() {
		writeImagesError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("n must be at most %d", ci.GetMaxImagesPerRequest()))
		return
	}

	token, err := h.resolveToken(ci.AuthFile)
	if err != nil {
		writeImagesError(w, http.StatusServiceUnavailable, "invalid_request_error", err.Error())
		return
	}

	data := make([]ImagesResponseData, 0, n)
	usage := &ImagesUsage{}
	var lastQuota http.Header

	for i := 0; i < n; i++ {
		outbound, err := BuildCodexImageRequest(req, ci.GetModel(), ci.GetInstructions())
		if err != nil {
			writeImagesError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		if len(edits) > 0 {
			if err := prependImageParts(outbound, edits); err != nil {
				writeImagesError(w, http.StatusInternalServerError, "api_error", err.Error())
				return
			}
		}

		result, u, quota, err := h.callCodex(r.Context(), ci, token, outbound)
		lastQuota = quota
		if err != nil {
			log.Ctx(r.Context()).Warn().Err(err).Int("image_index", i).Msg("codex image generation failed")
			if i == 0 && len(data) == 0 {
				writeImagesErrorFromUpstream(w, err)
				return
			}
			break
		}
		for _, res := range result {
			data = append(data, ImagesResponseData{
				B64JSON:       res.B64JSON,
				RevisedPrompt: res.RevisedPrompt,
			})
		}
		if u != nil {
			usage.TotalTokens += u.TotalTokens
			usage.InputTokens += u.InputTokens
			usage.OutputTokens += u.OutputTokens
		}
	}

	if len(data) == 0 {
		writeImagesError(w, http.StatusBadGateway, "api_error", "no image was generated by the Codex backend")
		return
	}

	// Surface the account quota headers so clients can watch their limits.
	for _, name := range []string{
		"x-codex-primary-used-percent", "x-codex-secondary-used-percent",
		"x-codex-primary-reset-after-seconds", "x-codex-plan-type",
	} {
		if v := lastQuota.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(newImagesResponse(data, usage))
}

// callCodex performs one upstream Responses request and aggregates its SSE
// stream into image results.
func (h *ImagesHandler) callCodex(
	ctx context.Context,
	ci *config.CodexImagesConfig,
	token *codexauth.Token,
	outbound *codexImageRequest,
) ([]codexImageResult, *codexUsage, http.Header, error) {
	body, err := jsonMarshalRequest(outbound)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encoding codex request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, ci.GetRequestTimeout())
	defer cancel()

	url := ci.GetBaseURL() + codexResponsesPath
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("building codex request: %w", err)
	}
	// Header set aligned with the Codex CLI/client family. session-id /
	// thread-id / x-client-request-id mirror what current Codex clients send
	// (plus the legacy session_id spelling for older backend versions).
	requestID := uuid.New().String()
	outbound.PromptCacheKey = requestID
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	if token.AccountID != "" {
		req.Header.Set("chatgpt-account-id", token.AccountID)
	}
	req.Header.Set("originator", ci.GetOriginator())
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session-id", requestID)
	req.Header.Set("session_id", requestID)
	req.Header.Set("thread-id", requestID)
	req.Header.Set("x-client-request-id", requestID)
	req.Header.Set("User-Agent", ci.GetUserAgent())

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("codex backend request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()

	quota := resp.Header.Clone()
	if resp.StatusCode != http.StatusOK {
		msg := readLimitedBody(resp.Body)
		return nil, nil, quota, &upstreamStatusError{status: resp.StatusCode, body: msg}
	}

	results, usage, err := aggregateCodexImageSSE(resp.Body)
	return results, usage, quota, err
}

// upstreamStatusError carries a non-200 upstream response.
type upstreamStatusError struct {
	status int
	body   string
}

func (e *upstreamStatusError) Error() string {
	return fmt.Sprintf("codex backend returned HTTP %d: %s", e.status, e.body)
}

// writeImagesErrorFromUpstream maps upstream failures to client-facing
// errors with actionable messages for the auth-specific statuses.
func writeImagesErrorFromUpstream(w http.ResponseWriter, err error) {
	var use *upstreamStatusError
	if !errors.As(err, &use) {
		writeImagesError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	switch use.status {
	case http.StatusUnauthorized:
		writeImagesError(w, http.StatusServiceUnavailable, "invalid_request_error",
			"Codex login expired — open the Codex app (or run `codex login`) to refresh the token, then retry")
	case http.StatusForbidden:
		writeImagesError(w, http.StatusForbidden, "invalid_request_error",
			"Codex backend rejected the request (account may lack access): "+use.body)
	case http.StatusTooManyRequests:
		writeImagesError(w, http.StatusTooManyRequests, "rate_limit_error",
			"Codex account rate limit reached, retry later: "+use.body)
	default:
		writeImagesError(w, http.StatusBadGateway, "api_error", use.Error())
	}
}

// resolveToken returns the current token, rebuilding the token source when
// the configured auth file path changed via hot-reload.
func (h *ImagesHandler) resolveToken(authFile string) (*codexauth.Token, error) {
	if h.customTokens {
		return h.tokens.Get()
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tokenSrc == nil || h.authPath != authFile {
		src, err := codexauth.NewTokenSource(authFile)
		if err != nil {
			return nil, err
		}
		h.tokenSrc = src
		h.authPath = authFile
	}
	return h.tokenSrc.Get()
}

// writeImagesError writes an OpenAI-style error response.
func writeImagesError(w http.ResponseWriter, statusCode int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(imagesErrorResponse{
		Error: imagesErrorDetail{Message: message, Type: errType},
	})
}

// jsonMarshalRequest serializes an outbound request.
func jsonMarshalRequest(v interface{}) ([]byte, error) { return json.Marshal(v) }

// readLimitedBody reads at most 4KB of an error body.
func readLimitedBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	return strings.TrimSpace(string(data))
}

// parseFormInt reads an integer form value.
func parseFormInt(r *http.Request, name string) *int {
	v := r.FormValue(name)
	if v == "" {
		return nil
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return nil
	}
	return &n
}

// collectEditImages extracts the uploaded image and mask files from an
// edits request. Mask (if present) is appended after the images.
func collectEditImages(r *http.Request) []imagePayload {
	if r.MultipartForm == nil {
		return nil
	}

	var payloads []imagePayload
	for _, field := range []string{"image", "image[]"} {
		for _, fh := range r.MultipartForm.File[field] {
			if p := readFileHeaderPayload(fh); p != nil {
				payloads = append(payloads, *p)
			}
		}
	}
	for _, fh := range r.MultipartForm.File["mask"] {
		if p := readFileHeaderPayload(fh); p != nil {
			payloads = append(payloads, *p)
		}
	}
	return payloads
}

// readFileHeaderPayload reads one multipart file with content-type sniffing.
func readFileHeaderPayload(fh *multipart.FileHeader) *imagePayload {
	f, err := fh.Open()
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxEditsUploadBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxEditsUploadBytes {
		return nil
	}

	mimeType := fh.Header.Get("Content-Type")
	if !strings.HasPrefix(mimeType, "image/") {
		sniffLen := len(data)
		if sniffLen > 512 {
			sniffLen = 512
		}
		mimeType = http.DetectContentType(data[:sniffLen])
	}
	return &imagePayload{MimeType: mimeType, Data: data}
}
