package controller

import (
	"html/template"
	"strings"

	"github.com/example/tracepulse/model"
	"github.com/example/tracepulse/view"
)

// cellVM 可折叠单元格的展示模型。
type cellVM struct {
	Text  string
	Short string
	Long  bool
}

// pageTemplates 内置页面模板。
//
// 设计约束：
//   - 零外部依赖：不引 CDN、不引字体，断网环境下部署在路由器上也照样能用；
//   - 移动端优先：窄屏下表格自动转成卡片流，字段用 data-label 标注；
//   - 长内容折叠：<details> 原生实现，不需要 JS，在老手机浏览器上也不掉链子；
//   - 内容全部走 html/template 转义，链路内容里的尖括号不会破坏页面结构。
var pageTemplates = template.Must(template.New("pages").Funcs(template.FuncMap{
	"cell": func(s string, max int) cellVM {
		return cellVM{
			Text:  s,
			Short: view.Truncate(s, max),
			Long:  view.IsLong(s, max),
		}
	},
	"dur": view.FormatDuration,
	"tf":  view.FormatTime,
	"inc": func(i int) int { return i + 1 },
	"isErrStatus": func(s string) bool {
		return s == model.TraceStatusError || s == model.TraceStatusTimeout
	},
	// isAbsURL 只有 http(s) 开头的 url 才渲染成可点链接。
	// 相对路径（如 "/api/order"）点了会打到 tracepulse 自己的路由上，纯属误导。
	"isAbsURL": func(s string) bool {
		return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
	},
}).Parse(pageTemplatesSrc))

