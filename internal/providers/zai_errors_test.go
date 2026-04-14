package providers

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseZAI429Error_TransientCodes(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		code     string
		category ZAIErrorCategory
		cooldown time.Duration
	}{
		{
			name:     "concurrent limit 1302",
			body:     `{"error":{"code":"1302","message":"Too many concurrent requests"}}`,
			code:     ZAIErrConcurrentLimit,
			category: ZAICatTransient,
			cooldown: 30 * time.Second,
		},
		{
			name:     "frequency limit 1303",
			body:     `{"error":{"code":"1303","message":"Frequency too high"}}`,
			code:     ZAIErrFrequencyLimit,
			category: ZAICatTransient,
			cooldown: 60 * time.Second,
		},
		{
			name:     "traffic limit 1305",
			body:     `{"error":{"code":"1305","message":"Traffic limit triggered"}}`,
			code:     ZAIErrTrafficLimit,
			category: ZAICatTransient,
			cooldown: 2 * time.Minute,
		},
		{
			name:     "model overloaded 1312",
			body:     `{"error":{"code":"1312","message":"Model overloaded"}}`,
			code:     ZAIErrModelOverloaded,
			category: ZAICatTransient,
			cooldown: 2 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, bodyBytes, err := ParseZAI429Error(strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info == nil {
				t.Fatal("expected non-nil info")
			}
			if info.Code != tt.code {
				t.Errorf("Code = %q, want %q", info.Code, tt.code)
			}
			if info.Category != tt.category {
				t.Errorf("Category = %v, want %v", info.Category, tt.category)
			}
			if info.Cooldown != tt.cooldown {
				t.Errorf("Cooldown = %v, want %v", info.Cooldown, tt.cooldown)
			}
			if info.IsPermanent {
				t.Error("IsPermanent should be false for transient errors")
			}
			if string(bodyBytes) != tt.body {
				t.Errorf("bodyBytes = %q, want %q", string(bodyBytes), tt.body)
			}
		})
	}
}

func TestParseZAI429Error_QuotaExhaustedCodes(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		code     string
		cooldown time.Duration
	}{
		{
			name:     "daily limit 1304",
			body:     `{"error":{"code":"1304","message":"Daily call limit reached"}}`,
			code:     ZAIErrDailyCallLimit,
			cooldown: 1 * time.Hour,
		},
		{
			name:     "usage limit 1308 without reset time",
			body:     `{"error":{"code":"1308","message":"Usage limit, no time info"}}`,
			code:     ZAIErrUsageLimit,
			cooldown: 5 * time.Hour, // fallback when no parseable reset time
		},
		{
			name:     "periodic limit 1310",
			body:     `{"error":{"code":"1310","message":"Weekly/monthly limit"}}`,
			code:     ZAIErrPeriodicLimit,
			cooldown: 1 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, _, err := ParseZAI429Error(strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info == nil {
				t.Fatal("expected non-nil info")
			}
			if info.Category != ZAICatQuotaExhausted {
				t.Errorf("Category = %v, want ZAICatQuotaExhausted", info.Category)
			}
			if info.Cooldown != tt.cooldown {
				t.Errorf("Cooldown = %v, want %v", info.Cooldown, tt.cooldown)
			}
			if info.IsPermanent {
				t.Error("IsPermanent should be false for quota errors")
			}
		})
	}
}

