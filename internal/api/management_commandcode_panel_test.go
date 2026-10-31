package api

import (
	"os"
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
				"commandcode_oauth_title:`Command Code API Key`",
				"commandcode_oauth_button:`Enter API Key`",
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

func TestPatchManagementHTMLInjectsAPIKeyImport(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`开始 Devin 登录`",
		"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`Start Devin Login`",
		"startAuth:(e,t)=>{let n=by(e),r={provider:n};return Uee.has(n)&&(r.is_webui=!0),W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}",
	}, ""))
	out := string(patchManagementHTMLForCommandCode(raw))
	if !strings.Contains(out, "oauth/import?provider=commandcode") {
		t.Fatalf("missing commandcode API key import patch:\n%s", out)
	}
	if !strings.Contains(out, "cc-apikey-overlay") || !strings.Contains(out, "data-cc-apikey-overlay") {
		t.Fatal("missing fixed API key overlay marker")
	}
	if !strings.Contains(out, "W.post(`/oauth/import?provider=commandcode`") {
		t.Fatal("overlay must reuse the page HTTP client symbol W")
	}
	if strings.Contains(out, "prompt(`Command Code API Key") || strings.Contains(out, "window.prompt") {
		t.Fatal("browser prompt() must not be used for API key input")
	}
	if strings.Contains(out, "浏览器登录请留空") || strings.Contains(out, "r.callback=") {
		t.Fatal("browser OAuth path should be removed from startAuth")
	}
	if !strings.Contains(out, "commandcode_oauth_status_error:`Command Code 保存失败：`") {
		t.Fatal("missing zh status error i18n")
	}
	if !strings.Contains(out, "commandcode_oauth_button:`Enter API Key`") {
		t.Fatal("missing en button i18n")
	}
	if !strings.Contains(out, "commandcode_oauth_button:`填写 API Key`") {
		t.Fatal("missing zh button i18n")
	}
}

func TestPatchManagementHTMLMatchesRenamedMinifiedSymbols(t *testing.T) {
	// Live CPAMC asset after 2026-10-31: by/Uee/W renamed to Nb/Mb/U.
	raw := []byte("startAuth:(e,t)=>{let n=Nb(e),r={provider:n};return Mb.has(n)&&(r.is_webui=!0),U.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}")
	out := string(patchManagementHTMLForCommandCode(raw))
	if !strings.Contains(out, "cc-apikey-overlay") {
		t.Fatal("expected overlay patch against renamed symbols")
	}
	if !strings.Contains(out, "let n=Nb(e)") || !strings.Contains(out, "Mb.has(n)") {
		t.Fatal("patched startAuth must keep Nb/Mb symbols")
	}
	if !strings.Contains(out, "U.post(`/oauth/import?provider=commandcode`") {
		t.Fatal("patched startAuth must call U.post for import")
	}
	if !strings.Contains(out, "U.get(`/oauth/auth-url`") {
		t.Fatal("non-commandcode branch must still use U.get auth-url")
	}
	if strings.Contains(out, "startAuth:(e,t)=>{let n=Nb(e),r={provider:n};return Mb.has(n)&&(r.is_webui=!0),U.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}") {
		t.Fatal("unpatched startAuth must not remain")
	}
}

func TestPatchManagementHTMLUpgradesPromptToModal(t *testing.T) {
	legacy := "startAuth:(e,t)=>{let n=by(e),r={provider:n};return Uee.has(n)&&(r.is_webui=!0),n===`commandcode`?(()=>{let k=(prompt(`Command Code API Key\\n从 https://commandcode.ai 或本机 ~/.commandcode/auth.json 的 apiKey 复制`,``)||``).trim();return k?W.post(`/oauth/import?provider=commandcode`,{api_key:k}):Promise.reject(new Error(`api_key required`))})():W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}"
	out := string(patchManagementHTMLForCommandCode([]byte(legacy)))
	if strings.Contains(out, "prompt(`Command Code API Key") {
		t.Fatal("legacy prompt() patch should be upgraded")
	}
	if !strings.Contains(out, "cc-apikey-overlay") {
		t.Fatal("upgraded patch should use fixed API key overlay")
	}
	again := string(patchManagementHTMLForCommandCode([]byte(out)))
	if again != out {
		t.Fatal("modal patch should be idempotent")
	}
}

func TestPatchManagementHTMLAgainstLiveSnippet(t *testing.T) {
	raw := []byte(`{kind:` + "`builtin`" + `,id:` + "`xai`" + `,label:` + "`xAI`" + `,titleKey:` + "`auth_login.xai_oauth_title`" + `,icon:{light:Ax,dark:jx},flow:` + "`browser`" + `},{kind:` + "`builtin`" + `,id:` + "`devin`" + `,label:` + "`Devin`" + `,titleKey:` + "`auth_login.devin_oauth_title`" + `,icon:{light:xx,dark:Sx},flow:` + "`browser`" + `}],ire=new Set(VM.map(e=>e.id)),are=new Set([` + "`codex`" + `,` + "`anthropic`" + `,` + "`antigravity`" + `,` + "`xai`" + `,` + "`devin`" + `])`)
	out := string(patchManagementHTMLForCommandCode(raw))
	if !strings.Contains(out, "kind:`builtin`,id:`commandcode`,label:`Command Code`,titleKey:`auth_login.commandcode_oauth_title`,icon:{light:xx,dark:Sx},flow:`browser`") {
		t.Fatalf("did not inject commandcode card with live icons:\n%s", out)
	}
}

func TestPatchManagementHTMLAgainstRealFile(t *testing.T) {
	raw, err := os.ReadFile(`D:\code\commandcodectf\cliproxy-local\static\management.html`)
	if err != nil {
		t.Skip(err)
	}
	out := string(patchManagementHTMLForCommandCode(raw))
	if !strings.Contains(out, "cc-apikey-overlay") {
		t.Fatal("real management.html must receive API key overlay patch")
	}
	if !strings.Contains(out, "oauth/import?provider=commandcode") {
		t.Fatal("real management.html must receive import route")
	}
	if !strings.Contains(out, "id:`commandcode`") {
		t.Fatal("real management.html must receive commandcode card")
	}
}
