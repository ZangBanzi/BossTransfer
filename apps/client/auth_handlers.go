package main

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/licenseclient"
	"bosstransfer/internal/websession"
)

type clientLicenseApp struct{ service *licenseclient.Service }

func (a clientLicenseApp) statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"license": a.service.Status()})
}

func (a clientLicenseApp) activateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	var payload struct {
		ManagerURL  string `json:"manager_url"`
		Code        string `json:"code"`
		DeviceLabel string `json:"device_label"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	if strings.TrimSpace(payload.DeviceLabel) == "" {
		payload.DeviceLabel, _ = os.Hostname()
	}
	status, err := a.service.Activate(r.Context(), payload.ManagerURL, payload.Code, payload.DeviceLabel)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "activation_failed", "message": err.Error(), "license": status})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"license": status})
}

func requireActiveLicense(service *licenseclient.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := service.Status()
		if !status.Activated {
			httpapi.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "license_required", "message": "请先使用管理员签发的授权码激活此客户端", "license": status})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requireLicensePage(service *licenseclient.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !service.Status().Activated {
			http.Redirect(w, r, "/activate", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func startLicenseHeartbeat(ctx context.Context, service *licenseclient.Service) {
	go func() {
		if service.Status().DeviceID != "" {
			heartbeatCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, _ = service.Heartbeat(heartbeatCtx)
			cancel()
		}
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if service.Status().DeviceID != "" {
					heartbeatCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					_, _ = service.Heartbeat(heartbeatCtx)
					cancel()
				}
			}
		}
	}()
}

func loginPageHandler(auth *websession.Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			http.NotFound(w, r)
			return
		}
		if principal, err := auth.Principal(r); err == nil {
			if principal.MustChange {
				http.Redirect(w, r, "/password", http.StatusSeeOther)
			} else {
				http.Redirect(w, r, "/", http.StatusSeeOther)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = loginTemplate.Execute(w, nil)
	}
}

func clientPasswordPageHandler(auth *websession.Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/password" {
			http.NotFound(w, r)
			return
		}
		principal, err := auth.Principal(r)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = clientPasswordTemplate.Execute(w, principal)
	}
}

func activationPageHandler(service *licenseclient.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/activate" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = activationTemplate.Execute(w, service.Status())
	}
}

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>登录 · BossTransfer</title><style>
:root{color-scheme:dark;font-family:InterVariable,Inter,-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;font-feature-settings:"cv01","ss03";color:#f7f8f8;background:#08090a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at 50% -20%,rgba(113,112,255,.16),transparent 42%),#08090a}.card{width:min(420px,100%);padding:32px;background:rgba(255,255,255,.025);border:1px solid rgba(255,255,255,.08);border-radius:10px;box-shadow:0 28px 90px rgba(0,0,0,.42)}.logo{font-size:25px;font-weight:590;letter-spacing:-.6px;color:#f7f8f8;margin-bottom:7px}.logo:after{content:" / CLIENT";color:#7170ff;font:510 11px ui-monospace,SFMono-Regular,Consolas,monospace;letter-spacing:.08em}.sub{color:#8a8f98;margin:0 0 25px;line-height:1.6;font-size:14px}label{display:block;color:#d0d6e0;font-size:12px;font-weight:510;margin:15px 0 7px}input{width:100%;min-height:48px;border:1px solid rgba(255,255,255,.1);border-radius:6px;padding:0 13px;font:inherit;background:rgba(255,255,255,.025);color:#f7f8f8;outline:none}input:focus{border-color:#7170ff;box-shadow:0 0 0 3px rgba(113,112,255,.12)}button{width:100%;min-height:48px;margin-top:22px;border:0;border-radius:6px;background:#5e6ad2;color:#fff;font:590 14px inherit;cursor:pointer}button:hover{background:#7170ff}.hint{margin-top:18px;padding:12px 13px;border:1px solid rgba(113,112,255,.18);border-radius:6px;background:rgba(113,112,255,.07);color:#8a8f98;font-size:12px;line-height:1.7}.hint strong{color:#d0d6e0;font:510 12px ui-monospace,SFMono-Regular,Consolas,monospace}.error{min-height:20px;margin-top:11px;color:#ef8b91;font-size:13px;font-weight:510}</style></head><body><main class="card"><div class="logo">BossTransfer</div><p class="sub">登录这台客户端，管理授权和下载设置。</p><form id="form"><label for="user">账号</label><input id="user" autocomplete="username" autofocus><label for="pass">密码</label><input id="pass" type="password" autocomplete="current-password"><button type="submit">登录</button><div class="error" id="error"></div></form><div class="hint">未设置环境引导值的新安装默认账号：<strong>admin</strong><br>默认密码：<strong>password</strong><br>登录后可在“后端接入”中修改。</div></main><script>
document.getElementById('form').addEventListener('submit',async e=>{e.preventDefault();const box=document.getElementById('error');box.textContent='';try{const r=await fetch('/api/v1/client/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:document.getElementById('user').value,password:document.getElementById('pass').value})});const d=await r.json();if(!r.ok)throw new Error(d.message||'登录失败');location.href=d.must_change?'/password':'/';}catch(err){box.textContent=err.message;}});
</script></body></html>`))

