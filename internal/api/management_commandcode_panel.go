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

func injectOnceIfMissing(out, old, neu, marker string) string {
	if !strings.Contains(out, old) {
		return out
	}
	// If this exact old segment already has marker immediately after title, skip.
	if strings.Contains(out, old) && strings.Contains(out, marker) {
		// Still replace occurrences of old that do not already include marker nearby.
		parts := strings.Split(out, old)
		if len(parts) == 1 {
			return out
		}
		var b strings.Builder
		b.WriteString(parts[0])
		for i := 1; i < len(parts); i++ {
			peek := parts[i]
			if len(peek) > 80 {
				peek = peek[:80]
			}
			if strings.Contains(peek, marker) {
				b.WriteString(old)
			} else {
				b.WriteString(neu)
			}
			b.WriteString(parts[i])
		}
		return b.String()
	}
	return strings.ReplaceAll(out, old, neu)
}

// patchManagementHTMLForCommandCode injects Command Code into the upstream CPAMC
// control panel (management.html), which hardcodes OAuth provider cards.
func patchManagementHTMLForCommandCode(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	out := string(raw)

	hasOAuthCard := strings.Contains(out, "kind:`builtin`,id:`commandcode`") ||
		strings.Contains(out, `kind:"builtin",id:"commandcode"`)

	if !hasOAuthCard {
		if loc := reDevinOAuthCard.FindStringIndex(out); loc != nil {
			devinCard := out[loc[0]:loc[1]]
			iconPart := "icon:{light:xx,dark:Sx}"
			if idx := strings.Index(devinCard, ",icon:"); idx >= 0 {
				end := strings.LastIndex(devinCard, ",flow:")
				if end > idx {
					iconPart = devinCard[idx+1 : end]
				}
			}
			commandCard := "{kind:`builtin`,id:`commandcode`,label:`Command Code`,titleKey:`auth_login.commandcode_oauth_title`," +
				iconPart + ",flow:`browser`}"
			out = out[:loc[1]] + "," + commandCard + out[loc[1]:]
		}
	}

	replacements := [][2]string{
		{"[`codex`,`anthropic`,`antigravity`,`xai`,`devin`]", "[`codex`,`anthropic`,`antigravity`,`xai`,`devin`,`commandcode`]"},
		{"[`codex`,`claude`,`antigravity`,`xai`,`devin`]", "[`codex`,`claude`,`antigravity`,`xai`,`devin`,`commandcode`]"},
	}
	for _, pair := range replacements {
		if strings.Contains(out, pair[0]) && !strings.Contains(out, pair[1]) {
			out = strings.ReplaceAll(out, pair[0], pair[1])
		}
	}

	devinFilter := "{id:`devin`,label:`Devin`,patterns:[]}"
	commandFilter := "{id:`commandcode`,label:`Command Code`,patterns:[/command[-_]?code/i,/poolside\\//i,/reflection\\//i,/glyph-cluster/i]}"
	if strings.Contains(out, devinFilter) && !strings.Contains(out, "{id:`commandcode`,label:`Command Code`") {
		out = strings.Replace(out, devinFilter, commandFilter+","+devinFilter, 1)
	}

	zhKeys := "commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`开始 Command Code 登录`,commandcode_oauth_hint:`远程请粘贴 API Key。浏览器登录回调本机且无查询参数。`,commandcode_oauth_status_waiting:`等待 Command Code 认证中...`,commandcode_oauth_status_success:`Command Code 认证成功！`,commandcode_oauth_status_error:`Command Code 认证失败：`,commandcode_oauth_start_error:`启动 Command Code 登录失败：`,commandcode_open_link:`打开链接`,commandcode_copy_link:`复制链接`"
	enKeys := "commandcode_oauth_title:`Command Code OAuth`,commandcode_oauth_button:`Start Command Code Login`,commandcode_oauth_hint:`Paste an API key on remote hosts. Browser login callbacks to localhost with no query string.`,commandcode_oauth_status_waiting:`Waiting for Command Code authentication...`,commandcode_oauth_status_success:`Command Code authentication successful!`,commandcode_oauth_status_error:`Command Code authentication failed: `,commandcode_oauth_start_error:`Failed to start Command Code login: `,commandcode_open_link:`Open link`,commandcode_copy_link:`Copy link`"
	twKeys := `"commandcode_oauth_title":"Command Code OAuth","commandcode_oauth_button":"開始 Command Code 登入","commandcode_oauth_hint":"遠端請貼上 API Key。","commandcode_oauth_status_waiting":"等待 Command Code 認證中...","commandcode_oauth_status_success":"Command Code 認證成功！","commandcode_oauth_status_error":"Command Code 認證失敗：","commandcode_oauth_start_error":"啟動 Command Code 登入失敗：","commandcode_open_link":"開啟連結","commandcode_copy_link":"複製連結"`

	out = injectOnceIfMissing(
		out,
		"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`开始 Devin 登录`",
		"devin_oauth_title:`Devin OAuth`,"+zhKeys+",devin_oauth_button:`开始 Devin 登录`",
		"commandcode_oauth_title",
	)
	out = injectOnceIfMissing(
		out,
		"devin_oauth_title:`Devin OAuth`,devin_oauth_button:`Start Devin Login`",
		"devin_oauth_title:`Devin OAuth`,"+enKeys+",devin_oauth_button:`Start Devin Login`",
		"commandcode_oauth_title",
	)
	out = injectOnceIfMissing(
		out,
		`"devin_oauth_title":"Devin OAuth","devin_oauth_button":"開始 Devin 登入"`,
		`"devin_oauth_title":"Devin OAuth",`+twKeys+`,"devin_oauth_button":"開始 Devin 登入"`,
		"commandcode_oauth_title",
	)
	// Fallback: English title alone already partially patched in older builds.
	if strings.Contains(out, "devin_oauth_button:`Start Devin Login`") && !strings.Contains(out, "commandcode_oauth_button:`Start Command Code Login`") {
		out = strings.Replace(out, "devin_oauth_button:`Start Devin Login`", enKeys+",devin_oauth_button:`Start Devin Login`", 1)
	}
	if strings.Contains(out, "devin_oauth_button:`开始 Devin 登录`") && !strings.Contains(out, "commandcode_oauth_button:`开始 Command Code 登录`") {
		out = strings.Replace(out, "devin_oauth_button:`开始 Devin 登录`", zhKeys+",devin_oauth_button:`开始 Devin 登录`", 1)
	}

	startAuthOld := "startAuth:(e,t)=>{let n=by(e),r={provider:n};return Uee.has(n)&&(r.is_webui=!0),W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}"
	startAuthNew := "startAuth:(e,t)=>{let n=by(e),r={provider:n};return Uee.has(n)&&(r.is_webui=!0),n===`commandcode`?(()=>{let k=(prompt(`粘贴 Command Code API Key（远程推荐）\\n可从本机 ~/.commandcode/auth.json 的 apiKey 复制\\n\\n浏览器登录请留空（必须已 SSH 端口转发）`,``)||``).trim();return k?W.post(`/oauth/import?provider=commandcode`,{api_key:k}):(r.callback=(prompt(`浏览器登录：localhost 回调地址`,`http://127.0.0.1:`+(location.port||`8317`)+`/callback`)||``).trim(),r.callback?W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}}):Promise.reject(new Error(`cancelled`)))})():W.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}"
	if strings.Contains(out, startAuthOld) && !strings.Contains(out, "oauth/import?provider=commandcode") {
		out = strings.Replace(out, startAuthOld, startAuthNew, 1)
	}

	return []byte(out)
}
