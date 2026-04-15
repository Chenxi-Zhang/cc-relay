// Package providers defines the interface for LLM backend providers.
package providers

import (
	"encoding/json"
	"io"
	"regexp"
	"time"
)

// ZAI (Zhipu AI) business error codes returned in 429 responses.
// These codes provide fine-grained information about the rate limit cause.
// Reference: https://docs.bigmodel.cn/cn/faq/api-code
const (
	// Transient rate limits — resolve in seconds to minutes.
	ZAIErrConcurrentLimit = "1302" // 并发数过高 — concurrent requests too high
	ZAIErrFrequencyLimit  = "1303" // 频率过高 — request frequency too high
	ZAIErrDailyCallLimit  = "1304" // 今日调用次数限额 — daily call limit reached
	ZAIErrTrafficLimit    = "1305" // 触发流量限制 — traffic limit triggered
	ZAIErrUsageLimit      = "1308" // 使用上限，限额将在 next_flush_time 重置 — usage limit with reset
	ZAIErrModelOverloaded = "1312" // 模型当前访问量过大 — model traffic too high

	// Subscription/quota limits — resolve on reset or after manual action.
	ZAIErrPlanExpired       = "1309" // GLM Coding Plan 套餐已到期 — plan expired
	ZAIErrPeriodicLimit     = "1310" // 每周/每月使用上限 — weekly/monthly limit
	ZAIErrModelNoPermission = "1311" // 订阅套餐暂未开放模型权限 — no model permission
	ZAIErrFairUseViolation  = "1313" // 不符合公平使用策略 — fair use policy violation

	// Account issues — permanent, require manual intervention.
	ZAIErrAccountLocked    = "1112" // 账户已被锁定 — account locked
	ZAIErrAccountArrears   = "1113" // 账户已欠费 — account in arrears
	ZAIErrAccountViolation = "1121" // 账户存违规行为 — account violation

	// Retryable 400 — ZAI returns 400 with this business error code for transient issues
	// that warrant failover to another provider.
	ZAIErrRetryable400 = "1234"
)

// ZAIErrorCategory classifies the severity and recoverability of a ZAI error.
type ZAIErrorCategory int

const (
	// ZAICatTransient indicates a temporary rate limit that resolves in seconds to minutes.
	ZAICatTransient ZAIErrorCategory = iota

	// ZAICatQuotaExhausted indicates a usage quota has been exhausted with a known reset time.
	ZAICatQuotaExhausted

	// ZAICatPermanent indicates an issue that requires manual intervention (account/plan issue).
	ZAICatPermanent
)