func TestParseZAI429Error_PermanentCodes(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{
			name: "plan expired 1309",
			body: `{"error":{"code":"1309","message":"GLM Coding Plan expired"}}`,
			code: ZAIErrPlanExpired,
		},
		{
			name: "no model permission 1311",
			body: `{"error":{"code":"1311","message":"No model permission"}}`,
			code: ZAIErrModelNoPermission,
		},
		{
			name: "fair use violation 1313",
			body: `{"error":{"code":"1313","message":"Fair use violation"}}`,
			code: ZAIErrFairUseViolation,
		},
		{
			name: "account locked 1112",
			body: `{"error":{"code":"1112","message":"Account locked"}}`,
			code: ZAIErrAccountLocked,
		},
		{
			name: "account arrears 1113",
			body: `{"error":{"code":"1113","message":"Account in arrears"}}`,
			code: ZAIErrAccountArrears,
		},
		{
			name: "account violation 1121",
			body: `{"error":{"code":"1121","message":"Account violation"}}`,
			code: ZAIErrAccountViolation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, _, err := ParseZAI429Error(strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info == nil {
				t.Fatal("expected non-nil info")
			}
			if info.Category != ZAICatPermanent {
				t.Errorf("Category = %v, want ZAICatPermanent", info.Category)
			}
			if !info.IsPermanent {
				t.Error("IsPermanent should be true")
			}
			if info.Cooldown != 0 {
				t.Errorf("Cooldown = %v, want 0 for permanent errors", info.Cooldown)
			}
		})
	}
}

func TestParseZAI429Error_UnknownCode_TreatedAsTransient(t *testing.T) {
	body := `{"error":{"code":"9999","message":"Unknown error"}}`
	info, _, err := ParseZAI429Error(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info for unknown codes")
	}
	if info.Category != ZAICatTransient {
		t.Errorf("Category = %v, want ZAICatTransient for unknown codes", info.Category)
	}
	if info.Cooldown != 60*time.Second {
		t.Errorf("Cooldown = %v, want 60s default for unknown codes", info.Cooldown)
	}
}

func TestParseZAI429Error_NonZAIFormat(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"anthropic format", `{"type":"error","error":{"type":"rate_limit_error","message":"Too many requests"}}`},
		{"no code field", `{"error":{"message":"Rate limit"}}`},
		{"empty body", ``},
		{"not json", `plain text error`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, bodyBytes, err := ParseZAI429Error(strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info != nil {
				t.Error("expected nil info for non-ZAI format")
			}
			if string(bodyBytes) != tt.body {
				t.Errorf("bodyBytes = %q, want %q", string(bodyBytes), tt.body)
			}
		})
	}
}

func TestParseZAI429Error_ReadError(t *testing.T) {
	reader := &errorReader{err: errors.New("read failure")}
	info, bodyBytes, err := ParseZAI429Error(reader)
	if err == nil {
		t.Fatal("expected error from reader")
	}
	if info != nil {
		t.Error("expected nil info on read error")
	}
	if bodyBytes != nil {
		t.Error("expected nil bodyBytes on read error")
	}
}

type errorReader struct{ err error }

func (r *errorReader) Read(_ []byte) (int, error) { return 0, r.err }

func TestParseZAI429Error_BodyRestoration(t *testing.T) {
	original := `{"error":{"code":"1302","message":"Rate limit"}}`
	info, bodyBytes, err := ParseZAI429Error(strings.NewReader(original))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	// Verify body can be re-read from returned bytes
	restored := string(bodyBytes)
	if restored != original {
		t.Errorf("restored body = %q, want %q", restored, original)
	}
}

func TestIsZAIProvider(t *testing.T) {
	if !IsZAIProvider(ZAIOwner) {
		t.Errorf("IsZAIProvider(%q) = false, want true", ZAIOwner)
	}
	if IsZAIProvider("anthropic") {
		t.Error("IsZAIProvider(\"anthropic\") = true, want false")
	}
	if IsZAIProvider("") {
		t.Error("IsZAIProvider(\"\") = true, want false")
	}
}