var clientPasswordTemplate = template.Must(template.New("client-password").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>设置本地账号 · BossTransfer</title><style>
:root{color-scheme:dark;font-family:InterVariable,Inter,-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;font-feature-settings:"cv01","ss03";color:#f7f8f8;background:#08090a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at 50% -20%,rgba(113,112,255,.16),transparent 42%),#08090a}.card{width:min(450px,100%);padding:32px;background:rgba(255,255,255,.025);border:1px solid rgba(255,255,255,.08);border-radius:10px;box-shadow:0 28px 90px rgba(0,0,0,.42)}h1{margin:0 0 9px;font-size:27px;font-weight:510;letter-spacing:-.55px}.sub{color:#8a8f98;line-height:1.65;margin:0 0 23px;font-size:14px}label{display:block;color:#d0d6e0;font-size:12px;font-weight:510;margin:14px 0 7px}input{width:100%;min-height:48px;border:1px solid rgba(255,255,255,.1);border-radius:6px;padding:0 13px;font:inherit;background:rgba(255,255,255,.025);color:#f7f8f8;outline:none}input:focus{border-color:#7170ff;box-shadow:0 0 0 3px rgba(113,112,255,.12)}button{width:100%;min-height:48px;margin-top:22px;border:0;border-radius:6px;background:#5e6ad2;color:#fff;font:590 14px inherit;cursor:pointer}button:hover{background:#7170ff}.error{min-height:20px;margin-top:11px;color:#ef8b91;font-size:13px;font-weight:510}</style></head><body><main class="card"><h1>先设置自己的登录账号</h1><p class="sub">默认账号只用于首次进入。保存后，客户可用自己的账号管理这台客户端，再填写管理员发放的授权码。</p><form id="form"><label for="user">新账号</label><input id="user" value="{{.Username}}" autocomplete="username" required><label for="current">当前密码</label><input id="current" type="password" autocomplete="current-password" required autofocus><label for="password">新密码</label><input id="password" type="password" autocomplete="new-password" minlength="8" required><button type="submit">保存并重新登录</button><div class="error" id="error"></div></form></main><script>
document.getElementById('form').addEventListener('submit',async e=>{e.preventDefault();const box=document.getElementById('error');box.textContent='';try{const r=await fetch('/api/v1/client/auth/credentials',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:document.getElementById('user').value,current_password:document.getElementById('current').value,password:document.getElementById('password').value})});let d={};try{d=await r.json()}catch{}if(!r.ok)throw new Error(d.message||'保存失败');location.href='/login'}catch(err){box.textContent=err.message}});
</script></body></html>`))

var activationTemplate = template.Must(template.New("activate").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>授权激活 · BossTransfer</title><style>
:root{color-scheme:dark;font-family:InterVariable,Inter,-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif;font-feature-settings:"cv01","ss03";color:#f7f8f8;background:#08090a}*{box-sizing:border-box}body{margin:0;background:#08090a}.bar{height:64px;background:rgba(8,9,10,.9);border-bottom:1px solid rgba(255,255,255,.06);display:flex;align-items:center;justify-content:space-between;padding:0 max(20px,calc((100% - 980px)/2));backdrop-filter:blur(18px)}.logo{font-size:20px;font-weight:590;letter-spacing:-.4px;color:#f7f8f8}.bar button{border:0;background:transparent;color:#8a8f98;cursor:pointer}.wrap{max-width:680px;margin:64px auto;padding:0 18px}.card{background:rgba(255,255,255,.025);border:1px solid rgba(255,255,255,.08);border-radius:10px;box-shadow:0 28px 90px rgba(0,0,0,.34);padding:34px}h1{font-size:31px;font-weight:510;letter-spacing:-.7px;margin:0 0 10px}.lead{color:#8a8f98;font-size:14px;line-height:1.7;margin:0 0 25px}.status{padding:12px 13px;border:1px solid rgba(255,255,255,.08);border-radius:6px;background:rgba(255,255,255,.025);color:#8a8f98;font:400 12px ui-monospace,SFMono-Regular,Consolas,monospace;margin-bottom:20px;overflow-wrap:anywhere}label{display:block;color:#d0d6e0;font-size:12px;font-weight:510;margin:14px 0 7px}input{width:100%;min-height:48px;border:1px solid rgba(255,255,255,.1);border-radius:6px;padding:0 13px;font:inherit;background:rgba(255,255,255,.025);color:#f7f8f8;outline:none}input:focus{border-color:#7170ff;box-shadow:0 0 0 3px rgba(113,112,255,.12)}button.primary{width:100%;min-height:48px;margin-top:22px;border:0;border-radius:6px;background:#5e6ad2;color:#fff;font:590 14px inherit;cursor:pointer}button.primary:hover{background:#7170ff}.message{margin-top:13px;min-height:22px;color:#ef8b91;font-size:13px;font-weight:510}.active{color:#69c77f}.links{margin-top:17px;text-align:center}.links a{color:#828fff;text-decoration:none;font-size:13px;font-weight:510}</style></head><body><header class="bar"><span class="logo">BossTransfer</span><button id="logout">退出登录</button></header><main class="wrap"><section class="card"><h1 id="title">{{if .Activated}}客户端已激活{{else}}激活这台客户端{{end}}</h1><p class="lead">本地账号只用于管理这台设备。授权码由 BossTransfer 管理员签发，用于控制搜索和下载权限。</p><div class="status" id="status">设备编号：{{.InstallationID}}{{if .License.Customer}} · 客户：{{.License.Customer}}{{end}}</div><form id="form"><label for="manager">授权服务器地址</label><input id="manager" value="{{.ManagerURL}}" placeholder="https://你的授权服务器"><label for="code">授权码</label><input id="code" autocomplete="off" placeholder="BT-XXXX-XXXX-XXXX"><label for="label">设备名称</label><input id="label" placeholder="例如：客厅 NAS"><button class="primary" type="submit">在线激活</button><div class="message" id="message"></div></form>{{if .Activated}}<div class="links"><a href="/">进入资源搜索</a></div>{{end}}</section></main><script>
async function api(url,o){const r=await fetch(url,o);let d={};try{d=await r.json()}catch{}if(r.status===401){location.href='/login';throw new Error('请重新登录')}if(!r.ok)throw new Error(d.message||'操作失败');return d}document.getElementById('form').addEventListener('submit',async e=>{e.preventDefault();const m=document.getElementById('message');m.className='message';m.textContent='正在验证授权码…';try{const d=await api('/api/v1/client/license/activate',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({manager_url:document.getElementById('manager').value,code:document.getElementById('code').value,device_label:document.getElementById('label').value})});m.className='message active';m.textContent='激活成功，正在进入资源搜索';setTimeout(()=>location.href='/',600)}catch(err){m.textContent=err.message}});document.getElementById('logout').onclick=async()=>{await api('/api/v1/client/auth/logout',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});location.href='/login'};
</script></body></html>`))