// String returns a human-readable category name for logging.
func (c ZAIErrorCategory) String() string {
	switch c {
	case ZAICatTransient:
		return "transient"
	case ZAICatQuotaExhausted:
		return "quota_exhausted"
	case ZAICatPermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// ZAIErrorInfo contains parsed information from a ZAI 429 error response.
type ZAIErrorInfo struct {
	Code        string
	Message     string
	Category    ZAIErrorCategory
	Cooldown    time.Duration
	IsPermanent bool
	// ResetTime is the parsed next_flush_time from the error message (1308/1310).
	// Zero value if not available or not applicable.
	ResetTime time.Time
}

// Description returns a human-readable description for the error, suitable for
// passing back to the client.
func (info *ZAIErrorInfo) Description() string {
	switch info.Code {
	case ZAIErrConcurrentLimit:
		return "ZAI: Too many concurrent requests. Please reduce parallelism."
	case ZAIErrFrequencyLimit:
		return "ZAI: Request frequency too high. Please slow down."
	case ZAIErrDailyCallLimit:
		return "ZAI: Daily API call limit reached. Limit resets at midnight."
	case ZAIErrTrafficLimit:
		return "ZAI: Traffic limit triggered. Please retry in a few minutes."
	case ZAIErrUsageLimit:
		return "ZAI: Usage limit reached. " + info.Message
	case ZAIErrModelOverloaded:
		return "ZAI: Model is currently overloaded. Please retry later or switch models."
	case ZAIErrPlanExpired:
		return "ZAI: GLM Coding Plan has expired. Please renew at https://bigmodel.cn/claude-code"
	case ZAIErrPeriodicLimit:
		return "ZAI: Weekly/monthly usage limit reached. " + info.Message
	case ZAIErrModelNoPermission:
		return "ZAI: Current subscription does not include this model. " + info.Message
	case ZAIErrFairUseViolation:
		return "ZAI: Fair use policy violation. Request frequency has been limited. " +
			"Please apply for restriction removal in your account settings " +
			"(个人中心 → 编程套餐总览)."
	case ZAIErrAccountLocked:
		return "ZAI: Account has been locked. Please contact support."
	case ZAIErrAccountArrears:
		return "ZAI: Account is in arrears. Please add funds to continue."
	case ZAIErrAccountViolation:
		return "ZAI: Account has policy violations and has been locked. " +
			"Please contact service@zhipuai.cn."
	default:
		if info.Message != "" {
			return "ZAI: " + info.Message
		}
		return "ZAI: Rate limit reached."
	}
}

// zaiErrorResponse matches the Zhipu error response format:
//
//	{"error":{"code":"1302","message":"Rate limit reached for requests"}}
type zaiErrorResponse struct {
	Error zaiErrorDetail `json:"error"`
}

type zaiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ParseZAI429Error reads the response body and attempts to parse a ZAI error code.
// Returns:
//   - (*ZAIErrorInfo, bodyBytes, nil) on successful parse
//   - (nil, bodyBytes, nil) when body is not a recognizable ZAI error format
//   - (nil, nil, err) on read failure
//
// The caller must restore resp.Body using the returned bodyBytes.
func ParseZAI429Error(body io.Reader) (*ZAIErrorInfo, []byte, error) {
	info, bodyBytes, err := ParseZAIError(body)
	if err != nil || info == nil {
		return info, bodyBytes, err
	}

	// Apply 429-specific post-processing
	info.Category = classifyZAIError(info.Code)
	info.IsPermanent = info.Category == ZAICatPermanent
	info.Cooldown = suggestedCooldown(info.Code)

	// For usage-limit errors (1308), try to extract next_flush_time from the
	// message text for a precise cooldown instead of the fixed default.
	if info.Code == ZAIErrUsageLimit {
		if resetTime := parseResetTimeFromMessage(info.Message); !resetTime.IsZero() {
			cooldown := time.Until(resetTime)
			if cooldown > 0 {
				info.Cooldown = cooldown
				info.ResetTime = resetTime
			}
		}
	}

	return info, bodyBytes, nil
}

// ParseZAIError reads the response body and parses a ZAI error code from JSON.
// This is a general-purpose parser that works for any HTTP status code.
// Returns:
//   - (*ZAIErrorInfo, bodyBytes, nil) on successful parse
//   - (nil, bodyBytes, nil) when body is not a recognizable ZAI error format
//   - (nil, nil, err) on read failure
//
// The caller must restore resp.Body using the returned bodyBytes.
func ParseZAIError(body io.Reader) (*ZAIErrorInfo, []byte, error) {
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, err
	}

	var resp zaiErrorResponse
	if err := json.Unmarshal(bodyBytes, &resp); err != nil {
		return nil, bodyBytes, nil // not valid JSON
	}

	if resp.Error.Code == "" {
		return nil, bodyBytes, nil // no business error code
	}

	info := &ZAIErrorInfo{
		Code:    resp.Error.Code,
		Message: resp.Error.Message,
	}

	return info, bodyBytes, nil
}

// IsZAIProvider checks if a provider owner string identifies a ZAI (Zhipu) provider.
func IsZAIProvider(owner string) bool {
	return owner == ZAIOwner
}

// IsZAIRetryable400 returns true if the ZAI business error code indicates a retryable
// condition that warrants failover to another provider, despite the 400 HTTP status code.
func IsZAIRetryable400(code string) bool {
	return code == ZAIErrRetryable400
}

func classifyZAIError(code string) ZAIErrorCategory {
	switch code {
	case ZAIErrConcurrentLimit, ZAIErrFrequencyLimit, ZAIErrTrafficLimit, ZAIErrModelOverloaded:
		return ZAICatTransient
	case ZAIErrDailyCallLimit, ZAIErrUsageLimit, ZAIErrPeriodicLimit:
		return ZAICatQuotaExhausted
	case ZAIErrPlanExpired, ZAIErrModelNoPermission, ZAIErrFairUseViolation,
		ZAIErrAccountLocked, ZAIErrAccountArrears, ZAIErrAccountViolation:
		return ZAICatPermanent
	default:
		return ZAICatTransient // unknown codes treated as transient (safe default)
	}
}

func suggestedCooldown(code string) time.Duration {
	switch code {
	case ZAIErrConcurrentLimit:
		return 30 * time.Second
	case ZAIErrFrequencyLimit:
		return 60 * time.Second
	case ZAIErrDailyCallLimit:
		return 1 * time.Hour // cap at 1h, will retry hourly
	case ZAIErrTrafficLimit, ZAIErrModelOverloaded:
		return 2 * time.Minute
	case ZAIErrUsageLimit:
		return 5 * time.Hour // fallback; overridden by parsed next_flush_time
	case ZAIErrPeriodicLimit:
		return 1 * time.Hour // cap at 1h, will retry hourly
	case ZAIErrPlanExpired, ZAIErrModelNoPermission, ZAIErrFairUseViolation,
		ZAIErrAccountLocked, ZAIErrAccountArrears, ZAIErrAccountViolation:
		return 0 // permanent — no cooldown helps
	default:
		return 60 * time.Second
	}
}

// zaiDatetimeRe matches datetime patterns in ZAI error messages.
// Matches: 2026-04-14 18:00:00, 2026-04-14T18:00:00, 2026-04-14 18:00
var zaiDatetimeRe = regexp.MustCompile(`\d{4}-\d{1,2}-\d{1,2}[\sT]\d{1,2}:\d{2}(?::\d{2})?`)

// zaiTimeFormats lists datetime layouts tried when parsing ZAI messages.
// Ordered from most specific to least.
var zaiTimeFormats = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
}

// parseResetTimeFromMessage extracts a reset/datetime from a ZAI error message
// and returns it in the local timezone. Returns zero time if no datetime is found.
//
// Typical message:
//
//	"已达到 100 次 的使用上限。您的限额将在 2026-04-14 18:00:00 重置"
func parseResetTimeFromMessage(msg string) time.Time {
	match := zaiDatetimeRe.FindString(msg)
	if match == "" {
		return time.Time{}
	}

	local := time.Local
	for _, layout := range zaiTimeFormats {
		if t, err := time.ParseInLocation(layout, match, local); err == nil {
			return t
		}
	}

	return time.Time{}
}
