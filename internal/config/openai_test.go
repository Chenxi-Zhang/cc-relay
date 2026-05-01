package config_test

import (
	"strings"
	"testing"

	"github.com/omarluq/cc-relay/internal/config"
)

// --- YAML parsing tests ---

func TestLoadOpenAIProvidersFromYAML(t *testing.T) {
	t.Parallel()

	yamlContent := `server:
  listen: "` + defaultListenAddr + `"

openai_providers:
  - name: "openai-main"
    type: "openai"
    base_url: "https://api.openai.com/v1"
    enabled: true
    keys:
      - key: "sk-openai-test"
        rpm_limit: 60

logging:
  level: "info"
  format: "json"
`

	cfg, err := config.LoadFromReader(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("config.LoadFromReader failed: %v", err)
	}

	if len(cfg.OpenAIProviders) != 1 {
		t.Fatalf("Expected 1 openai_provider, got %d", len(cfg.OpenAIProviders))
	}

	p := cfg.OpenAIProviders[0]
	if p.Name != "openai-main" {
		t.Errorf("Expected name=openai-main, got %s", p.Name)
	}
	if p.Type != "openai" {
		t.Errorf("Expected type=openai, got %s", p.Type)
	}
	if p.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("Expected base_url=https://api.openai.com/v1, got %s", p.BaseURL)
	}
	if !p.Enabled {
		t.Error("Expected enabled=true")
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-openai-test" {
		t.Errorf("Unexpected keys: %+v", p.Keys)
	}
}

func TestLoadMultipleOpenAIProvidersFromYAML(t *testing.T) {
	t.Parallel()

	yamlContent := `server:
  listen: "` + defaultListenAddr + `"

openai_providers:
  - name: "openai-us"
    type: "openai"
    base_url: "https://api.openai.com/v1"
    enabled: true
    keys:
      - key: "sk-key1"
  - name: "openai-eu"
    type: "openai"
    base_url: "https://eu.api.openai.com/v1"
    enabled: true
    keys:
      - key: "sk-key2"

logging:
  level: "info"
`

	cfg, err := config.LoadFromReader(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("config.LoadFromReader failed: %v", err)
	}

	if len(cfg.OpenAIProviders) != 2 {
		t.Fatalf("Expected 2 openai_providers, got %d", len(cfg.OpenAIProviders))
	}
	if cfg.OpenAIProviders[0].Name != "openai-us" {
		t.Errorf("Expected first provider name=openai-us, got %s", cfg.OpenAIProviders[0].Name)
	}
	if cfg.OpenAIProviders[1].Name != "openai-eu" {
		t.Errorf("Expected second provider name=openai-eu, got %s", cfg.OpenAIProviders[1].Name)
	}
}

func TestLoadOpenAIProvidersEmpty(t *testing.T) {
	t.Parallel()

	yamlContent := `server:
  listen: "` + defaultListenAddr + `"

logging:
  level: "info"
`

	cfg, err := config.LoadFromReader(strings.NewReader(yamlContent))
	if err != nil {
		t.Fatalf("config.LoadFromReader failed: %v", err)
	}

	if len(cfg.OpenAIProviders) != 0 {
		t.Errorf("Expected 0 openai_providers, got %d", len(cfg.OpenAIProviders))
	}
}

// --- Validation tests ---

func TestValidateOpenAITypeIsValid(t *testing.T) {
	t.Parallel()

	cfg := configWithListen(defaultListenAddr)

	prov := config.MakeTestProviderConfig()
	prov.Name = "openai-test"
	prov.Type = "openai"
	prov.BaseURL = "https://api.openai.com/v1"
	prov.Keys = []config.KeyConfig{config.MakeTestKeyConfig("sk-test")}

	cfg.OpenAIProviders = []config.ProviderConfig{prov}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Expected valid config with openai provider, got error: %v", err)
	}
}

