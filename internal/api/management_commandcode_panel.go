package api

import (
	"regexp"
	"strings"
)

// Matches the minified Devin OAuth card across CPAMC builds (icon identifiers change).
var reDevinOAuthCard = regexp.MustCompile(
	`\{kind:` + "`builtin`" + `,id:` + "`devin`" + `,label:` + "`Devin`" +
		`,titleKey:` + "`auth_login.devin_oauth_title`" +
		`,icon:(?:\{light:[A-Za-z0-9_$]+,dark:[A-Za-z0-9_$]+\}|[A-Za-z0-9_$]+),flow:` + "`browser`" + `\}`,
)

// patchManagementHTMLForCommandCode injects Command Code into the upstream CPAMC
// control panel (management.html), which hardcodes OAuth provider cards.
func patchManagementHTMLForCommandCode(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	out := string(raw)

	// Already has the OAuth provider card — still ensure sets / i18n / filters.
	hasOAuthCard := strings.Contains(out, "kind:`builtin`,id:`commandcode`") ||
		strings.Contains(out, `kind:"builtin",id:"commandcode"`)

	if !hasOAuthCard {
		if loc := reDevinOAuthCard.FindStringIndex(out); loc != nil {
			devinCard := out[loc[0]:loc[1]]
			// Reuse Devin's icon identifiers so the minified asset stays consistent.
			iconPart := "icon:{light:xx,dark:Sx}"
			if idx := strings.Index(devinCard, ",icon:"); idx >= 0 {
				end := strings.LastIndex(devinCard, ",flow:")
				if end > idx {
					iconPart = devinCard[idx+1 : end] // "icon:..."
				}
			}
			commandCard := "{kind:`builtin`,id:`commandcode`,label:`Command Code`,titleKey:`auth_login.commandcode_oauth_title`," +
				iconPart + ",flow:`browser`}"
			out = out[:loc[1]] + "," + commandCard + out[loc[1]:]
		}
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
			"devin_oauth_title:`Devin OAuth`,commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`开始 Command Code 登录`,commandcode_oauth_hint:`远程请直接粘贴 API Key。浏览器登录会回调 127.0.0.1（无查询参数），必须先 SSH 端口转发，否则会连接被拒绝。`",
		},
		{
			`"devin_oauth_title":"Devin OAuth"`,
			`"devin_oauth_title":"Devin OAuth","commandcode_oauth_title":"Command Code OAuth","commandcode_oauth_button":"開始 Command Code 登入","commandcode_oauth_hint":"透過瀏覽器 OAuth 登入 Command Code，自動儲存 API Key。"`,
		},
		{
			"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`Start Devin Login`",
			"devin_oauth_title:`Devin OAuth`,commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`Start Command Code Login`,commandcode_oauth_hint:`You will be asked for a localhost callback (e.g. http://127.0.0.1:8317/callback). Use an SSH tunnel when the proxy is remote.`,devin_oauth_button:`Start Devin Login`",
		},
	}
	for _, pair := range i18nInserts {
		if strings.Contains(out, pair[0]) && !strings.Contains(out, "commandcode_oauth_title") {
			out = strings.Replace(out, pair[0], pair[1], 1)
		}
	}
	if strings.Contains(out, "devin_oauth_button:`Start Devin Login`") && !strings.Contains(out, "commandcode_oauth_button:`Start Command Code Login`") {
		out = strings.Replace(
			out,
			"devin_oauth_button:`Start Devin Login`",
			"commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`Start Command Code Login`,commandcode_oauth_hint:`You will be asked for a localhost callback (e.g. http://127.0.0.1:8317/callback). Use an SSH tunnel when the proxy is remote.`,devin_oauth_button:`Start Devin Login`",
			1,
		)
	}

	// Command Code: prefer pasting API key (remote-safe). Browser OAuth needs a live
	// localhost listener because studio POSTs apiKey to the callback (URL has no query).
	startAuthOld := "startAuth:(e,t)=>{let n=by(e),r={provider:n};return Uee.has(n)&&(r.is_webui=!0),W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}"
	startAuthNew := "startAuth:(e,t)=>{let n=by(e),r={provider:n};return Uee.has(n)&&(r.is_webui=!0),n===`commandcode`?(()=>{let k=(prompt(`推荐：粘贴 Command Code API Key（远程服务器请用这个）\\n来源：本机 ~/.commandcode/auth.json 的 apiKey，或 commandcode.ai\\n\\n若坚持浏览器登录请留空（须先 SSH -L 把端口转到本机，否则会 127.0.0.1 连接被拒绝且地址栏无参数）`,``)||``).trim();return k?W.post(`/oauth/import?provider=commandcode`,{api_key:k}):(r.callback=(prompt(`浏览器登录：填写 localhost 回调（须已 SSH 端口转发）`,`http://127.0.0.1:`+(location.port||`8317`)+`/callback`)||``).trim(),r.callback?W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}}):Promise.reject(new Error(`cancelled`)))})():W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}"
	if strings.Contains(out, startAuthOld) && !strings.Contains(out, "oauth/import?provider=commandcode") {
		out = strings.Replace(out, startAuthOld, startAuthNew, 1)
	}

	return []byte(out)
}