const pageTemplatesSrc = `
{{define "head"}}<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
:root{
  --bg:#f6f8fa; --fg:#1f2328; --muted:#57606a; --border:#d0d7de; --card:#ffffff;
  --accent:#0969da; --err:#cf222e; --warn:#9a6700; --ok:#1a7f37; --chip:#eaeef2;
  --errbg:#ffebe9; --warnbg:#fff8c5; --okbg:#dafbe1;
}
@media (prefers-color-scheme: dark){
  :root{
    --bg:#0d1117; --fg:#e6edf3; --muted:#8b949e; --border:#30363d; --card:#161b22;
    --accent:#58a6ff; --err:#f85149; --warn:#d29922; --ok:#3fb950; --chip:#21262d;
    --errbg:#3d1418; --warnbg:#3b2f10; --okbg:#12261e;
  }
}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--fg);
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",
    "PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;
  font-size:14px;line-height:1.6;-webkit-font-smoothing:antialiased}
a{color:var(--accent);text-decoration:none}
a:hover{text-decoration:underline}
code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace}

.topbar{display:flex;align-items:center;gap:16px;padding:10px 16px;background:var(--card);
  border-bottom:1px solid var(--border);position:sticky;top:0;z-index:10}
.brand{font-weight:700;font-size:16px;color:var(--fg)}
.brand span{color:var(--accent)}
.topbar nav{margin-left:auto;display:flex;gap:14px;font-size:13px}
.badge-live{padding:1px 8px;border-radius:10px;background:var(--chip);color:var(--muted);font-size:11px}

main{max-width:1280px;margin:0 auto;padding:14px 12px 40px}
.card{background:var(--card);border:1px solid var(--border);border-radius:10px;
  padding:14px 16px;margin-bottom:14px}
.card > h2{margin:0 0 10px;font-size:15px;font-weight:600}
.muted{color:var(--muted)}
.empty{padding:26px 0;text-align:center;color:var(--muted)}

/* ---------- 表单 ---------- */
.filters{display:grid;grid-template-columns:repeat(auto-fill,minmax(190px,1fr));gap:10px 12px}
.field{display:flex;flex-direction:column;gap:4px;min-width:0}
.field label{font-size:12px;color:var(--muted)}
.field input,.field select{width:100%;padding:7px 9px;font-size:13px;color:var(--fg);
  background:var(--bg);border:1px solid var(--border);border-radius:6px;
  font-family:inherit;-webkit-appearance:none;appearance:none}
.field input:focus,.field select:focus{outline:2px solid var(--accent);outline-offset:-1px;border-color:var(--accent)}
/* 操作行与快捷行横跨整个筛选网格，避免被挤在 190px 的单列里折行堆叠 */
.actions{grid-column:1/-1;display:flex;flex-wrap:nowrap;gap:8px;align-items:center;margin-top:12px;overflow-x:auto}
button,.btn{display:inline-block;padding:7px 14px;font-size:13px;font-family:inherit;
  color:var(--fg);background:var(--bg);border:1px solid var(--border);border-radius:6px;cursor:pointer}
button:hover,.btn:hover{text-decoration:none;border-color:var(--muted)}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
.quick{grid-column:1/-1;display:flex;flex-wrap:nowrap;gap:6px;margin-top:10px;align-items:center;
  overflow-x:auto;padding-bottom:4px;-webkit-overflow-scrolling:touch}
/* 内部禁止折行：否则窄容器里「最近 30 分钟」会断成两行，看起来像竖排 */
.quick > span,.quick a{white-space:nowrap;flex:0 0 auto}
.quick a{padding:2px 10px;border:1px solid var(--border);border-radius:20px;
  background:var(--bg);color:var(--muted);font-size:12px}
.quick a:hover{border-color:var(--accent);color:var(--accent);text-decoration:none}

.goto{display:flex;gap:8px;margin-bottom:14px}
.goto input{flex:1;min-width:0;padding:9px 11px;font-size:14px;color:var(--fg);background:var(--bg);
  border:1px solid var(--border);border-radius:7px;font-family:ui-monospace,Menlo,Consolas,monospace}

/* ---------- 表格 ---------- */
.table-wrap{overflow-x:auto;-webkit-overflow-scrolling:touch}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{padding:7px 9px;border-bottom:1px solid var(--border);text-align:left;vertical-align:top}
thead th{position:sticky;top:0;background:var(--card);color:var(--muted);
  font-weight:600;font-size:12px;white-space:nowrap;z-index:1}
tbody tr:hover{background:var(--bg)}
td .wrap{word-break:break-all;white-space:pre-wrap}

.lvl{display:inline-block;padding:0 7px;border-radius:20px;font-size:11px;
  line-height:18px;font-weight:600;white-space:nowrap;background:var(--chip);color:var(--muted)}
.lvl-error,.lvl-fatal{background:var(--errbg);color:var(--err)}
.lvl-warn{background:var(--warnbg);color:var(--warn)}
.lvl-info,.lvl-debug,.lvl-trace{background:var(--chip);color:var(--muted)}
.status{display:inline-block;padding:0 8px;border-radius:20px;font-size:11px;
  line-height:19px;font-weight:600;white-space:nowrap}
.status-ok{background:var(--okbg);color:var(--ok)}
.status-error,.status-timeout{background:var(--errbg);color:var(--err)}
.status-warn{background:var(--warnbg);color:var(--warn)}
.row-err{background:var(--errbg)}
.row-err:hover{filter:brightness(.97)}
.gap-slow{color:var(--warn);font-weight:600}

/* ---------- 折叠 ---------- */
details.fold{margin:0}
details.fold > summary{cursor:pointer;list-style:none;display:inline}
details.fold > summary::-webkit-details-marker{display:none}
details.fold > summary::after{content:" ▸";color:var(--accent);font-size:11px}
details.fold[open] > summary::after{content:" ▾"}
.fold-body{margin-top:6px;padding:8px 10px;background:var(--bg);border:1px solid var(--border);
  border-radius:6px;white-space:pre-wrap;word-break:break-all;font-size:12px}
table.kv{width:auto;min-width:180px;font-size:12px;margin:0}
table.kv th{width:1%;white-space:nowrap;color:var(--muted);font-weight:500;
  border:none;padding:2px 12px 2px 0;background:transparent;position:static}
table.kv td{border:none;padding:2px 0;word-break:break-all}

/* ---------- 详情页头 ---------- */
.trace-head{display:flex;flex-wrap:wrap;gap:8px;align-items:center;margin-bottom:10px}
.trace-id{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:14px;
  background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:3px 8px;
  word-break:break-all;flex:1;min-width:200px}
.meta{display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:8px 16px;margin-top:10px}
.meta div{font-size:13px}
.meta span{display:block;font-size:11px;color:var(--muted)}
/* 接口名是一长串 URL，独占一行才不会被挤成竖排 */
.meta .meta-url{grid-column:1/-1}
.urlbox{display:inline-block;word-break:break-all;font-size:12px;
  font-family:ui-monospace,Menlo,Consolas,monospace}
/* 列表「接口名」列：截断展示，title 悬停看全文 */
.row-url{margin-top:3px;font-size:11px;color:var(--muted);word-break:break-all}
.errbox{margin-top:12px;padding:10px 12px;border-radius:8px;background:var(--errbg);
  border:1px solid var(--err);color:var(--err);white-space:pre-wrap;word-break:break-all;font-size:13px}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin-top:10px}
.chip{padding:1px 9px;border-radius:20px;background:var(--chip);color:var(--muted);font-size:11px}

.alert{padding:10px 12px;border-radius:8px;margin-bottom:12px;font-size:13px}
.alert-error{background:var(--errbg);border:1px solid var(--err);color:var(--err)}

/* ---------- 分页 ---------- */
.pager{display:flex;flex-wrap:wrap;gap:6px;align-items:center;margin-top:14px}
.pager a,.pager span{padding:4px 11px;border:1px solid var(--border);border-radius:6px;
  background:var(--bg);font-size:13px;color:var(--fg)}
.pager a.cur{background:var(--accent);border-color:var(--accent);color:#fff;font-weight:600}
.pager a:hover{text-decoration:none;border-color:var(--accent)}
.pager .info{color:var(--muted);border:none;background:transparent}

/* ---------- 移动端：表格转卡片 ---------- */
@media (max-width: 860px){
  main{padding:10px 8px 32px}
  .card{padding:12px;border-radius:8px}
  table.timeline thead{display:none}
  table.timeline,table.timeline tbody,table.timeline tr,table.timeline td{display:block;width:100%}
  table.timeline tr{border:1px solid var(--border);border-radius:8px;margin-bottom:10px;
    padding:8px 10px;background:var(--card)}
  table.timeline tr.row-err{border-color:var(--err)}
  table.timeline td{border:none;padding:3px 0;display:flex;gap:10px;align-items:flex-start}
  table.timeline td::before{content:attr(data-label);flex:0 0 62px;color:var(--muted);
    font-size:11px;padding-top:1px}
  table.timeline td > *{flex:1;min-width:0}
  table.timeline td.seq-cell{font-size:12px;color:var(--muted)}
  table.list thead{display:none}
  table.list,table.list tbody,table.list tr,table.list td{display:block;width:100%}
  table.list tr{border:1px solid var(--border);border-radius:8px;margin-bottom:10px;
    padding:8px 10px;background:var(--card)}
  table.list td{border:none;padding:3px 0;display:flex;gap:10px}
  table.list td::before{content:attr(data-label);flex:0 0 68px;color:var(--muted);font-size:11px}
  table.list td > *{flex:1;min-width:0}
}
</style>
</head>
<body>
<header class="topbar">
  <a class="brand" href="/traces">Trace<span>Pulse</span></a>
  <nav>
    <a href="/traces">检索</a>
    <a href="/health">健康</a>
  </nav>
</header>
<main>
{{end}}

{{define "foot"}}
</main>
<script>
(function(){
  document.addEventListener('click', function(e){
    var btn = e.target.closest ? e.target.closest('[data-copy]') : null;
    if(!btn) return;
    var text = btn.getAttribute('data-copy');
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text);
    } else {
      var ta = document.createElement('textarea');
      ta.value = text; document.body.appendChild(ta); ta.select();
      try { document.execCommand('copy'); } catch (err) {}
      document.body.removeChild(ta);
    }
    var old = btn.textContent;
    btn.textContent = '已复制';
    setTimeout(function(){ btn.textContent = old; }, 1200);
  });
})();
</script>
</body>
</html>
{{end}}

{{define "cell"}}{{if .Long}}<details class="fold"><summary>{{.Short}}</summary><div class="fold-body">{{.Text}}</div></details>{{else}}{{.Text}}{{end}}{{end}}

{{define "detail.html"}}{{template "head" .}}
{{if .Error}}<div class="alert alert-error">{{.Error}}</div>{{end}}

{{if .Found}}
<section class="card">
  <div class="trace-head">
    <code class="trace-id" id="tid">{{.TraceID}}</code>
    <button class="copy" data-copy="{{.TraceID}}">复制 trace_id</button>
    <span class="status status-{{.Summary.Status}}">{{.Summary.Status}}</span>
  </div>

  <div class="meta">
    <div><span>Service</span>{{if .Summary.Service}}{{.Summary.Service}}{{else}}<em class="muted">-</em>{{end}}</div>
    <div class="meta-url"><span>接口名</span>{{if .Summary.URL}}{{if isAbsURL .Summary.URL}}<a class="urlbox" href="{{.Summary.URL}}" target="_blank" rel="noopener noreferrer">{{.Summary.URL}}</a>{{else}}<code class="urlbox">{{.Summary.URL}}</code>{{end}}{{else}}<em class="muted">-</em>{{end}}</div>
    <div><span>Duration</span>{{.Summary.Duration}}</div>
    <div><span>Start</span>{{.Summary.Start}}</div>
    <div><span>End</span>{{.Summary.End}}</div>
    <div><span>Events</span>{{.Summary.EventCount}}</div>
    <div><span>级别分布</span>
      {{if .Summary.LevelStats}}{{range .Summary.LevelStats}}<span class="chip">{{.Level}} × {{.Count}}</span> {{end}}{{else}}-{{end}}
    </div>
  </div>

  {{if .Summary.ErrorMessage}}<div class="errbox">{{.Summary.ErrorMessage}}</div>{{end}}
</section>

<section class="card">
  <h2>时间线 · {{len .Events}} 步</h2>
  {{if .Events}}
  <div class="table-wrap">
  <table class="timeline">
    <thead>
      <tr>
        <th>#</th><th>时间</th><th>步骤耗时</th><th>级别</th>
        <th>模块</th><th>事件</th><th>消息</th><th>KV 参数</th><th>错误消息</th>
      </tr>
    </thead>
    <tbody>
    {{range .Events}}
      <tr class="{{if .IsError}}row-err{{end}}">
        <td class="seq-cell" data-label="#">{{.Seq}}</td>
        <td data-label="时间"><span title="{{.FullTime}}">{{.Clock}}</span></td>
        <td data-label="步骤耗时">{{if .Gap}}<span class="{{if .GapSlow}}gap-slow{{end}}" title="与上一事件的时间间隔">{{.Gap}}</span>{{else}}<span class="muted">-</span>{{end}}</td>
        <td data-label="级别"><span class="lvl lvl-{{.Level}}">{{.Level}}</span></td>
        <td data-label="模块">{{.Module}}</td>
        <td data-label="事件">{{.Event}}</td>
        <td data-label="消息"><div class="wrap">{{template "cell" (cell .Message 160)}}</div></td>
        <td data-label="KV 参数">
          {{if .Params}}
            <details class="fold">
              <summary>{{len .Params}} 项</summary>
              <div class="fold-body">
                <table class="kv">
                  {{range .Params}}<tr><th>{{.Key}}</th><td>{{.Value}}</td></tr>{{end}}
                </table>
              </div>
            </details>
          {{else}}<span class="muted">-</span>{{end}}
        </td>
        <td data-label="错误消息">
          {{if .ErrorMsg}}<div class="wrap">{{template "cell" (cell .ErrorMsg 160)}}</div>{{else}}<span class="muted">-</span>{{end}}
        </td>
      </tr>
    {{end}}
    </tbody>
  </table>
  </div>
  {{else}}
  <div class="empty">该链路暂无事件</div>
  {{end}}
</section>
{{end}}
{{template "foot" .}}{{end}}

{{define "list.html"}}{{template "head" .}}

<form class="goto" method="get" action="/traces">
  <input name="goto_trace_id" placeholder="粘贴 trace_id 回车直达（告警排查最常用）"
         autocomplete="off" spellcheck="false">
  <button class="primary" type="submit">打开链路</button>
</form>

<section class="card">
  <h2>条件检索</h2>
  <form class="filters" method="get" action="/traces">
    <div class="field">
      <label for="f-service">服务</label>
      <input id="f-service" name="service" value="{{.Form.Service}}" placeholder="例如 api-gateway">
    </div>
    <div class="field">
      <label for="f-trace">trace_id（支持作为过滤条件）</label>
      <input id="f-trace" name="trace_id" value="{{.Form.TraceID}}" placeholder="精确匹配" spellcheck="false">
    </div>
    <div class="field">
      <label for="f-kw">关键词（模糊搜索）</label>
      <input id="f-kw" name="keyword" value="{{.Form.Keyword}}" placeholder="匹配 trace_id / 服务 / 消息 / 参数 / 错误">
    </div>
    <div class="field">
      <label for="f-status">状态</label>
      <select id="f-status" name="status">
        <option value="">全部</option>
        <option value="ok"{{if eq .Form.Status "ok"}} selected{{end}}>ok</option>
        <option value="error"{{if eq .Form.Status "error"}} selected{{end}}>error</option>
        <option value="warn"{{if eq .Form.Status "warn"}} selected{{end}}>warn</option>
        <option value="timeout"{{if eq .Form.Status "timeout"}} selected{{end}}>timeout</option>
      </select>
    </div>
    <div class="field">
      <label for="f-level">级别（链路中出现过）</label>
      <select id="f-level" name="level">
        <option value="">全部</option>
        <option value="error"{{if eq .Form.Level "error"}} selected{{end}}>error</option>
        <option value="warn"{{if eq .Form.Level "warn"}} selected{{end}}>warn</option>
        <option value="info"{{if eq .Form.Level "info"}} selected{{end}}>info</option>
        <option value="debug"{{if eq .Form.Level "debug"}} selected{{end}}>debug</option>
        <option value="trace"{{if eq .Form.Level "trace"}} selected{{end}}>trace</option>
      </select>
    </div>
    <div class="field">
      <label for="f-module">模块</label>
      <input id="f-module" name="module" value="{{.Form.Module}}" placeholder="例如 order">
    </div>
    <div class="field">
      <label for="f-haserr">是否含错误</label>
      <select id="f-haserr" name="has_error">
        <option value="">全部</option>
        <option value="true"{{if eq .Form.HasError "true"}} selected{{end}}>仅错误链路</option>
        <option value="false"{{if eq .Form.HasError "false"}} selected{{end}}>仅正常链路</option>
      </select>
    </div>
    <div class="field">
      <label for="f-dur">耗时 ≥ (ms)（查慢调用）</label>
      <input id="f-dur" name="min_duration_ms" value="{{.Form.MinDuration}}" placeholder="例如 1000" inputmode="numeric">
    </div>
    <div class="field">
      <label for="f-start">开始时间</label>
      <input id="f-start" name="start_time" value="{{.Form.StartRaw}}" placeholder="2026-01-02 15:04:05 或 1h">
    </div>
    <div class="field">
      <label for="f-end">结束时间</label>
      <input id="f-end" name="end_time" value="{{.Form.EndRaw}}" placeholder="留空表示现在">
    </div>
    <div class="field">
      <label for="f-size">每页条数</label>
      <select id="f-size" name="page_size">
        {{range $s := .PageSizeOptions}}
        <option value="{{$s}}"{{if eq $.Form.PageSize $s}} selected{{end}}>{{$s}}</option>
        {{end}}
      </select>
    </div>

    <div class="actions">
      <button class="primary" type="submit">搜索</button>
      <a class="btn" href="/traces">重置</a>
    </div>
    <div class="quick">
      <span class="muted">快捷时间范围：</span>
      <a href="{{.QuickURL "30m"}}">最近 30 分钟</a>
      <a href="{{.QuickURL "1h"}}">最近 1 小时</a>
      <a href="{{.QuickURL "24h"}}">最近 24 小时</a>
      <a href="{{.QuickURL "7d"}}">最近 7 天</a>
      <span class="muted">·</span>
      <a href="{{.QuickURL "24h" "status" "error"}}">最近 1 天错误</a>
      <a href="{{.QuickURL "24h" "status" "timeout"}}">最近 1 天超时</a>
      <a href="{{.QuickURL "24h" "level" "warn"}}">最近 1 天告警</a>
    </div>
  </form>
</section>

{{if .Error}}<div class="alert alert-error">{{.Error}}</div>{{end}}

{{if .Queried}}
<section class="card">
  <h2>结果{{if .Result}} · 共 {{.Result.Total}} 条{{end}}</h2>
  {{if .Rows}}
  <div class="table-wrap">
  <table class="list">
    <thead>
      <tr>
        <th>Trace ID</th><th>状态</th><th>服务</th><th>接口名</th><th>开始时间</th>
        <th>耗时</th><th>事件数</th><th>错误信息</th><th></th>
      </tr>
    </thead>
    <tbody>
    {{range .Rows}}
      <tr class="{{if .IsError}}row-err{{end}}">
        <td data-label="Trace ID"><code>{{.Trace.TraceID}}</code></td>
        <td data-label="状态"><span class="status status-{{.Trace.Status}}">{{.Trace.Status}}</span></td>
        <td data-label="服务">{{if .Trace.ServiceName}}{{.Trace.ServiceName}}{{else}}<span class="muted">-</span>{{end}}</td>
        <td data-label="接口名">{{if .Trace.URL}}<div class="wrap" title="{{.Trace.URL}}">{{.URLShort}}</div>{{else}}<span class="muted">-</span>{{end}}</td>
        <td data-label="开始时间">{{.Start}}</td>
        <td data-label="耗时">{{.Duration}}</td>
        <td data-label="事件数">{{.Trace.EventCount}}</td>
        <td data-label="错误信息">{{if .Trace.ErrorMessage}}<div class="wrap">{{template "cell" (cell .Trace.ErrorMessage 90)}}</div>{{else}}<span class="muted">-</span>{{end}}</td>
        <td data-label=""><a href="/trace/{{.Trace.TraceID}}">查看详情</a></td>
      </tr>
    {{end}}
    </tbody>
  </table>
  </div>

  <div class="pager">
    {{if .PrevPage}}<a href="{{.PageURL .PrevPage}}">上一页</a>{{end}}
    {{range .Pages}}
      {{if eq . $.Result.Page}}<a class="cur" href="{{$.PageURL .}}">{{.}}</a>
      {{else}}<a href="{{$.PageURL .}}">{{.}}</a>{{end}}
    {{end}}
    {{if .NextPage}}<a href="{{.PageURL .NextPage}}">下一页</a>{{end}}
    <span class="info">第 {{.Result.Page}} / {{.Result.TotalPages}} 页</span>
  </div>
  {{else}}
  <div class="empty">没有匹配的链路。试着放宽时间范围，或清空条件重新检索。</div>
  {{end}}
</section>
{{end}}
{{template "foot" .}}{{end}}
`
