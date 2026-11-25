package gemini

import (
	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

// NormalizeAntigravityRequestDoc applies the native Antigravity wire format.
func NormalizeAntigravityRequestDoc(doc *ojson.Doc) {
	translatorcommon.NormalizeAntigravityRequestDoc(doc)
}

func normalizeAntigravityRequestDocAt(doc *ojson.Doc, prefix string) {
	translatorcommon.NormalizeAntigravityRequestDocAt(doc, prefix)
}
