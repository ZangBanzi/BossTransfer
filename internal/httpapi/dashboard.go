package httpapi

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"time"
)

type DashboardCard struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

type DashboardStep struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Status string `json:"status,omitempty"`
}

type DashboardAction struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Href        string `json:"href,omitempty"`
	Port        string `json:"port,omitempty"`
	CopyValue   string `json:"copy_value,omitempty"`
	CopyCurrent bool   `json:"copy_current,omitempty"`
	Variant     string `json:"variant,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

type DashboardSection struct {
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Actions     []DashboardAction `json:"actions"`
}

type DashboardLink struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

type DashboardData struct {
	Title           string             `json:"title"`
	Product         string             `json:"product"`
	Role            string             `json:"role"`
	Mode            string             `json:"mode"`
	Version         string             `json:"version"`
	ListenAddr      string             `json:"listen_addr"`
	SummaryPath     string             `json:"summary_path"`
	LivePath        string             `json:"live_path"`
	ReadyPath       string             `json:"ready_path"`
	Cards           []DashboardCard    `json:"cards"`
	Steps           []DashboardStep    `json:"steps"`
	PrimaryActions  []DashboardAction  `json:"primary_actions"`
	Sections        []DashboardSection `json:"sections"`
	Links           []DashboardLink    `json:"links"`
	Warnings        []string           `json:"warnings,omitempty"`
	GeneratedAt     string             `json:"generated_at"`
	RefreshInterval int                `json:"refresh_interval_seconds"`
}

func DashboardHandler(data DashboardData) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		data = completeDashboardData(data)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := dashboardTemplate.Execute(w, data); err != nil {
			http.Error(w, "render dashboard", http.StatusInternalServerError)
		}
	}
}

func DashboardSummaryHandler(data DashboardData) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data = completeDashboardData(data)
		WriteJSON(w, http.StatusOK, data)
	}
}

func completeDashboardData(data DashboardData) DashboardData {
	if data.Product == "" {
		data.Product = "BossTransfer"
	}
	if data.Mode == "" {
		data.Mode = "Docker 正式版"
	}
	if data.LivePath == "" {
		data.LivePath = "/api/v1/health/live"
	}
	if data.ReadyPath == "" {
		data.ReadyPath = "/api/v1/health/ready"
	}
	if data.RefreshInterval <= 0 {
		data.RefreshInterval = 5
	}
	data.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	return data
}

var dashboardTemplate = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"js": func(value string) template.JS {
		encoded, _ := json.Marshal(value)
		return template.JS(encoded)
	},
	"itoa": func(value int) string {
		return strconv.Itoa(value)
	},
	"add": func(left int, right int) int {
		return left + right
	},
}).Parse(`{{define "action"}}
<article class="action-card {{.Variant}}{{if .Disabled}} is-disabled{{end}}">
  {{if .Disabled}}
  <button class="button disabled" type="button" disabled>{{.Label}}</button>
  {{else if .Port}}
  <a class="button {{.Variant}}" href="#" data-port="{{.Port}}">{{.Label}}</a>
  {{else if .CopyCurrent}}
  <button class="button {{.Variant}}" type="button" data-copy-current="true">{{.Label}}</button>
  {{else if .CopyValue}}
  <button class="button {{.Variant}}" type="button" data-copy="{{.CopyValue}}">{{.Label}}</button>
  {{else if .Href}}
  <a class="button {{.Variant}}" href="{{.Href}}" target="_blank" rel="noreferrer">{{.Label}}</a>
  {{else}}
  <button class="button {{.Variant}}" type="button">{{.Label}}</button>
  {{end}}
  {{if .Description}}<p>{{.Description}}</p>{{end}}
