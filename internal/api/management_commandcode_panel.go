package api

import (
	"fmt"
	"regexp"
	"strings"
)

// Matches the minified Devin OAuth card across CPAMC builds (icon identifiers
// change; newer builds append optional fields such as signInNoteKey after flow).
var reDevinOAuthCard = regexp.MustCompile(
	`\{kind:` + "`builtin`" + `,id:` + "`devin`" + `,label:` + "`Devin`" +
		`,titleKey:` + "`auth_login.devin_oauth_title`" +
		`,icon:(?:\{light:[A-Za-z0-9_$]+,dark:[A-Za-z0-9_$]+\}|[A-Za-z0-9_$]+),flow:` + "`browser`" +
		`(?:(?:,[A-Za-z0-9_$]+:(?:` + "`[^`]*`" + `|\{[^}]*\}|[A-Za-z0-9_$.]+))*)\}`,
)

// Legacy two-arg startAuth (CPAMC through ~2026-10):
// startAuth:(e,t)=>{let n=Nb(e),r={provider:n};return Mb.has(n)&&(r.is_webui=!0),U.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}
var reStartAuthLegacy = regexp.MustCompile(
	`startAuth:\(e,t\)=>\{let n=([A-Za-z_$][\w$]*)\(e\),r=\{provider:n\};return ([A-Za-z_$][\w$]*)\.has\(n\)&&\(r\.is_webui=!0\),([A-Za-z_$][\w$]*)\.get\(` +
		"`/oauth/auth-url`" +
		`,\{params:r,\.\.\.t\?\{signal:t\}:\{\}\}\)\}`,
)

// Current three-arg startAuth with optional proxyUrl (CPAMC ~2026-11):
// startAuth:(e,t,n={})=>{let r=fb(e),i={provider:r};Kte.has(r)&&(i.is_webui=!0);let a=n.proxyUrl?.trim();return a?W.post(`/oauth/auth-url`,{...i,proxy_url:a},t?{signal:t}:void 0):W.get(`/oauth/auth-url`,{params:i,...t?{signal:t}:{}})}
// RE2 has no backrefs, so identifiers are captured independently.
var reStartAuthProxy = regexp.MustCompile(
	`startAuth:\(e,t,n=\{\}\)=>\{let ([A-Za-z_$][\w$]*)=([A-Za-z_$][\w$]*)\(e\),([A-Za-z_$][\w$]*)=\{provider:[A-Za-z_$][\w$]*\};([A-Za-z_$][\w$]*)\.has\([A-Za-z_$][\w$]*\)&&\([A-Za-z_$][\w$]*\.is_webui=!0\);let ([A-Za-z_$][\w$]*)=n\.proxyUrl\?\.trim\(\);return [A-Za-z_$][\w$]*\?([A-Za-z_$][\w$]*)\.post\(` +
		"`/oauth/auth-url`" +
		`,\{\.\.\.[A-Za-z_$][\w$]*,proxy_url:[A-Za-z_$][\w$]*\},t\?\{signal:t\}:void 0\):[A-Za-z_$][\w$]*\.get\(` +
		"`/oauth/auth-url`" +
		`,\{params:[A-Za-z_$][\w$]*,\.\.\.t\?\{signal:t\}:\{\}\}\)\}`,
)

