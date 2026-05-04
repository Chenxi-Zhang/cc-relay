package providers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const MaxCooldownCap = 6 * time.Hour

const defaultCooldown = 60 * time.Second

// CooldownDecision is the result of resolving a rate-limit response.
type CooldownDecision struct {
	Cooldown    time.Duration
	IsPermanent bool
	SkipCB      bool
}

// CooldownResolver resolves cooldown duration from an HTTP response.
type CooldownResolver interface {
	Resolve(resp *http.Response) CooldownDecision
}

// NewCooldownResolver returns the resolver for the given strategy name.
func NewCooldownResolver(strategy string) CooldownResolver {
	switch strategy {
	case "zai":
		return &zaiResolver{}
	case "openai":
		return &openAIResolver{}
	default:
		return &genericResolver{}
	}
}

// --- GenericResolver ---

type genericResolver struct{}

func (g *genericResolver) Resolve(resp *http.Response) CooldownDecision {
	if resp.StatusCode != http.StatusTooManyRequests {
		return CooldownDecision{}
	}
	cd := parseRetryAfterHeader(resp.Header)
	if cd > MaxCooldownCap {
		cd = MaxCooldownCap
	}
	return CooldownDecision{Cooldown: cd}
}

// --- OpenAIResolver ---

type openAIResolver struct{}

func (o *openAIResolver) Resolve(resp *http.Response) CooldownDecision {
	if resp.StatusCode != http.StatusTooManyRequests {
		return CooldownDecision{}
	}
	if cd := o.parseResetHeaders(resp.Header); cd > 0 {
		if cd > MaxCooldownCap {
			cd = MaxCooldownCap
		}
		return CooldownDecision{Cooldown: cd}
	}
	cd := parseRetryAfterHeader(resp.Header)
	if cd > MaxCooldownCap {
		cd = MaxCooldownCap
	}
	return CooldownDecision{Cooldown: cd}
}

func (o *openAIResolver) parseResetHeaders(h http.Header) time.Duration {
	for _, key := range []string{"X-RateLimit-Reset-Requests", "X-RateLimit-Reset-Tokens"} {
		val := h.Get(key)
		if val == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			d := time.Until(t)
			if d > 0 {
				return d
			}
		}
	}
	return 0
}

// --- ZAIResolver ---

type zaiResolver struct{}

func (z *zaiResolver) Resolve(resp *http.Response) CooldownDecision {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return z.resolve429(resp)
	case http.StatusBadRequest:
		return z.resolve400(resp)
	default:
		return CooldownDecision{}
	}
}

func (z *zaiResolver) resolve429(resp *http.Response) CooldownDecision {
	info, bodyBytes, err := ParseZAI429Error(resp.Body)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return CooldownDecision{Cooldown: parseRetryAfterHeader(resp.Header)}
	}
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))

	if info == nil {
		return CooldownDecision{Cooldown: parseRetryAfterHeader(resp.Header)}
	}
	if info.IsPermanent {
		return CooldownDecision{
			IsPermanent: true,
			SkipCB:      true,
		}
	}
	cd := info.Cooldown
	if cd > MaxCooldownCap {
		cd = MaxCooldownCap
	}
	return CooldownDecision{Cooldown: cd, SkipCB: info.Category == ZAICatTransient}
}

func (z *zaiResolver) resolve400(resp *http.Response) CooldownDecision {
	info, bodyBytes, err := ParseZAI429Error(resp.Body)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return CooldownDecision{}
	}
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))

	if info == nil || !IsZAIRetryable400Code(info.Code) {
		return CooldownDecision{}
	}
	return CooldownDecision{Cooldown: 30 * time.Second, SkipCB: true}
}

// --- Shared helpers ---

func parseRetryAfterHeader(headers http.Header) time.Duration {
	val := headers.Get("Retry-After")
	if val == "" {
		return defaultCooldown
	}
	if seconds, err := strconv.Atoi(val); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return defaultCooldown
}

// ZAI429Details returns structured info from a ZAI 429/400 response for logging.
func ZAI429Details(resp *http.Response) (code, message, category string, cooldown time.Duration) {
	info, bodyBytes, err := ParseZAI429Error(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))
	if err != nil || info == nil {
		return "", "", "", 0
	}
	return info.Code, info.Message, info.Category.String(), info.Cooldown
}

// FormatZAIDecisionLog creates a zerolog-compatible set of fields for a CooldownDecision from ZAI.
func FormatZAIDecisionLog(info *ZAIErrorInfo) string {
	return fmt.Sprintf("ZAI [%s] %s (%s)", info.Code, info.Message, info.Category)
}