</article>
{{end}}
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f4f7fb;
      --panel: #ffffff;
      --text: #172033;
      --muted: #667085;
      --line: #e4e7ec;
      --soft: #f8fafc;
      --brand: #2563eb;
      --brand-dark: #1d4ed8;
      --ok: #059669;
      --warn: #d97706;
      --fail: #dc2626;
      --shadow: 0 18px 48px rgba(15, 23, 42, .08);
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Microsoft YaHei", sans-serif;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background:
        radial-gradient(circle at top left, #dbeafe 0, transparent 28rem),
        linear-gradient(180deg, #f8fbff 0, var(--bg) 50%, #eef2f7 100%);
      color: var(--text);
    }
    main {
      width: min(1180px, calc(100% - 32px));
      margin: 0 auto;
      padding: 28px 0 52px;
    }
    h1, h2, h3, p { margin-top: 0; }
    h1 { margin-bottom: 10px; font-size: clamp(30px, 5vw, 46px); letter-spacing: -.03em; }
    h2 { margin-bottom: 8px; font-size: 21px; }
    h3 { margin-bottom: 7px; font-size: 15px; }
    .muted, .lead, .hint, .step p, .check-message, .action-card p, .section-desc {
      color: var(--muted);
      line-height: 1.68;
      font-size: 14px;
    }
    .lead { max-width: 760px; margin-bottom: 0; font-size: 16px; }
    .hero {
      display: grid;
      gap: 20px;
      padding: 30px;
      border: 1px solid rgba(37, 99, 235, .16);
      border-radius: 28px;
      background: rgba(255,255,255,.94);
      box-shadow: var(--shadow);
    }
    .hero-meta, .cards, .status-grid, .links, .primary-actions, .section-actions, .status-layout, .warning-list {
      display: grid;
      gap: 12px;
    }
    .hero-meta {
      grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    }
    .pill {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      width: fit-content;
      padding: 8px 12px;
      border-radius: 999px;
      border: 1px solid var(--line);
      background: #fff;
      color: var(--muted);
      font-size: 13px;
      white-space: nowrap;
    }
    .dot {
      flex: 0 0 auto;
      width: 9px;
      height: 9px;
      border-radius: 999px;
      background: var(--warn);
    }
    .dot.ok { background: var(--ok); }
    .dot.fail { background: var(--fail); }
    .primary-actions {
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    }
    .status-layout {
      grid-template-columns: minmax(0, 1.4fr) minmax(280px, .9fr);
      align-items: stretch;
    }
    section {
      margin-top: 18px;
      padding: 22px;
      border: 1px solid var(--line);
      border-radius: 22px;
      background: rgba(255,255,255,.96);
      box-shadow: 0 10px 30px rgba(15, 23, 42, .05);
    }
    .section-header {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: 16px;
      margin-bottom: 14px;
    }
    .section-header p { margin-bottom: 0; }
    .cards {
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    }
    .card, .action-card, .check, .link-card {
      border: 1px solid var(--line);
      border-radius: 16px;
      background: #fff;
    }
    .card {
      min-height: 124px;
      padding: 18px;
    }
    .card .value {
      margin: 0 0 8px;
      font-size: 20px;
      font-weight: 850;
      word-break: break-word;
    }
    .action-card {
      display: grid;
      gap: 10px;
      align-content: start;
      min-height: 126px;
      padding: 16px;
    }
    .action-card.primary {
      border-color: rgba(37, 99, 235, .24);
      background: linear-gradient(180deg, #ffffff 0, #f5f8ff 100%);
    }
    .action-card.is-disabled {
      background: #f8fafc;
      opacity: .86;
    }
    .action-card p { margin-bottom: 0; }
    .button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 44px;
      padding: 10px 14px;
      border-radius: 12px;
      border: 1px solid var(--brand);
      background: var(--brand);
      color: #fff;
      font-weight: 800;
      text-decoration: none;
      cursor: pointer;
      text-align: center;
      width: fit-content;
      max-width: 100%;
    }
    .button.secondary {
      background: #fff;
      color: var(--brand);
    }
    .button.ghost {
      border-color: var(--line);
      background: var(--soft);
      color: var(--text);
    }
    .button.disabled, .button:disabled {
      cursor: not-allowed;
      border-color: #d0d5dd;
      background: #eef2f7;
      color: #98a2b3;
    }
    .button:hover {
      background: var(--brand-dark);
      color: #fff;
    }
    .button.disabled:hover, .button:disabled:hover {
      background: #eef2f7;
      color: #98a2b3;
    }
    .section-actions {
      grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
    }
    .workflow {
      display: grid;
      gap: 12px;
      grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
      counter-reset: step;
    }
    .step {
      position: relative;
      display: grid;
      gap: 10px;
      padding: 18px;
      border: 1px solid var(--line);
      border-radius: 16px;
      background: #fff;
    }
    .step-top {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
    }
    .step-number {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 34px;
      height: 34px;
      border-radius: 10px;
      background: #eff6ff;
      color: var(--brand);
      font-weight: 850;
    }
    .step-status {
      display: inline-flex;
      align-items: center;
      min-height: 26px;
      padding: 0 10px;
      border-radius: 999px;
      background: #fef3c7;
      color: #92400e;
      font-size: 12px;
      font-weight: 850;
      white-space: nowrap;
    }
    .check {
      display: grid;
      grid-template-columns: auto 1fr auto;
      gap: 12px;
      align-items: start;
      padding: 14px;
    }
    .check + .check { margin-top: 10px; }
    .badge {
      display: inline-flex;
      align-items: center;
      height: 26px;
      padding: 0 10px;
      border-radius: 999px;
      font-size: 12px;
      font-weight: 850;
      text-transform: uppercase;
      background: #fef3c7;
      color: #92400e;
    }
    .badge.ok { background: #dcfce7; color: #166534; }
    .badge.fail { background: #fee2e2; color: #991b1b; }
    .links {
      grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    }
    .link-card {
      display: grid;
      gap: 4px;
      padding: 14px;
      color: var(--brand);
      text-decoration: none;
      font-weight: 800;
      overflow-wrap: anywhere;
    }
    .warning {
      margin: 0;
      padding: 12px 14px;
      border-radius: 13px;
      background: #fff7ed;
      color: #9a3412;
      border: 1px solid #fed7aa;
      line-height: 1.62;
    }
    .toast {
      position: fixed;
      left: 50%;
      bottom: 22px;
      transform: translateX(-50%) translateY(20px);
      padding: 10px 14px;
      border-radius: 999px;
      background: #111827;
      color: #fff;
      box-shadow: var(--shadow);
      opacity: 0;
      pointer-events: none;
      transition: opacity .18s ease, transform .18s ease;
      z-index: 10;
      font-size: 14px;
    }
    .toast.show {
      opacity: 1;
      transform: translateX(-50%) translateY(0);
    }
    footer {
      margin-top: 20px;
      color: var(--muted);
      font-size: 13px;
      text-align: center;
    }
    @media (max-width: 720px) {
      main { width: min(100% - 20px, 1180px); padding-top: 18px; }
      .hero, section { padding: 18px; border-radius: 18px; }
      .section-header { display: grid; }
      .status-layout { grid-template-columns: 1fr; }
      .check { grid-template-columns: auto 1fr; }
      .check .badge { grid-column: 2; width: fit-content; }
      .button { width: 100%; }
    }
  </style>
</head>
<body>
  <main>
    <header class="hero">
      <div>
        <h1>{{.Title}}</h1>
        <p class="lead">{{.Role}}</p>
      </div>
      <div class="hero-meta">
        <span class="pill"><span id="status-dot" class="dot"></span><span id="status-text">正在检查</span></span>
        <span class="pill">运行模式：{{.Mode}}</span>
        <span class="pill">版本：{{.Version}}</span>
        <span class="pill">监听：{{.ListenAddr}}</span>
      </div>
    </header>

    <section>
      <div class="section-header">
        <div>
          <h2>三步操作路径</h2>
          <p class="section-desc">先确认容器在线，再进入客户端页面，最后根据页面提示完成目录配置和文件保存。</p>
        </div>
      </div>
      <div class="workflow">
        {{range $index, $step := .Steps}}
        <article class="step">
          <div class="step-top">
            <span class="step-number">{{itoa (add $index 1)}}</span>
            {{if $step.Status}}<span class="step-status">{{$step.Status}}</span>{{end}}
          </div>
          <div>
            <h3>{{$step.Title}}</h3>
            <p>{{$step.Body}}</p>
          </div>
        </article>
        {{end}}
      </div>
    </section>

    {{if .PrimaryActions}}
    <section>
      <div class="section-header">
        <div>
          <h2>主要操作</h2>
          <p class="section-desc">这里放当前最该点击的按钮：打开另一端、刷新状态、复制地址或查看关键设置。</p>
        </div>
      </div>
      <div class="primary-actions">
        {{range .PrimaryActions}}{{template "action" .}}{{end}}
      </div>
    </section>
    {{end}}

    <section>
      <div class="section-header">
        <div>
          <h2>服务状态</h2>
          <p class="section-desc">这里直接读取后端 ready 接口。绿色表示已就绪，黄色表示可访问但业务能力还没接入，红色才是故障。</p>
        </div>
        <button class="button secondary" type="button" id="refresh">刷新服务状态</button>
      </div>
      <div class="status-layout">
        <div id="checks" class="status-grid">
          <p class="hint">正在读取健康检查结果。</p>
        </div>
        {{if .Warnings}}
        <div class="warning-list">
          {{range .Warnings}}<p class="warning">{{.}}</p>{{end}}
        </div>
        {{end}}
      </div>
    </section>

    <section>
      <div class="section-header">
        <div>
          <h2>运行信息</h2>
          <p class="section-desc">这些信息用于确认你打开的是哪个服务、哪个端口和哪个配置。</p>
        </div>
      </div>
      <div class="cards">
        {{range .Cards}}
        <article class="card">
          <h3>{{.Title}}</h3>
          <p class="value">{{.Value}}</p>
          {{if .Hint}}<p class="hint">{{.Hint}}</p>{{end}}
        </article>
        {{end}}
      </div>
    </section>

    {{range .Sections}}
    <section>
      <div class="section-header">
        <div>
          <h2>{{.Title}}</h2>
          {{if .Description}}<p class="section-desc">{{.Description}}</p>{{end}}
        </div>
      </div>
      <div class="section-actions">
        {{range .Actions}}{{template "action" .}}{{end}}
      </div>
    </section>
    {{end}}

    <section>
      <div class="section-header">
        <div>
          <h2>JSON 诊断入口</h2>
          <p class="section-desc">如果页面打开正常但状态异常，先看 ready JSON，再看 Docker 日志。</p>
        </div>
      </div>
      <div class="links">
        {{range .Links}}<a class="link-card" href="{{.Href}}" target="_blank" rel="noreferrer">{{.Label}}</a>{{end}}
      </div>
    </section>

    <footer>页面由本地 BossTransfer 服务生成，不依赖外部 CDN。状态每 {{.RefreshInterval}} 秒刷新一次。</footer>
  </main>
  <div class="toast" id="toast">已复制</div>

  <script>
    const readyPath = {{js .ReadyPath}};
    const refreshInterval = {{.RefreshInterval}} * 1000;
    const checksEl = document.querySelector('#checks');
    const statusText = document.querySelector('#status-text');
    const statusDot = document.querySelector('#status-dot');
    const refreshButton = document.querySelector('#refresh');
    const toast = document.querySelector('#toast');

    function setStatus(status) {
      statusDot.className = 'dot';
      if (status === 'ready') {
        statusDot.classList.add('ok');
        statusText.textContent = '服务就绪';
        return;
      }
      if (status === 'not_ready') {
        statusDot.classList.add('fail');
        statusText.textContent = '服务异常';
        return;
      }
      statusText.textContent = '可访问，部分能力待接入';
    }

    function renderChecks(payload) {
      setStatus(payload.status || 'degraded');
      const checks = Array.isArray(payload.checks) ? payload.checks : [];
      if (checks.length === 0) {
        checksEl.innerHTML = '<p class="hint">没有返回检查项。</p>';
        return;
      }
      checksEl.innerHTML = checks.map((check) => {
        const status = String(check.status || 'warn');
        const badgeClass = status === 'ok' ? 'ok' : (status === 'fail' ? 'fail' : '');
        const message = check.message ? '<p class="check-message">' + escapeHtml(check.message) + '</p>' : '';
        return '<article class="check">' +
          '<span class="dot ' + badgeClass + '"></span>' +
          '<div><h3>' + escapeHtml(check.name || 'check') + '</h3>' + message + '</div>' +
          '<span class="badge ' + badgeClass + '">' + escapeHtml(status) + '</span>' +
        '</article>';
      }).join('');
    }

    function escapeHtml(value) {
      return String(value).replace(/[&<>"']/g, (char) => ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#039;'
      }[char]));
    }

    async function refresh() {
      try {
        const response = await fetch(readyPath, { cache: 'no-store' });
        const payload = await response.json();
        renderChecks(payload);
      } catch (error) {
        statusDot.className = 'dot fail';
        statusText.textContent = '读取状态失败';
        checksEl.innerHTML = '<p class="warning">无法读取健康检查结果，请确认容器正在运行并且端口映射正确。</p>';
      }
    }

    function buildPortLinks() {
      document.querySelectorAll('[data-port]').forEach((link) => {
        const port = link.getAttribute('data-port');
        link.href = window.location.protocol + '//' + window.location.hostname + ':' + port + '/';
      });
    }

    function showToast(message) {
      toast.textContent = message;
      toast.classList.add('show');
      window.setTimeout(() => toast.classList.remove('show'), 1600);
    }

    async function copyText(value) {
      try {
        await navigator.clipboard.writeText(value);
        showToast('已复制');
      } catch (error) {
        showToast('复制失败，请手动复制');
      }
    }

    document.querySelectorAll('[data-copy]').forEach((button) => {
      button.addEventListener('click', () => copyText(button.getAttribute('data-copy')));
    });
    document.querySelectorAll('[data-copy-current]').forEach((button) => {
      button.addEventListener('click', () => copyText(window.location.href));
    });

    buildPortLinks();
    refreshButton.addEventListener('click', refresh);
    refresh();
    window.setInterval(refresh, refreshInterval);
  </script>
</body>
</html>`))