func TestValidateOpenAIProviderMissingBaseURL(t *testing.T) {
	t.Parallel()

	cfg := configWithListen(defaultListenAddr)

	prov := config.MakeTestProviderConfig()
	prov.Name = "openai-no-url"
	prov.Type = "openai"
	prov.BaseURL = "" // missing
	prov.Keys = []config.KeyConfig{config.MakeTestKeyConfig("sk-test")}

	cfg.OpenAIProviders = []config.ProviderConfig{prov}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for openai provider missing base_url")
	}

	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("Expected 'base_url' in error, got: %v", err)
	}
}

func TestValidateOpenAIProviderDuplicateNames(t *testing.T) {
	t.Parallel()

	cfg := configWithListen(defaultListenAddr)

	prov1 := config.MakeTestProviderConfig()
	prov1.Name = "openai-dup"
	prov1.Type = "openai"
	prov1.BaseURL = "https://api.openai.com/v1"
	prov1.Keys = []config.KeyConfig{config.MakeTestKeyConfig("key1")}

	prov2 := config.MakeTestProviderConfig()
	prov2.Name = "openai-dup"
	prov2.Type = "openai"
	prov2.BaseURL = "https://api.openai.com/v1"
	prov2.Keys = []config.KeyConfig{config.MakeTestKeyConfig("key2")}

	cfg.OpenAIProviders = []config.ProviderConfig{prov1, prov2}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for duplicate openai provider names")
	}

	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Expected 'duplicate' in error, got: %v", err)
	}
}

func TestValidateOpenAIProviderMissingName(t *testing.T) {
	t.Parallel()

	cfg := configWithListen(defaultListenAddr)

	prov := config.MakeTestProviderConfig()
	prov.Name = ""
	prov.Type = "openai"
	prov.BaseURL = "https://api.openai.com/v1"

	cfg.OpenAIProviders = []config.ProviderConfig{prov}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for missing openai provider name")
	}

	if !strings.Contains(err.Error(), "name") {
		t.Errorf("Expected 'name' in error, got: %v", err)
	}
}

func TestValidateOpenAIProviderInvalidType(t *testing.T) {
	t.Parallel()

	cfg := configWithListen(defaultListenAddr)

	prov := config.MakeTestProviderConfig()
	prov.Name = "openai-bad"
	prov.Type = "invalid-type"
	prov.BaseURL = "https://api.openai.com/v1"

	cfg.OpenAIProviders = []config.ProviderConfig{prov}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Expected error for invalid openai provider type")
	}

	if !strings.Contains(err.Error(), "type") {
		t.Errorf("Expected 'type' in error, got: %v", err)
	}
}

func TestValidateOpenAIProviderWithValidBaseURL(t *testing.T) {
	t.Parallel()

	// Ensure a non-empty base_url passes validation for openai type
	cfg := configWithListen(defaultListenAddr)

	prov := config.MakeTestProviderConfig()
	prov.Name = "openai-with-url"
	prov.Type = "openai"
	prov.BaseURL = "https://custom.openai.proxy/v1"
	prov.Keys = []config.KeyConfig{config.MakeTestKeyConfig("sk-test")}

	cfg.OpenAIProviders = []config.ProviderConfig{prov}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Expected valid config, got error: %v", err)
	}
}

func TestValidateOpenAIProvidersAndProvidersCoexist(t *testing.T) {
	t.Parallel()

	// Both Providers and OpenAIProviders should validate independently
	cfg := configWithListen(defaultListenAddr)

	// Regular provider
	prov1 := config.MakeTestProviderConfig()
	prov1.Name = "anthropic-main"
	prov1.Type = "anthropic"
	prov1.Keys = []config.KeyConfig{config.MakeTestKeyConfig("key1")}

	// OpenAI provider
	prov2 := config.MakeTestProviderConfig()
	prov2.Name = "openai-main"
	prov2.Type = "openai"
	prov2.BaseURL = "https://api.openai.com/v1"
	prov2.Keys = []config.KeyConfig{config.MakeTestKeyConfig("key2")}

	cfg.Providers = []config.ProviderConfig{prov1}
	cfg.OpenAIProviders = []config.ProviderConfig{prov2}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Expected valid config with both providers and openai_providers, got error: %v", err)
	}
}