func injectOnceIfMissing(out, old, neu, marker string) string {
	if !strings.Contains(out, old) {
		return out
	}
	if strings.Contains(out, old) && strings.Contains(out, marker) {
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
				end := strings.Index(devinCard[idx+1:], ",flow:")
				if end >= 0 {
					iconPart = devinCard[idx+1 : idx+1+end]
				}
			}
			commandCard := "{kind:`builtin`,id:`commandcode`,label:`Command Code`,titleKey:`auth_login.commandcode_oauth_title`," +
				iconPart + ",flow:`browser`}"
			out = out[:loc[1]] + "," + commandCard + out[loc[1]:]
		}
	}

	out = ensureCommandCodeInProviderSets(out)

	devinFilter := "{id:`devin`,label:`Devin`,patterns:[]}"
	commandFilter := "{id:`commandcode`,label:`Command Code`,patterns:[/command[-_]?code/i,/poolside\\//i,/reflection\\//i,/glyph-cluster/i,/claude-/i,/gpt-/i,/deepseek\\//i,/moonshotai\\//i,/z-ai\\//i,/zai-org\\//i,/minimax\\//i,/MiniMaxAI\\//i,/ornith\\//i,/xiaomi\\//i,/Qwen\\//i,/meituan\\//i,/stepfun\\//i,/tencent\\//i,/google\\/gemini/i,/sakana\\//i,/nvidia\\//i,/thinkingmachines\\//i,/inclusionai\\//i,/meta\\/muse/i,/xai\\/grok/i,/mistral\\//i]}"
	if strings.Contains(out, devinFilter) && !strings.Contains(out, "{id:`commandcode`,label:`Command Code`") {
		out = strings.Replace(out, devinFilter, commandFilter+","+devinFilter, 1)
	}

	zhKeys := "commandcode_oauth_title:`Command Code API Key`,commandcode_oauth_button:`填写 API Key`,commandcode_oauth_hint:`粘贴 Command Code API Key 即可（https://commandcode.ai 或本机 ~/.commandcode/auth.json 的 apiKey）。无需浏览器授权跳转。`,commandcode_oauth_status_waiting:`正在保存 Command Code API Key...`,commandcode_oauth_status_success:`Command Code API Key 已保存！`,commandcode_oauth_status_error:`Command Code 保存失败：`,commandcode_oauth_start_error:`保存 Command Code API Key 失败：`,commandcode_open_link:`打开链接`,commandcode_copy_link:`复制链接`"
	enKeys := "commandcode_oauth_title:`Command Code API Key`,commandcode_oauth_button:`Enter API Key`,commandcode_oauth_hint:`Paste a Command Code API key (from https://commandcode.ai or ~/.commandcode/auth.json). No browser OAuth redirect.`,commandcode_oauth_status_waiting:`Saving Command Code API key...`,commandcode_oauth_status_success:`Command Code API key saved!`,commandcode_oauth_status_error:`Failed to save Command Code API key: `,commandcode_oauth_start_error:`Failed to save Command Code API key: `,commandcode_open_link:`Open link`,commandcode_copy_link:`Copy link`"
	twKeys := `"commandcode_oauth_title":"Command Code API Key","commandcode_oauth_button":"填寫 API Key","commandcode_oauth_hint":"貼上 Command Code API Key 即可，無需瀏覽器授權跳轉。","commandcode_oauth_status_waiting":"正在儲存 Command Code API Key...","commandcode_oauth_status_success":"Command Code API Key 已儲存！","commandcode_oauth_status_error":"Command Code 儲存失敗：","commandcode_oauth_start_error":"儲存 Command Code API Key 失敗：","commandcode_open_link":"開啟連結","commandcode_copy_link":"複製連結"`

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
	if strings.Contains(out, "devin_oauth_button:`Start Devin Login`") && !strings.Contains(out, "commandcode_oauth_button:`Enter API Key`") {
		out = strings.Replace(out, "devin_oauth_button:`Start Devin Login`", enKeys+",devin_oauth_button:`Start Devin Login`", 1)
	}
	if strings.Contains(out, "devin_oauth_button:`开始 Devin 登录`") && !strings.Contains(out, "commandcode_oauth_button:`填写 API Key`") {
		out = strings.Replace(out, "devin_oauth_button:`开始 Devin 登录`", zhKeys+",devin_oauth_button:`开始 Devin 登录`", 1)
	}

	out = patchCommandCodeStartAuth(out)
	return []byte(out)
}

// ensureCommandCodeInProviderSets appends `commandcode` to OAuth/webui provider
// sets that already list `devin` (or the classic anthropic/claude oauth sets).
func ensureCommandCodeInProviderSets(out string) string {
	replacements := [][2]string{
		{"[`codex`,`anthropic`,`antigravity`,`xai`,`devin`]", "[`codex`,`anthropic`,`antigravity`,`xai`,`devin`,`commandcode`]"},
		{"[`codex`,`claude`,`antigravity`,`xai`,`devin`]", "[`codex`,`claude`,`antigravity`,`xai`,`devin`,`commandcode`]"},
		{"[`antigravity`,`claude`,`codex`,`devin`,`kimi`,`meta`,`xai`]", "[`antigravity`,`claude`,`codex`,`devin`,`kimi`,`meta`,`xai`,`commandcode`]"},
		{"[`meta`,`antigravity`,`claude`,`codex`,`devin`,`kimi`,`plugin`,`xai`]", "[`meta`,`antigravity`,`claude`,`codex`,`devin`,`kimi`,`plugin`,`xai`,`commandcode`]"},
		{"[`meta`,`antigravity`,`claude`,`codex`,`kimi`,`xai`]", "[`meta`,`antigravity`,`claude`,`codex`,`kimi`,`xai`,`commandcode`]"},
		{"[`meta`,`vertex`,`aistudio`,`antigravity`,`xai`,`claude`,`codex`,`devin`,`kimi`]", "[`meta`,`vertex`,`aistudio`,`antigravity`,`xai`,`claude`,`codex`,`devin`,`kimi`,`commandcode`]"},
		{"[`claude`,`antigravity`,`codex`,`xai`,`kimi`,`devin`,`meta`,`plugin`]", "[`claude`,`antigravity`,`codex`,`xai`,`kimi`,`devin`,`meta`,`plugin`,`commandcode`]"},
	}
	for _, pair := range replacements {
		if strings.Contains(out, pair[0]) && !strings.Contains(out, pair[1]) {
			out = strings.ReplaceAll(out, pair[0], pair[1])
		}
	}
	return out
}

