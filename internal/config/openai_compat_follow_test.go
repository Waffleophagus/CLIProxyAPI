package config

import (
	"strings"
	"testing"
	"time"
)

func TestOpenAICompatibilityFollowConfig(t *testing.T) {
	valid := `api-keys:
  openai-compatibility:
    - name: dgx-spark
      base-url: http://spark.local:8000/v1
      follow-interval: 2s
      keys: []
      models:
        - alias: DGX-SPARK
          follow: true
          match: "GLM-*"
        - alias: DGX-PINNED
          follow: true
          name: GLM-5.3-Flash-EXL3
`
	if err := ValidateV8Config([]byte(valid)); err != nil {
		t.Fatalf("ValidateV8Config(valid) = %v", err)
	}
	cfg, err := ParseConfigBytes([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	compat := cfg.OpenAICompatibility[0]
	if !compat.Models[0].Follow || compat.Models[0].Match != "GLM-*" || !compat.Models[0].IsFollowing() {
		t.Fatalf("follow entry = %+v", compat.Models[0])
	}
	if compat.Models[1].IsFollowing() || !compat.HasFollowModels() {
		t.Fatalf("pinned entry must not poll: %+v", compat.Models[1])
	}
	if got := compat.FollowPollInterval(); got != MinFollowInterval {
		t.Fatalf("FollowPollInterval = %s, want clamp to %s", got, MinFollowInterval)
	}
	if got := (&OpenAICompatibility{}).FollowPollInterval(); got != 30*time.Second {
		t.Fatalf("default FollowPollInterval = %s", got)
	}

	for name, tc := range map[string]struct{ yaml, want string }{
		"follow without alias": {
			yaml: "api-keys:\n  openai-compatibility:\n    - name: g\n      base-url: http://x/v1\n      keys: []\n      models:\n        - follow: true\n",
			want: "requires alias",
		},
		"match without follow": {
			yaml: "api-keys:\n  openai-compatibility:\n    - name: g\n      base-url: http://x/v1\n      keys: []\n      models:\n        - name: a\n          alias: b\n          match: \"a*\"\n",
			want: "match requires follow",
		},
		"bad interval": {
			yaml: "api-keys:\n  openai-compatibility:\n    - name: g\n      base-url: http://x/v1\n      follow-interval: soon\n      keys: []\n      models:\n        - alias: b\n          follow: true\n",
			want: "invalid follow-interval",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if errValidate := ValidateV8Config([]byte(tc.yaml)); errValidate == nil || !strings.Contains(errValidate.Error(), tc.want) {
				t.Fatalf("ValidateV8Config = %v, want %q", errValidate, tc.want)
			}
			if _, errParse := ParseConfigBytes([]byte(tc.yaml)); errParse == nil || !strings.Contains(errParse.Error(), tc.want) {
				t.Fatalf("ParseConfigBytes = %v, want %q", errParse, tc.want)
			}
		})
	}
}
