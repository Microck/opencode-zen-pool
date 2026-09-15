package main

import "testing"

func TestYAML(t *testing.T) {
	raw := []byte("defaults: &p {proxy-url: 'http://u:p@localhost:1234'}\nopenai-compatibility:\n  - name: opencode-zen\n    base-url: https://opencode.ai/zen/v1\n    api-key-entries:\n      - <<: *p\n        api-key: \"a # b\"\n")
	m, err := yamlMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	providers := m["openai-compatibility"].([]any)
	p := providers[0].(map[string]any)
	a := p["api-key-entries"].([]any)[0].(map[string]any)
	if a["api-key"] != "a # b" || a["proxy-url"] != "http://u:p@localhost:1234" {
		t.Fatal("YAML scalars or merge corrupted")
	}
}
func TestYAMLRejectsInvalidDocuments(t *testing.T) {
	for _, raw := range []string{"key: a\nkey: b\n", "a: [\n", "a: &a {b: *a}\n", "a: value\n---\nb: value\n"} {
		if _, err := yamlMap([]byte(raw)); err == nil {
			t.Fatal("invalid YAML accepted")
		}
	}
}
