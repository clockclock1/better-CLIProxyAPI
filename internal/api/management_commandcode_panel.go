package api

import (
	"bytes"
	"strings"
)

// patchManagementHTMLForCommandCode injects Command Code into the upstream CPAMC
// control panel (management.html), which hardcodes OAuth provider cards.
func patchManagementHTMLForCommandCode(raw []byte) []byte {
	if len(raw) == 0 || bytes.Contains(raw, []byte("id:`commandcode`")) || bytes.Contains(raw, []byte(`id:"commandcode"`)) {
		return raw
	}
	out := string(raw)

	// Builtin OAuth provider card list (IN=[{...},{id:`devin`,...}])
	devinCard := "{kind:`builtin`,id:`devin`,label:`Devin`,titleKey:`auth_login.devin_oauth_title`,icon:{light:iS,dark:aS},flow:`browser`}"
	commandCard := "{kind:`builtin`,id:`commandcode`,label:`Command Code`,titleKey:`auth_login.commandcode_oauth_title`,icon:{light:iS,dark:aS},flow:`browser`}"
	if strings.Contains(out, devinCard) && !strings.Contains(out, commandCard) {
		out = strings.Replace(out, devinCard, devinCard+","+commandCard, 1)
	}

	// Recommended / browser-callback provider sets used by startAuth(is_webui).
	replacements := [][2]string{
		{"[`codex`,`anthropic`,`antigravity`,`xai`,`devin`]", "[`codex`,`anthropic`,`antigravity`,`xai`,`devin`,`commandcode`]"},
		{"[`codex`,`claude`,`antigravity`,`xai`,`devin`]", "[`codex`,`claude`,`antigravity`,`xai`,`devin`,`commandcode`]"},
	}
	for _, pair := range replacements {
		if strings.Contains(out, pair[0]) && !strings.Contains(out, pair[1]) {
			out = strings.ReplaceAll(out, pair[0], pair[1])
		}
	}

	// Model filter chips.
	devinFilter := "{id:`devin`,label:`Devin`,patterns:[]}"
	commandFilter := "{id:`commandcode`,label:`Command Code`,patterns:[/command[-_]?code/i,/poolside\\//i,/reflection\\//i,/glyph-cluster/i]}"
	if strings.Contains(out, devinFilter) && !strings.Contains(out, "{id:`commandcode`,label:`Command Code`") {
		out = strings.Replace(out, devinFilter, commandFilter+","+devinFilter, 1)
	}

	// i18n strings (zh-CN / zh-TW / en share the auth_login object shape).
	i18nInserts := [][2]string{
		{
			"devin_oauth_title:`Devin OAuth`",
			"devin_oauth_title:`Devin OAuth`,commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`开始 Command Code 登录`,commandcode_oauth_hint:`通过浏览器 OAuth 登录 Command Code，自动保存 API Key。`",
		},
		{
			`"devin_oauth_title":"Devin OAuth"`,
			`"devin_oauth_title":"Devin OAuth","commandcode_oauth_title":"Command Code OAuth","commandcode_oauth_button":"開始 Command Code 登入","commandcode_oauth_hint":"透過瀏覽器 OAuth 登入 Command Code，自動儲存 API Key。"`,
		},
		{
			"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`Start Devin Login`",
			"devin_oauth_title:`Devin OAuth`,commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`Start Command Code Login`,commandcode_oauth_hint:`Sign in to Command Code through browser OAuth to save an API key.`,devin_oauth_button:`Start Devin Login`",
		},
	}
	for _, pair := range i18nInserts {
		if strings.Contains(out, pair[0]) && !strings.Contains(out, "commandcode_oauth_title") {
			out = strings.Replace(out, pair[0], pair[1], 1)
		}
	}
	// English block may already have been partially matched by the first zh pattern;
	// ensure English title exists when the Start Devin Login phrase is present.
	if strings.Contains(out, "devin_oauth_button:`Start Devin Login`") && !strings.Contains(out, "commandcode_oauth_button:`Start Command Code Login`") {
		out = strings.Replace(
			out,
			"devin_oauth_button:`Start Devin Login`",
			"commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`Start Command Code Login`,commandcode_oauth_hint:`Sign in to Command Code through browser OAuth to save an API key.`,devin_oauth_button:`Start Devin Login`",
			1,
		)
	}

	return []byte(out)
}
