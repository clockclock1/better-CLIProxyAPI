package api

import (
	"strings"
	"testing"
)

func TestPatchManagementHTMLForCommandCode(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"{kind:`builtin`,id:`devin`,label:`Devin`,titleKey:`auth_login.devin_oauth_title`,icon:{light:iS,dark:aS},flow:`browser`}",
		"RN=new Set([`codex`,`anthropic`,`antigravity`,`xai`,`devin`])",
		"$y=new Set([`codex`,`claude`,`antigravity`,`xai`,`devin`])",
		"{id:`devin`,label:`Devin`,patterns:[]}",
		"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`Start Devin Login`",
	}, ""))

	out := string(patchManagementHTMLForCommandCode(raw))
	for _, want := range []string{
		"id:`commandcode`",
		"label:`Command Code`",
		"[`codex`,`anthropic`,`antigravity`,`xai`,`devin`,`commandcode`]",
		"[`codex`,`claude`,`antigravity`,`xai`,`devin`,`commandcode`]",
		"commandcode_oauth_title",
		"commandcode_oauth_button:`Start Command Code Login`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("patched HTML missing %q\n%s", want, out)
		}
	}

	again := string(patchManagementHTMLForCommandCode([]byte(out)))
	if again != out {
		t.Fatal("patch should be idempotent")
	}
}
