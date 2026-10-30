package api

import (
	"strings"
	"testing"
)

func TestPatchManagementHTMLForCommandCode(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "legacy icons",
			raw: strings.Join([]string{
				"{kind:`builtin`,id:`devin`,label:`Devin`,titleKey:`auth_login.devin_oauth_title`,icon:{light:iS,dark:aS},flow:`browser`}]",
				"new Set([`codex`,`anthropic`,`antigravity`,`xai`,`devin`])",
				"new Set([`codex`,`claude`,`antigravity`,`xai`,`devin`])",
				"{id:`devin`,label:`Devin`,patterns:[]}",
				"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`Start Devin Login`",
			}, ""),
		},
		{
			name: "current CPAMC icons",
			raw: strings.Join([]string{
				"{kind:`builtin`,id:`xai`,label:`xAI`,titleKey:`auth_login.xai_oauth_title`,icon:{light:Ax,dark:jx},flow:`browser`},{kind:`builtin`,id:`devin`,label:`Devin`,titleKey:`auth_login.devin_oauth_title`,icon:{light:xx,dark:Sx},flow:`browser`}]",
				"are=new Set([`codex`,`anthropic`,`antigravity`,`xai`,`devin`])",
				"$y=new Set([`codex`,`claude`,`antigravity`,`xai`,`devin`])",
				"{id:`devin`,label:`Devin`,patterns:[]}",
				"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`Start Devin Login`",
			}, ""),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(patchManagementHTMLForCommandCode([]byte(tc.raw)))
			for _, want := range []string{
				"kind:`builtin`,id:`commandcode`",
				"label:`Command Code`",
				"titleKey:`auth_login.commandcode_oauth_title`",
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
		})
	}
}

func TestPatchManagementHTMLAgainstLiveSnippet(t *testing.T) {
	// Exact live CPAMC fragment observed on 186.241.81.108 (v0.1.1 panel asset).
	raw := []byte(`{kind:` + "`builtin`" + `,id:` + "`xai`" + `,label:` + "`xAI`" + `,titleKey:` + "`auth_login.xai_oauth_title`" + `,icon:{light:Ax,dark:jx},flow:` + "`browser`" + `},{kind:` + "`builtin`" + `,id:` + "`devin`" + `,label:` + "`Devin`" + `,titleKey:` + "`auth_login.devin_oauth_title`" + `,icon:{light:xx,dark:Sx},flow:` + "`browser`" + `}],ire=new Set(VM.map(e=>e.id)),are=new Set([` + "`codex`" + `,` + "`anthropic`" + `,` + "`antigravity`" + `,` + "`xai`" + `,` + "`devin`" + `])`)
	out := string(patchManagementHTMLForCommandCode(raw))
	if !strings.Contains(out, "kind:`builtin`,id:`commandcode`,label:`Command Code`,titleKey:`auth_login.commandcode_oauth_title`,icon:{light:xx,dark:Sx},flow:`browser`") {
		t.Fatalf("did not inject commandcode card with live icons:\n%s", out)
	}
}
