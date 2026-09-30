package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omarluq/cc-relay/codexauth"
	"github.com/omarluq/cc-relay/internal/config"
)

type fakeImagesConfigProvider struct{ cfg *config.Config }

func (f *fakeImagesConfigProvider) Get() *config.Config { return f.cfg }

type fakeTokenSource struct {
	tok *codexauth.Token
	err error
}

func (f fakeTokenSource) Get() (*codexauth.Token, error) { return f.tok, f.err }

func newTestImagesHandler(t *testing.T, upstreamURL string, tokens CodexImagesTokenSource) *ImagesHandler {
	t.Helper()
	cfg := &config.Config{}
	cfg.CodexImages = config.CodexImagesConfig{
		Enabled:          true,
		BaseURL:          upstreamURL,
		Model:            "gpt-test",
		RequestTimeoutMs: 5000,
	}
	h, err := NewImagesHandler(&ImagesHandlerOptions{
		ConfigProvider: &fakeImagesConfigProvider{cfg: cfg},
		TokenSource:    tokens,
	})
	if err != nil {
		t.Fatalf("NewImagesHandler: %v", err)
	}
	return h
}

func validToken() *codexauth.Token {
	return &codexauth.Token{AccessToken: "tok-123", AccountID: "acc-1", AuthMode: "chatgpt"}
}

// upstreamFixture streams a successful image generation SSE exchange and
// validates the translated outbound request.
func upstreamFixture(t *testing.T, calls *int32, checkBody func(t *testing.T, body map[string]interface{})) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)

		if r.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("chatgpt-account-id"); got != "acc-1" {
			t.Errorf("chatgpt-account-id = %q", got)
		}
		if got := r.Header.Get("originator"); got == "" {
			t.Errorf("originator header missing")
		}
		if got := r.Header.Get("session_id"); got == "" {
			t.Errorf("session_id header missing")
		}
		if got := r.Header.Get("OpenAI-Beta"); got != "responses=experimental" {
			t.Errorf("OpenAI-Beta = %q", got)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("upstream body not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if checkBody != nil {
			checkBody(t, body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-codex-primary-used-percent", "11")
		_, _ = io.WriteString(w, sseFixture)
	})
}

func TestImagesHandlerGenerationsSuccess(t *testing.T) {
	var calls int32
	up := httptest.NewServer(upstreamFixture(t, &calls, func(t *testing.T, body map[string]interface{}) {
		if body["model"] != "gpt-test" {
			t.Errorf("model = %v", body["model"])
		}
		tools := body["tools"].([]interface{})
		tool := tools[0].(map[string]interface{})
		if tool["type"] != "image_generation" {
			t.Errorf("tool type = %v", tool["type"])
		}
		if tool["size"] != "1024x1024" || tool["quality"] != "low" {
			t.Errorf("tool params = %v", tool)
		}
		if body["stream"] != true {
			t.Errorf("stream = %v", body["stream"])
		}
	}))
	defer up.Close()

	h := newTestImagesHandler(t, up.URL, fakeTokenSource{tok: validToken()})

	reqBody := `{"model":"gpt-image-2","prompt":"a red coffee mug icon","size":"1024x1024","quality":"low"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(reqBody))
	h.HandleGenerations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp ImagesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data = %+v", resp.Data)
	}
	if resp.Data[0].B64JSON != "ZmluYWwtaW1hZ2UtYjY0" {
		t.Errorf("b64_json = %q", resp.Data[0].B64JSON)
	}
	if resp.Data[0].RevisedPrompt != "a flat minimal red coffee mug icon" {
		t.Errorf("revised_prompt = %q", resp.Data[0].RevisedPrompt)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 46 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Created == 0 {
		t.Errorf("created missing")
	}
	if got := rec.Header().Get("x-codex-primary-used-percent"); got != "11" {
		t.Errorf("quota header = %q", got)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d", calls)
	}
}

func TestImagesHandlerN2(t *testing.T) {
	var calls int32
	up := httptest.NewServer(upstreamFixture(t, &calls, nil))
	defer up.Close()

	h := newTestImagesHandler(t, up.URL, fakeTokenSource{tok: validToken()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"prompt":"x","n":2}`))
	h.HandleGenerations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ImagesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Errorf("data = %d items, want 2", len(resp.Data))
	}
	if calls != 2 {
		t.Errorf("upstream calls = %d, want 2", calls)
	}
}

func TestImagesHandlerEdits(t *testing.T) {
	var calls int32
	pngMagic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	up := httptest.NewServer(upstreamFixture(t, &calls, func(t *testing.T, body map[string]interface{}) {
		input := body["input"].([]interface{})
		item := input[0].(map[string]interface{})
		content := item["content"].([]interface{})
		if len(content) != 2 {
			t.Fatalf("content parts = %d, want 2 (image + text)", len(content))
		}
		img := content[0].(map[string]interface{})
		if img["type"] != "input_image" {
			t.Errorf("first part type = %v", img["type"])
		}
		url := img["image_url"].(string)
		if !strings.HasPrefix(url, "data:image/png;base64,") {
			t.Errorf("image_url = %q", url)
		}
		txt := content[1].(map[string]interface{})
		if txt["type"] != "input_text" || txt["text"] != "make the mug blue" {
			t.Errorf("text part = %v", txt)
		}
	}))
	defer up.Close()

	h := newTestImagesHandler(t, up.URL, fakeTokenSource{tok: validToken()})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("image", "input.png")
	_, _ = fw.Write(pngMagic)
	_ = mw.WriteField("prompt", "make the mug blue")
	_ = mw.WriteField("size", "1024x1024")
	_ = mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	h.HandleEdits(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ImagesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Errorf("data = %+v", resp.Data)
	}
}

func TestImagesHandlerUpstream401(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"token expired"}`)
	}))
	defer up.Close()

	h := newTestImagesHandler(t, up.URL, fakeTokenSource{tok: validToken()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"prompt":"x"}`))
	h.HandleGenerations(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var errResp imagesErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("error body not JSON: %v", err)
	}
	if !strings.Contains(errResp.Error.Message, "Codex login expired") {
		t.Errorf("message = %q", errResp.Error.Message)
	}
}

func TestImagesHandlerUpstream429(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "rate limited")
	}))
	defer up.Close()

	h := newTestImagesHandler(t, up.URL, fakeTokenSource{tok: validToken()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"prompt":"x"}`))
	h.HandleGenerations(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestImagesHandlerValidation(t *testing.T) {
	h := newTestImagesHandler(t, "http://127.0.0.1:1", fakeTokenSource{tok: validToken()})

	cases := []struct {
		name string
		body string
		code int
		want string
	}{
		{"missing prompt", `{"model":"gpt-image-2"}`, http.StatusBadRequest, "prompt is required"},
		{"url format", `{"prompt":"x","response_format":"url"}`, http.StatusBadRequest, "not supported"},
		{"bad json", `{`, http.StatusBadRequest, "invalid JSON"},
		{"n too large", `{"prompt":"x","n":99}`, http.StatusBadRequest, "n must be at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(tc.body))
			h.HandleGenerations(rec, req)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body = %s, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestImagesHandlerTokenUnavailable(t *testing.T) {
	h := newTestImagesHandler(t, "http://127.0.0.1:1",
		fakeTokenSource{err: io.ErrClosedPipe})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"prompt":"x"}`))
	h.HandleGenerations(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}