func TestZAIErrorCategory_String(t *testing.T) {
	tests := []struct {
		cat  ZAIErrorCategory
		want string
	}{
		{ZAICatTransient, "transient"},
		{ZAICatQuotaExhausted, "quota_exhausted"},
		{ZAICatPermanent, "permanent"},
		{ZAIErrorCategory(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.cat.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestZAIErrorInfo_Description(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		{"concurrent", ZAIErrConcurrentLimit, "ZAI: Too many concurrent requests"},
		{"frequency", ZAIErrFrequencyLimit, "ZAI: Request frequency too high"},
		{"daily", ZAIErrDailyCallLimit, "ZAI: Daily API call limit reached"},
		{"traffic", ZAIErrTrafficLimit, "ZAI: Traffic limit triggered"},
		{"overloaded", ZAIErrModelOverloaded, "ZAI: Model is currently overloaded"},
		{"plan expired", ZAIErrPlanExpired, "ZAI: GLM Coding Plan has expired"},
		{"account locked", ZAIErrAccountLocked, "ZAI: Account has been locked"},
		{"arrears", ZAIErrAccountArrears, "ZAI: Account is in arrears"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &ZAIErrorInfo{Code: tt.code, Message: "test"}
			desc := info.Description()
			if !bytes.Contains([]byte(desc), []byte(tt.want)) {
				t.Errorf("Description() = %q, want to contain %q", desc, tt.want)
			}
		})
	}
}

func TestParseZAI429Error_UsageLimitWithResetTime(t *testing.T) {
	// Use a time far in the future so the parsed cooldown is > 5h fallback
	body := `{"error":{"code":"1308","message":"已达到 100 次 的使用上限。您的限额将在 2099-06-01 12:00:00 重置"}}`
	info, _, err := ParseZAI429Error(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.Code != "1308" {
		t.Errorf("Code = %q, want \"1308\"", info.Code)
	}
	if info.Category != ZAICatQuotaExhausted {
		t.Errorf("Category = %v, want ZAICatQuotaExhausted", info.Category)
	}
	// Cooldown should be based on parsed reset time, NOT the 5h fallback
	if info.Cooldown <= 5*time.Hour {
		t.Errorf("Cooldown = %v, should be > 5h (parsed from reset time)", info.Cooldown)
	}
	if info.ResetTime.IsZero() {
		t.Error("ResetTime should be set from message")
	}
	// Description should include the original message with reset time
	desc := info.Description()
	if !strings.Contains(desc, "2099-06-01 12:00:00") {
		t.Errorf("Description() = %q, should contain reset time from message", desc)
	}
}

func TestParseZAI429Error_UsageLimitResetTimeInPast_FallsBackToDefault(t *testing.T) {
	// Reset time is in the past → cooldown would be negative → fall back to 5h
	body := `{"error":{"code":"1308","message":"已达到 100 次 的使用上限。您的限额将在 2020-01-01 00:00:00 重置"}}`
	info, _, err := ParseZAI429Error(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.Cooldown != 5*time.Hour {
		t.Errorf("Cooldown = %v, want 5h fallback when reset time is in the past", info.Cooldown)
	}
	if !info.ResetTime.IsZero() {
		t.Error("ResetTime should be zero when parsed time is in the past")
	}
}

func TestParseZAI429Error_UsageLimitNoDatetime_FallsBackToDefault(t *testing.T) {
	body := `{"error":{"code":"1308","message":"Usage limit reached"}}`
	info, _, err := ParseZAI429Error(strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil info")
	}
	if info.Cooldown != 5*time.Hour {
		t.Errorf("Cooldown = %v, want 5h fallback when no datetime in message", info.Cooldown)
	}
}

func TestParseResetTimeFromMessage(t *testing.T) {
	tests := []struct {
		name      string
		message   string
		wantMatch bool
	}{
		{
			name:      "Chinese message with datetime",
			message:   "已达到 100 次 的使用上限。您的限额将在 2026-04-14 18:00:00 重置",
			wantMatch: true,
		},
		{
			name:      "ISO 8601 format",
			message:   "Usage limit. Reset at 2026-04-14T18:00:00",
			wantMatch: true,
		},
		{
			name:      "short time format HH:MM",
			message:   "Resets at 2026-04-14 18:00",
			wantMatch: true,
		},
		{
			name:      "no datetime",
			message:   "Usage limit reached without time info",
			wantMatch: false,
		},
		{
			name:      "empty message",
			message:   "",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseResetTimeFromMessage(tt.message)
			if tt.wantMatch && got.IsZero() {
				t.Error("expected non-zero time, got zero")
			}
			if !tt.wantMatch && !got.IsZero() {
				t.Errorf("expected zero time, got %v", got)
			}
		})
	}
}
