package api

import (
	"fmt"
	"regexp"
	"strings"
)

// Matches the minified Devin OAuth card across CPAMC builds (icon identifiers change).
var reDevinOAuthCard = regexp.MustCompile(
	`\{kind:` + "`builtin`" + `,id:` + "`devin`" + `,label:` + "`Devin`" +
		`,titleKey:` + "`auth_login.devin_oauth_title`" +
		`,icon:(?:\{light:[A-Za-z0-9_$]+,dark:[A-Za-z0-9_$]+\}|[A-Za-z0-9_$]+),flow:` + "`browser`" + `\}`,
)

// startAuth minified shape across CPAMC builds. Identifier names are unstable
// (by/Uee/W vs Nb/Mb/U), so capture them and rewrite with the same symbols.
var reStartAuth = regexp.MustCompile(
	`startAuth:\(e,t\)=>\{let n=([A-Za-z_$][\w$]*)\(e\),r=\{provider:n\};return ([A-Za-z_$][\w$]*)\.has\(n\)&&\(r\.is_webui=!0\),([A-Za-z_$][\w$]*)\.get\(` +
		"`/oauth/auth-url`" +
		`,\{params:r,\.\.\.t\?\{signal:t\}:\{\}\}\)\}`,
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

func patchCommandCodeStartAuth(out string) string {
	// Already on the fixed overlay build.
	if strings.Contains(out, "cc-apikey-overlay") && strings.Contains(out, "oauth/import?provider=commandcode") {
		return out
	}
	if loc := reStartAuth.FindStringSubmatchIndex(out); loc != nil {
		byFn := out[loc[2]:loc[3]]
		webuiSet := out[loc[4]:loc[5]]
		httpClient := out[loc[6]:loc[7]]
		neu := commandCodeStartAuthPatch(byFn, webuiSet, httpClient)
		return out[:loc[0]] + neu + out[loc[1]:]
	}
	// Upgrade an already-injected but outdated commandcode branch.
	if strings.Contains(out, "oauth/import?provider=commandcode") {
		return upgradeCommandCodeStartAuthPatch(out)
	}
	return out
}

// commandCodeStartAuthPatch returns minified startAuth that hides the stock OAuth
// dialog and shows a fixed API Key overlay (no browser auth steps).
func commandCodeStartAuthPatch(byFn, webuiSet, httpClient string) string {
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
		"startAuth:(e,t)=>{let n=%s(e),r={provider:n};return %s.has(n)&&(r.is_webui=!0),n===`commandcode`?(()=>new Promise((ok,no)=>{let zh=(localStorage.getItem(`cli-proxy-language`)||document.documentElement.lang||``).toLowerCase().startsWith(`zh`),done=0,busy=0;let hideOAuth=()=>{document.querySelectorAll(`.modal-overlay`).forEach(el=>{el.setAttribute(`data-cc-hide`,`1`);el.style.setProperty(`display`,`none`,`important`)})};let showOAuth=()=>{document.querySelectorAll(`[data-cc-hide]`).forEach(el=>{el.removeAttribute(`data-cc-hide`);el.style.removeProperty(`display`)})};let cleanup=()=>{let o=document.getElementById(`cc-apikey-overlay`);o&&o.remove();showOAuth()};let reject=m=>{if(done)return;done=1;cleanup();no(new Error(m||`cancelled`))};let accept=v=>{if(done)return;done=1;cleanup();ok(v)};hideOAuth();let old=document.getElementById(`cc-apikey-overlay`);old&&old.remove();let root=document.createElement(`div`);root.id=`cc-apikey-overlay`;root.setAttribute(`data-cc-apikey-overlay`,`1`);root.style.cssText=`position:fixed;inset:0;z-index:2147483646;display:flex;align-items:center;justify-content:center;background:rgba(0,0,0,.55);padding:24px;box-sizing:border-box`;root.innerHTML=`<div role=\"dialog\" aria-modal=\"true\" style=\"width:min(440px,100%%);border-radius:16px;background:var(--bg-primary,#1b1d21);color:var(--text-primary,#f3f4f6);border:1px solid var(--border-primary,#333);box-shadow:0 24px 64px rgba(0,0,0,.45);padding:22px 22px 18px;font:14px/1.45 system-ui,-apple-system,sans-serif\"><div style=\"font-size:16px;font-weight:650;margin:0 0 6px\">Command Code API Key</div><div style=\"opacity:.72;font-size:12px;margin:0 0 14px\">`+(zh?`填写 API Key`:`Enter API Key`)+`</div><p style=\"margin:0 0 14px;opacity:.85\">`+(zh?`从 <a href=\"https://commandcode.ai\" target=\"_blank\" rel=\"noreferrer\" style=\"color:#6ea8fe\">commandcode.ai</a> 或本机 ~/.commandcode/auth.json 复制 apiKey，无需浏览器授权`:`Paste apiKey from <a href=\"https://commandcode.ai\" target=\"_blank\" rel=\"noreferrer\" style=\"color:#6ea8fe\">commandcode.ai</a> or ~/.commandcode/auth.json — no browser OAuth`)+`</p><label for=\"cc-apikey-input\" style=\"display:block;margin:0 0 8px;font-weight:600\">API Key</label><input id=\"cc-apikey-input\" data-cc-input type=\"password\" autocomplete=\"off\" spellcheck=\"false\" placeholder=\"apiKey\" style=\"width:100%%;box-sizing:border-box;border-radius:10px;border:1px solid var(--border-primary,#444);background:var(--bg-secondary,#111);color:inherit;padding:10px 12px\"/><p data-cc-err style=\"display:none;margin:10px 0 0;color:#f87171\"></p><div style=\"display:flex;justify-content:flex-end;gap:8px;margin-top:18px\"><button type=\"button\" data-cc-cancel style=\"border-radius:10px;border:1px solid var(--border-primary,#444);background:transparent;color:inherit;padding:8px 14px;cursor:pointer\">`+(zh?`取消`:`Cancel`)+`</button><button type=\"button\" data-cc-ok style=\"border-radius:10px;border:0;background:#2563eb;color:#fff;padding:8px 14px;cursor:pointer\">`+(zh?`保存`:`Save`)+`</button></div></div>`;document.body.appendChild(root);let inp=root.querySelector(`[data-cc-input]`),err=root.querySelector(`[data-cc-err]`),okBtn=root.querySelector(`[data-cc-ok]`),cancelBtn=root.querySelector(`[data-cc-cancel]`);let fin=()=>{if(busy||done)return;let k=(inp.value||``).trim();if(!k){inp.focus();err.style.display=`block`;err.textContent=zh?`请填写 API Key`:`API Key is required`;return}busy=1;err.style.display=`none`;okBtn.disabled=!0;okBtn.textContent=zh?`保存中…`:`Saving…`;cancelBtn.disabled=!0;%s.post(`/oauth/import?provider=commandcode`,{api_key:k}).then(res=>{let d=res&&res.data&&(res.data.state!=null||res.data.status!=null)?res.data:res;accept(d)},e=>{busy=0;okBtn.disabled=!1;okBtn.textContent=zh?`保存`:`Save`;cancelBtn.disabled=!1;err.style.display=`block`;err.textContent=(e&&(e.response&&e.response.data&&(e.response.data.error||e.response.data.message)||e.message))||(zh?`保存失败`:`Save failed`)})};cancelBtn.onclick=()=>reject(`cancelled`);okBtn.onclick=fin;root.addEventListener(`keydown`,e=>{if(e.key===`Enter`){e.preventDefault();fin()}else if(e.key===`Escape`){e.preventDefault();reject(`cancelled`)}});setTimeout(()=>inp&&inp.focus(),30)}))():%s.get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}",
		byFn, webuiSet, httpClient, httpClient,
	)
}

