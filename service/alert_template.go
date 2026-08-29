package service

import (
	htmpl "html/template"
	ttmpl "text/template"
)

// 告警邮件模板。
//
// HTML 版本统一走 html/template：业务上报的 message / params / error_message 会被
// 自动转义，避免「链路内容把邮件结构冲垮」或注入恶意 HTML。
// 样式全部内联，因为绝大多数邮件客户端（含手机自带邮箱）会剥离 <style> 标签。
var (
	traceMailHTML = htmpl.Must(htmpl.New("trace.html").Parse(traceMailHTMLSrc))
	traceMailText = ttmpl.Must(ttmpl.New("trace.txt").Parse(traceMailTextSrc))
	dropMailHTML  = htmpl.Must(htmpl.New("drop.html").Parse(dropMailHTMLSrc))
	dropMailText  = ttmpl.Must(ttmpl.New("drop.txt").Parse(dropMailTextSrc))
)

const traceMailHTMLSrc = `<!DOCTYPE html>
<html lang="zh-CN">
<body style="margin:0;padding:14px;background:#f6f7f9;font-family:-apple-system,'Segoe UI',Roboto,'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2328;font-size:13px;line-height:1.6;">
<div style="max-width:1100px;margin:0 auto;">

  <div style="padding:14px 18px;border-radius:8px 8px 0 0;background:{{if eq .Status "error"}}#cf222e{{else if eq .Status "timeout"}}#bc4c00{{else if eq .Status "warn"}}#9a6700{{else}}#1a7f37{{end}};color:#fff;">
    <div style="font-size:17px;font-weight:600;">Trace Alert &middot; {{.Status}}</div>
    <div style="opacity:.85;font-size:12px;margin-top:2px;">链路触发告警，完整时间线见下方，点链接可直接打开网页详情</div>
  </div>

  <div style="background:#fff;border:1px solid #d0d7de;border-top:none;padding:14px 18px;">
    <table style="border-collapse:collapse;width:100%;font-size:13px;">
      <tr>
        <td style="padding:3px 0;width:88px;color:#57606a;vertical-align:top;">Trace ID</td>
        <td style="padding:3px 0;"><code style="background:#f6f8fa;padding:2px 6px;border-radius:4px;font-size:12px;">{{.TraceID}}</code></td>
      </tr>
      <tr><td style="padding:3px 0;color:#57606a;">Service</td><td style="padding:3px 0;">{{.Service}}</td></tr>
      <tr><td style="padding:3px 0;color:#57606a;">Status</td><td style="padding:3px 0;">{{.Status}}</td></tr>
      <tr><td style="padding:3px 0;color:#57606a;">Duration</td><td style="padding:3px 0;">{{.Duration}}</td></tr>
      <tr><td style="padding:3px 0;color:#57606a;">Events</td><td style="padding:3px 0;">{{.EventCount}}{{if .Hidden}}（当前展示 {{.ShownCount}} 条，中间省略 {{.Hidden}} 条）{{end}}</td></tr>
      <tr><td style="padding:3px 0;color:#57606a;">Start</td><td style="padding:3px 0;">{{.StartAt}}</td></tr>
      <tr><td style="padding:3px 0;color:#57606a;">End</td><td style="padding:3px 0;">{{.EndAt}}</td></tr>
      {{if .ErrorMsg}}
      <tr><td style="padding:3px 0;color:#57606a;vertical-align:top;">Error</td><td style="padding:3px 0;color:#cf222e;word-break:break-all;">{{.ErrorMsg}}</td></tr>
      {{end}}
      <tr><td style="padding:3px 0;color:#57606a;">Detail</td><td style="padding:3px 0;"><a href="{{.URL}}" style="color:#0969da;">{{.URL}}</a></td></tr>
    </table>

    {{if .LevelStats}}
    <div style="margin-top:10px;">
      {{range .LevelStats}}<span style="display:inline-block;margin-right:6px;padding:1px 8px;border-radius:10px;background:#f6f8fa;border:1px solid #d0d7de;font-size:12px;">{{.Level}} &times; {{.Count}}</span>{{end}}
    </div>
    {{end}}
  </div>

  <div style="background:#fff;border:1px solid #d0d7de;border-top:none;padding:0 18px 18px;">
    <div style="padding:12px 0 6px;font-weight:600;">Timeline</div>
    <table style="border-collapse:collapse;width:100%;font-size:12px;">
      <tr style="background:#f6f8fa;">
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">#</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Time</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Offset</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Level</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Module</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Event</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Message</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Params</th>
        <th style="padding:6px;border:1px solid #d0d7de;text-align:left;">Error</th>
      </tr>
      {{range .Events}}
      <tr{{if .Highlight}} style="background:#fff5f5;"{{end}}>
        <td style="padding:5px 6px;border:1px solid #d0d7de;color:#57606a;">{{.Idx}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;white-space:nowrap;">{{.Time}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;white-space:nowrap;color:#57606a;">{{.Offset}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;white-space:nowrap;{{if .Highlight}}color:#cf222e;font-weight:600;{{end}}">{{.Level}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;">{{.Module}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;">{{.Event}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;word-break:break-all;">{{.Message}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;word-break:break-all;">{{.Params}}</td>
        <td style="padding:5px 6px;border:1px solid #d0d7de;word-break:break-all;color:#cf222e;">{{.ErrorMsg}}</td>
      </tr>
      {{end}}
    </table>
  </div>

  <div style="padding:10px 4px;color:#57606a;font-size:11px;">
    由 TracePulse 自动发送 &middot; 同链路同状态在去重窗口内只发送一次
  </div>
</div>
</body>
</html>`