func patchCommandCodeStartAuth(out string) string {
	// Already on the fixed overlay build for the current startAuth shape.
	if strings.Contains(out, "cc-apikey-overlay") && strings.Contains(out, "oauth/import?provider=commandcode") {
		return out
	}
	if loc := reStartAuthProxy.FindStringSubmatchIndex(out); loc != nil {
		providerVar := out[loc[2]:loc[3]]
		byFn := out[loc[4]:loc[5]]
		paramsVar := out[loc[6]:loc[7]]
		webuiSet := out[loc[8]:loc[9]]
		proxyVar := out[loc[10]:loc[11]]
		httpClient := out[loc[12]:loc[13]]
		neu := commandCodeStartAuthProxyPatch(providerVar, byFn, paramsVar, webuiSet, proxyVar, httpClient)
		return out[:loc[0]] + neu + out[loc[1]:]
	}
	if loc := reStartAuthLegacy.FindStringSubmatchIndex(out); loc != nil {
		byFn := out[loc[2]:loc[3]]
		webuiSet := out[loc[4]:loc[5]]
		httpClient := out[loc[6]:loc[7]]
		neu := commandCodeStartAuthLegacyPatch(byFn, webuiSet, httpClient)
		return out[:loc[0]] + neu + out[loc[1]:]
	}
	// Upgrade an already-injected but outdated commandcode branch.
	if strings.Contains(out, "oauth/import?provider=commandcode") {
		return upgradeCommandCodeStartAuthPatch(out)
	}
	return out
}

// commandCodeStartAuthLegacyPatch returns minified startAuth that hides the stock
// OAuth dialog and shows a fixed API Key overlay (no browser auth steps).
func commandCodeStartAuthLegacyPatch(byFn, webuiSet, httpClient string) string {
	if byFn == "" {
		byFn = "by"
	}
	if webuiSet == "" {
		webuiSet = "Uee"
	}
	if httpClient == "" {
		httpClient = "W"
	}
	return fmt.Sprintf(
		"startAuth:(e,t)=>{let n=%s(e),r={provider:n};return %s.has(n)&&(r.is_webui=!0),n===`commandcode`?%s:%s.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}",
		byFn, webuiSet, commandCodeOverlayPromise(httpClient), httpClient,
	)
}

func commandCodeStartAuthProxyPatch(providerVar, byFn, paramsVar, webuiSet, proxyVar, httpClient string) string {
	if providerVar == "" {
		providerVar = "r"
	}
	if byFn == "" {
		byFn = "fb"
	}
	if paramsVar == "" {
		paramsVar = "i"
	}
	if webuiSet == "" {
		webuiSet = "Kte"
	}
	if proxyVar == "" {
		proxyVar = "a"
	}
	if httpClient == "" {
		httpClient = "W"
	}
	return fmt.Sprintf(
		"startAuth:(e,t,n={})=>{let %s=%s(e),%s={provider:%s};%s.has(%s)&&(%s.is_webui=!0);let %s=n.proxyUrl?.trim();return %s===`commandcode`?%s:%s?%s.post(`/oauth/auth-url`,{...%s,proxy_url:%s},t?{signal:t}:void 0):%s.get(`/oauth/auth-url`,{params:%s,...t?{signal:t}:{}})}",
		providerVar, byFn, paramsVar, providerVar, webuiSet, providerVar, paramsVar, proxyVar,
		providerVar, commandCodeOverlayPromise(httpClient),
		proxyVar, httpClient, paramsVar, proxyVar, httpClient, paramsVar,
	)
}

