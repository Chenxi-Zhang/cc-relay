package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestResponsesConfigProviderNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "omitted uses all providers",
			yaml: "responses:\n  enable_responses_api: true\n",
			want: nil,
		},
		{
			name: "empty uses all providers",
			yaml: "responses:\n  providers: []\n",
			want: []string{},
		},
		{
			name: "single provider",
			yaml: "responses:\n  providers:\n    - openai-main\n",
			want: []string{"openai-main"},
		},
		{
			name: "multiple providers",
			yaml: "responses:\n  providers:\n    - openai-main\n    - openai-eu\n",
			want: []string{"openai-main", "openai-eu"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg Config
			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &cfg))
			assert.Equal(t, tt.want, cfg.Responses.ProviderNames)
		})
	}
}
