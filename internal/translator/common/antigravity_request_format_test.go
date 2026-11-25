package common

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestNormalizeAntigravityRequestDocAt(t *testing.T) {
	NormalizeAntigravityRequestDoc(nil)
	NormalizeAntigravityRequestDocAt(nil, "")

	const input = `{"systemInstruction":{"role":"system","parts":[{"text":"first"},{"text":"second"}]},"contents":[{"parts":[{"functionResponse":{"response":{"result":"done"}}},{"function_response":{"response":{"result":"legacy"}}},{"functionResponse":{"response":{"result":"structured","output":"preserved"}}}]}]}`
	for _, prefix := range []string{"", "request."} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			raw := input
			if prefix != "" {
				raw = `{"request":` + input + `}`
			}
			doc := ojson.NewDocString(raw)
			if prefix == "" {
				NormalizeAntigravityRequestDocAt(doc, prefix)
			} else {
				NormalizeAntigravityRequestDoc(doc)
			}
			if got := doc.Get(prefix + "systemInstruction.role").String(); got != "user" {
				t.Fatalf("system role = %q, want user", got)
			}
			if got := doc.Get(prefix + "systemInstruction.parts.#").Int(); got != 1 {
				t.Fatalf("system parts = %d, want 1", got)
			}
			if got := doc.Get(prefix + "systemInstruction.parts.0.text").String(); got != "first\n\nsecond" {
				t.Fatalf("system text = %q", got)
			}
			for i, field := range []string{"functionResponse", "function_response"} {
				part := doc.Get(prefix + "contents.0.parts").Array()[i]
				response := part.Get(field + ".response")
				if response.Get("result").Exists() || response.Get("output").String() != []string{"done", "legacy"}[i] {
					t.Fatalf("tool response was not normalized: %s", response.Raw)
				}
			}
			response := doc.Get(prefix + "contents.0.parts.2.functionResponse.response")
			if response.Get("result").String() != "structured" || response.Get("output").String() != "preserved" {
				t.Fatalf("structured response changed: %s", response.Raw)
			}
			first := doc.String()
			NormalizeAntigravityRequestDocAt(doc, prefix)
			if doc.String() != first {
				t.Fatal("normalization must be idempotent")
			}
		})
	}
}