const traceMailTextSrc = `Trace Alert - {{.Status}}

Trace ID : {{.TraceID}}
Service  : {{.Service}}
Status   : {{.Status}}
Duration : {{.Duration}}
Events   : {{.EventCount}}{{if .Hidden}} (showing {{.ShownCount}}, {{.Hidden}} omitted){{end}}
Start    : {{.StartAt}}
End      : {{.EndAt}}
{{if .ErrorMsg}}Error    : {{.ErrorMsg}}
{{end}}Detail   : {{.URL}}

---- Timeline ----
{{range .Events}}#{{.Idx}} [{{.Time}} {{.Offset}}] {{.Level}} {{.Module}}.{{.Event}}{{if .Message}} | {{.Message}}{{end}}{{if .Params}} | {{.Params}}{{end}}{{if .ErrorMsg}} | ERR: {{.ErrorMsg}}{{end}}
{{end}}`

const dropMailHTMLSrc = `<!DOCTYPE html>
<html lang="zh-CN">
<body style="margin:0;padding:14px;background:#f6f7f9;font-family:-apple-system,'Segoe UI',Roboto,'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2328;font-size:13px;line-height:1.6;">
<div style="max-width:640px;margin:0 auto;">
  <div style="padding:14px 18px;border-radius:8px 8px 0 0;background:#bc4c00;color:#fff;font-size:17px;font-weight:600;">Trace Queue Drop Alert</div>
  <div style="background:#fff;border:1px solid #d0d7de;border-top:none;padding:14px 18px;">
    <p style="margin:0 0 8px;">在最近的聚合窗口内，<strong>{{.Count}}</strong> 条 trace 事件因队列已满被丢弃。</p>
    <p style="margin:0;color:#57606a;">常见原因：trace-server 写入 SQLite 变慢、磁盘 IO 打满，或事件上报量超过队列容量。<br>
    事件已通过 ndjson 兜底落盘，不会永久丢失，但实时检索会缺这部分数据。</p>
    <p style="margin:10px 0 0;color:#57606a;">Time: {{.At}}</p>
  </div>
</div>
</body>
</html>`

const dropMailTextSrc = `Trace Queue Drop Alert

在最近的聚合窗口内，{{.Count}} 条 trace 事件因队列已满被丢弃。

常见原因：trace-server 写入 SQLite 变慢、磁盘 IO 打满，或事件上报量超过队列容量。
事件已通过 ndjson 兜底落盘，不会永久丢失，但实时检索会缺这部分数据。

Time: {{.At}}`