func commandCodeOverlayPromise(httpClient string) string {
	if httpClient == "" {
		httpClient = "W"
	}
	return fmt.Sprintf(
		"new Promise((ok,no)=>{let zh=(localStorage.getItem(`cli-proxy-language`)||document.documentElement.lang||``).toLowerCase().startsWith(`zh`),done=0,busy=0;let hideOAuth=()=>{document.querySelectorAll(`.modal-overlay`).forEach(el=>{el.setAttribute(`data-cc-hide`,`1`);el.style.setProperty(`display`,`none`,`important`)})};let showOAuth=()=>{document.querySelectorAll(`[data-cc-hide]`).forEach(el=>{el.removeAttribute(`data-cc-hide`);el.style.removeProperty(`display`)})};let cleanup=()=>{let o=document.getElementById(`cc-apikey-overlay`);o&&o.remove();showOAuth()};let reject=m=>{if(done)return;done=1;cleanup();no(new Error(m||`cancelled`))};let accept=v=>{if(done)return;done=1;cleanup();ok(v)};hideOAuth();let old=document.getElementById(`cc-apikey-overlay`);old&&old.remove();let root=document.createElement(`div`);root.id=`cc-apikey-overlay`;root.setAttribute(`data-cc-apikey-overlay`,`1`);root.style.cssText=`position:fixed;inset:0;z-index:2147483646;display:flex;align-items:center;justify-content:center;background:rgba(0,0,0,.55);padding:24px;box-sizing:border-box`;root.innerHTML=`<div role=\"dialog\" aria-modal=\"true\" style=\"width:min(440px,100%%);border-radius:16px;background:var(--bg-primary,#1b1d21);color:var(--text-primary,#f3f4f6);border:1px solid var(--border-primary,#333);box-shadow:0 24px 64px rgba(0,0,0,.45);padding:22px 22px 18px;font:14px/1.45 system-ui,-apple-system,sans-serif\"><div style=\"font-size:16px;font-weight:650;margin:0 0 6px\">Command Code API Key</div><div style=\"opacity:.72;font-size:12px;margin:0 0 14px\">`+(zh?`填写 API Key`:`Enter API Key`)+`</div><p style=\"margin:0 0 14px;opacity:.85\">`+(zh?`从 <a href=\"https://commandcode.ai\" target=\"_blank\" rel=\"noreferrer\" style=\"color:#6ea8fe\">commandcode.ai</a> 或本机 ~/.commandcode/auth.json 复制 apiKey，无需浏览器授权`:`Paste apiKey from <a href=\"https://commandcode.ai\" target=\"_blank\" rel=\"noreferrer\" style=\"color:#6ea8fe\">commandcode.ai</a> or ~/.commandcode/auth.json — no browser OAuth`)+`</p><label for=\"cc-apikey-input\" style=\"display:block;margin:0 0 8px;font-weight:600\">API Key</label><input id=\"cc-apikey-input\" data-cc-input type=\"password\" autocomplete=\"off\" spellcheck=\"false\" placeholder=\"apiKey\" style=\"width:100%%;box-sizing:border-box;border-radius:10px;border:1px solid var(--border-primary,#444);background:var(--bg-secondary,#111);color:inherit;padding:10px 12px\"/><p data-cc-err style=\"display:none;margin:10px 0 0;color:#f87171\"></p><div style=\"display:flex;justify-content:flex-end;gap:8px;margin-top:18px\"><button type=\"button\" data-cc-cancel style=\"border-radius:10px;border:1px solid var(--border-primary,#444);background:transparent;color:inherit;padding:8px 14px;cursor:pointer\">`+(zh?`取消`:`Cancel`)+`</button><button type=\"button\" data-cc-ok style=\"border-radius:10px;border:0;background:#2563eb;color:#fff;padding:8px 14px;cursor:pointer\">`+(zh?`保存`:`Save`)+`</button></div></div>`;document.body.appendChild(root);let inp=root.querySelector(`[data-cc-input]`),err=root.querySelector(`[data-cc-err]`),okBtn=root.querySelector(`[data-cc-ok]`),cancelBtn=root.querySelector(`[data-cc-cancel]`);let fin=()=>{if(busy||done)return;let k=(inp.value||``).trim();if(!k){inp.focus();err.style.display=`block`;err.textContent=zh?`请填写 API Key`:`API Key is required`;return}busy=1;err.style.display=`none`;okBtn.disabled=!0;okBtn.textContent=zh?`保存中…`:`Saving…`;cancelBtn.disabled=!0;%s.post(`/oauth/import?provider=commandcode`,{api_key:k}).then(res=>{let d=res&&res.data&&(res.data.state!=null||res.data.status!=null)?res.data:res;accept(d)},e=>{busy=0;okBtn.disabled=!1;okBtn.textContent=zh?`保存`:`Save`;cancelBtn.disabled=!1;err.style.display=`block`;err.textContent=(e&&(e.response&&e.response.data&&(e.response.data.error||e.response.data.message)||e.message))||(zh?`保存失败`:`Save failed`)})};cancelBtn.onclick=()=>reject(`cancelled`);okBtn.onclick=fin;root.addEventListener(`keydown`,e=>{if(e.key===`Enter`){e.preventDefault();fin()}else if(e.key===`Escape`){e.preventDefault();reject(`cancelled`)}});setTimeout(()=>inp&&inp.focus(),30)})",
		httpClient,
	)
}