func upgradeCommandCodeStartAuthPatch(out string) string {
	// Prefer replacing a bare startAuth that still hits auth-url.
	if loc := reStartAuth.FindStringSubmatchIndex(out); loc != nil {
		byFn := out[loc[2]:loc[3]]
		webuiSet := out[loc[4]:loc[5]]
		httpClient := out[loc[6]:loc[7]]
		return out[:loc[0]] + commandCodeStartAuthPatch(byFn, webuiSet, httpClient) + out[loc[1]:]
	}
	if i := strings.Index(out, "n===`commandcode`?"); i >= 0 {
		start := strings.LastIndex(out[:i], "startAuth:(e,t)=>{let n=")
		endMarker := ".get(`/oauth/auth-url`,{params:r,...t?{signal:t}:{}})}"
		endRel := strings.Index(out[i:], endMarker)
		if start >= 0 && endRel >= 0 {
			end := i + endRel + len(endMarker)
			// Recover identifiers from the surrounding startAuth prefix when possible.
			byFn, webuiSet, httpClient := "by", "Uee", "W"
			if m := reStartAuth.FindStringSubmatch(out[start:]); len(m) == 4 {
				byFn, webuiSet, httpClient = m[1], m[2], m[3]
			} else {
				// Best-effort parse: startAuth:(e,t)=>{let n=XXX(e)...
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
			return out[:start] + commandCodeStartAuthPatch(byFn, webuiSet, httpClient) + out[end:]
		}
	}
	return out
}