func upgradeCommandCodeStartAuthPatch(out string) string {
	if loc := reStartAuthProxy.FindStringSubmatchIndex(out); loc != nil {
		providerVar := out[loc[2]:loc[3]]
		byFn := out[loc[4]:loc[5]]
		paramsVar := out[loc[6]:loc[7]]
		webuiSet := out[loc[8]:loc[9]]
		proxyVar := out[loc[10]:loc[11]]
		httpClient := out[loc[12]:loc[13]]
		return out[:loc[0]] + commandCodeStartAuthProxyPatch(providerVar, byFn, paramsVar, webuiSet, proxyVar, httpClient) + out[loc[1]:]
	}
	if loc := reStartAuthLegacy.FindStringSubmatchIndex(out); loc != nil {
		byFn := out[loc[2]:loc[3]]
		webuiSet := out[loc[4]:loc[5]]
		httpClient := out[loc[6]:loc[7]]
		return out[:loc[0]] + commandCodeStartAuthLegacyPatch(byFn, webuiSet, httpClient) + out[loc[1]:]
	}
	if i := strings.Index(out, "n===`commandcode`?"); i >= 0 {
		start := strings.LastIndex(out[:i], "startAuth:(e,t)=>{let n=")
		endMarker := ".get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}"
		endRel := strings.Index(out[i:], endMarker)
		if start >= 0 && endRel >= 0 {
			end := i + endRel + len(endMarker)
			byFn, webuiSet, httpClient := "by", "Uee", "W"
			if m := reStartAuthLegacy.FindStringSubmatch(out[start:]); len(m) == 4 {
				byFn, webuiSet, httpClient = m[1], m[2], m[3]
			} else {
				frag := out[start:end]
				if m2 := regexp.MustCompile(`let n=([A-Za-z_$][\w$]*)\(e\)`).FindStringSubmatch(frag); len(m2) == 2 {
					byFn = m2[1]
				}
				if m2 := regexp.MustCompile(`return ([A-Za-z_$][\w$]*)\.has\(n\)`).FindStringSubmatch(frag); len(m2) == 2 {
					webuiSet = m2[1]
				}
				if m2 := regexp.MustCompile(`([A-Za-z_$][\w$]*)\.get\(` + "`/oauth/auth-url`" + ``).FindStringSubmatch(frag); len(m2) == 2 {
					httpClient = m2[1]
				}
			}
			return out[:start] + commandCodeStartAuthLegacyPatch(byFn, webuiSet, httpClient) + out[end:]
		}
	}
	if i := strings.Index(out, "===`commandcode`?"); i >= 0 {
		start := strings.LastIndex(out[:i], "startAuth:(e,t,n={})=>{")
		endMarker := ".get(`/oauth/auth-url`,{params:"
		endRel := strings.Index(out[i:], endMarker)
		if start >= 0 && endRel >= 0 {
			// Find closing of the get(...) call.
			closeRel := strings.Index(out[i+endRel:], ")}")
			if closeRel >= 0 {
				end := i + endRel + closeRel + 2
				if m := reStartAuthProxy.FindStringSubmatch(out[start:]); len(m) == 7 {
					return out[:start] + commandCodeStartAuthProxyPatch(m[1], m[2], m[3], m[4], m[5], m[6]) + out[end:]
				}
			}
		}
	}
	return out
}
