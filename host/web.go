package main

// web.go —— 配置界面: 本机 Web 服务 + 表单读写 config.yaml(6.5)。
//
// 为什么是 Web 界面而不是原生窗口?
// 1. Go 写原生 GUI 很重(fyne/wails 都引入大量依赖), Web 只用标准库 net/http;
// 2. 表单这种界面 HTML 最擅长, 改样式不用重新编译;
// 3. 只监听 127.0.0.1(本机回环), 别人访问不到 —— API key 只在你自己浏览器里出现。
//
// 读写 config.yaml 用 yaml.Node 而不是 map:
// map 方式回写会把文件里的注释全冲掉(那些注释是教同事用的);
// Node 是"带注释的语法树", 只动我们改的节点, 其它原样保留。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// fieldSpec 一个可编辑配置项: 表单显示名 + yaml 里的路径 + 值类型。
// 新增可配置项 = 在这里加一行, 页面自动多一个输入框。
// 用键值对初始化而不是位置顺序 —— 11 个字段按位置排, 错一个编译都不报错,
// 键值对写错字段名编译器直接拦。
type fieldSpec struct {
	Path     []string // config.yaml 里的层级, 如 ["notify","api","doc_file"]
	Label    string
	Kind     string   // "string" | "int" | "bool" | "password" | "time" | "list"
	Min      string   // int 类型的下限(原生数字输入框约束)
	Max      string   // int 类型的上限
	Picker   string   // "" 无 | "dir" 目录选择器(填回输入框) | "locate" 在文件管理器中打开
	Options  []string // list 类型的可选项(复选框)
	Requires string   // 前端联动: "复选框组字段名:选项值", 勾了才显示, 空 = 常显。
	// 例: notify 的 doc_file 依赖 "notify>channels:api"(勾了 api 渠道才显示);
	// git 的仓库列表依赖 "sources:git"(勾了 git 来源才显示)。
	// 推广成通式是因为 7.2 后联动需求不止一处 —— 写死 if(api) 就要复制粘贴改。
	Section string // 所属导航分组, 见 sectionDefs
}

// 导航分区定义(顺序即页面顺序)。导航只是"滚动定位"的视图装饰:
// 全文仍是一个 <form> 一次 POST, 保存/校验逻辑不分页 —— 分页会把
// 一份配置的提交拆成多个, 状态同步的复杂度立刻翻倍。
var sectionDefs = []struct {
	ID    string
	Title string
}{
	{"ai", "AI 配置"},
	{"source", "数据来源"},
	{"time", "时间调度"},
	{"notify", "通知"},
}

// 可编辑字段清单。刻意只放"用户该碰的", llm.provider 这种技术项不放;
// 要全量编辑请用页面上的"编辑配置文件"按钮(见 handleEditConfig)。
var editableFields = []fieldSpec{
	{Path: []string{"llm", "model"}, Label: "AI 模型", Kind: "string", Section: "ai"},
	{Path: []string{"llm", "api_key"}, Label: "DeepSeek API Key", Kind: "password", Section: "ai"},

	{Path: []string{"sources"}, Label: "启用的数据来源(可多选, 消息合并整理)", Kind: "list", Options: []string{"dingtalk", "git"}, Section: "source"},
	{Path: []string{"dingtalk", "data_dir"}, Label: "钉钉数据目录(留空=用定位 skill 自动找)", Kind: "string", Picker: "dir", Section: "source"},
	{Path: []string{"dingtalk", "uid"}, Label: "钉钉内部 uid(留空=自动获取)", Kind: "string", Section: "source"},
	{Path: []string{"collect", "conversations"}, Label: "关注的会话(逗号分隔, 留空=全部)", Kind: "tags", Section: "source"},
	{Path: []string{"collect", "only_mine"}, Label: "只整理我发的消息(合规, 推荐开着)", Kind: "bool", Section: "source"},

	{Path: []string{"git", "repos"}, Label: "git 仓库列表(每行一个仓库)", Kind: "repos", Section: "source", Requires: "sources:git"},
	{Path: []string{"git", "author"}, Label: "提交作者过滤(留空=全部人; 可用下方按钮从仓库读取后勾选)", Kind: "string", Section: "source", Requires: "sources:git"},

	{Path: []string{"collect", "since_hour"}, Label: "每天几点开始收集", Kind: "time", Section: "time"},
	{Path: []string{"schedule", "work_end"}, Label: "下班时间", Kind: "time", Section: "time"},
	{Path: []string{"schedule", "lead_minutes"}, Label: "提前多少分钟生成", Kind: "int", Min: "1", Max: "240", Section: "time"},

	{Path: []string{"notify", "channels"}, Label: "通知渠道", Kind: "list", Options: []string{"toast", "api"}, Section: "notify"},
	{Path: []string{"notify", "api", "doc_file"}, Label: "API 通知文档路径", Kind: "string", Picker: "locate", Requires: "notify>channels:api", Section: "notify"},
	{Path: []string{"notify", "api", "at_mobiles"}, Label: "@人的手机号(逗号分隔, 留空=不@)", Kind: "tags", Requires: "notify>channels:api", Section: "notify"},
}

// normTimeValue 把配置值规整成 time 输入框要求的 "HH:MM"(必须两位补零)。
// 兼容旧配置: 纯小时 "9" -> "09:00"。配置演化的兼容层在展示侧再补一道。
func normTimeValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.Contains(v, ":") {
		return pad2(v) + ":00"
	}
	parts := strings.SplitN(v, ":", 2)
	return pad2(parts[0]) + ":" + pad2(parts[1])
}

// pad2 一位数补零成两位; 非数字原样返回(time 输入框会忽略非法值, 不致命)。
func pad2(s string) string {
	if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
		return "0" + s
	}
	return s
}

// yamlStrNode 造一个字符串标量节点; 含 YAML 特殊字符(冒号/反斜杠路径常带)时
// 强制加引号, 防止写回后解析错。引号规则与 setNodeValue 的 default 分支一致。
func yamlStrNode(v string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	if strings.ContainsAny(v, ":#{}[]&*!|>'\"%@`") || strings.TrimSpace(v) != v {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// getNode 沿路径找到 yaml.Node。找不到返回 nil(字段缺失=用户没配, 显示空)。
func getNode(doc *yaml.Node, path []string) *yaml.Node {
	cur := doc.Content[0] // DocumentNode 下唯一的 MappingNode
	for _, key := range path {
		next := (*yaml.Node)(nil)
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == key {
				next = cur.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// setNodeValue 沿路径设置标量值, 按类型打 Tag(YAML 的 1 和 "1" 不是一回事)。
// 只改已有节点 —— 不新建节点。理由: 字段清单和 example 模板是同步维护的,
// 用户文件里"该有的都有"; 自动新建会让手滑打错的路径名悄悄写进配置。
func setNodeValue(doc *yaml.Node, path []string, raw string, kind string) bool {
	node := getNode(doc, path)
	if node == nil {
		return false
	}
	switch kind {
	case "int":
		node.Tag = "!!int"
		node.Style = 0
	case "bool":
		node.Tag = "!!bool"
		node.Style = 0
	default:
		strNode := yamlStrNode(raw)
		node.Tag, node.Style = strNode.Tag, strNode.Style
	}
	node.Value = raw
	return true
}

// ---- 页面模板 ----

var pageTmpl = template.Must(template.New("cfg").Parse(`<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>日报服务 · 配置</title>
<style>
  body { font-family: system-ui, "Microsoft YaHei", sans-serif; margin: 0;
         color: #1f2937; background: #f9fafb; }
  /* 左导航 + 主内容两栏。导航是锚点滚动定位, 不是分页 —— 整个表单仍是一次 POST */
  .layout { display: flex; max-width: 960px; margin: 0 auto; }
  .side { width: 168px; flex: none; padding: 28px 0; position: sticky; top: 0;
          align-self: flex-start; }
  .side .brand { font-size: 18px; font-weight: 700; padding: 0 20px 18px; }
  .side a { display: block; padding: 9px 20px; color: #4b5563; font-size: 14px;
            text-decoration: none; border-left: 3px solid transparent; }
  .side a:hover { color: #2563eb; background: #eff6ff; }
  main { flex: 1; min-width: 0; padding: 28px 24px 64px; }
  section { background: #fff; border: 1px solid #e5e7eb; border-radius: 10px;
            padding: 20px 24px; margin-bottom: 20px; scroll-margin-top: 16px; }
  section h2 { font-size: 16px; margin: 0 0 4px; }
  .sub { color: #6b7280; font-size: 13px; margin-bottom: 20px; }
  label { display: block; font-size: 14px; margin: 14px 0 4px; }
  .field { display: flex; gap: 8px; align-items: center; }
  input { flex: 1; min-width: 0; box-sizing: border-box; padding: 8px 10px; font-size: 14px;
          border: 1px solid #d1d5db; border-radius: 6px; }
  input:focus { outline: 2px solid #2563eb; border-color: transparent; }
  .saved { background: #ecfdf5; border: 1px solid #10b981; color: #047857;
           padding: 10px 14px; border-radius: 6px; margin-bottom: 16px; }
  /* 上下都留: 只有 margin-top 时按钮行会和下一个区块贴死(7.1 实测 pony 反馈) */
  .row { display: flex; gap: 12px; flex-wrap: wrap; margin: 24px 0; }
  button { padding: 9px 18px; border: 0; border-radius: 6px; font-size: 14px;
           cursor: pointer; }
  .primary { background: #2563eb; color: #fff; }
  .ghost { background: #f3f4f6; color: #374151; }
  .mini { padding: 8px 12px; font-size: 13px; white-space: nowrap; }
  .checks { display: flex; gap: 16px; padding: 6px 0; }
  .check { display: flex; align-items: center; gap: 6px; font-size: 14px;
           margin: 0; cursor: pointer; }
  /* flex:none 必须: 全局 input{flex:1} 会让复选框吃掉所有剩余空间,
     把后面的文字推到最右(会话列表里标签越长偏得越离谱) */
  .check input { width: auto; flex: none; }
  /* 开关(toggle): appearance:none 抹掉原生样式, 用 after 圆点滑动画状态。
     全局 input{flex:1} 对开关是灾难(它会撑满整行), 必须 flex:none 钉死尺寸 */
  input.tgl { appearance: none; -webkit-appearance: none; flex: none; width: 42px; height: 24px;
              border: 0; padding: 0; border-radius: 12px; background: #d1d5db;
              position: relative; cursor: pointer; transition: background .15s; }
  input.tgl::after { content: ""; position: absolute; top: 3px; left: 3px; width: 18px; height: 18px;
                     border-radius: 50%; background: #fff; transition: left .15s; }
  input.tgl:checked { background: #2563eb; }
  input.tgl:checked::after { left: 21px; }
  /* git 仓库编辑器: 行 = 路径(吃满) + 显示名(限宽) + 浏览/删除按钮 */
  .repo-name { max-width: 160px; }
  .warn { background: #fffbeb; border: 1px solid #f59e0b; color: #b45309;
          padding: 10px 14px; border-radius: 6px; margin-top: 24px; font-size: 13px; }
</style>
</head>
<body>
<div class="layout">
  <nav class="side">
    <div class="brand">📋 日报服务</div>
    {{range .Sections}}<a href="#sec-{{.ID}}">{{.Title}}</a>{{end}}
    <a href="#sec-generate">手动生成</a>
  </nav>
  <main>
  <div class="sub">只监听本机(127.0.0.1), 保存即写入 config.yaml, 下次生成生效。</div>
  {{if .Saved}}<div class="saved">✓ 已保存</div>{{end}}
  {{if .Note}}<div class="warn">{{.Note}}</div>{{end}}
  <form method="POST" action="/save">
  {{range .Sections}}
  <section id="sec-{{.ID}}"><h2>{{.Title}}</h2>
  {{range .Fields}}
  {{$f := .}}
  {{if .Requires}}<div class="dep" data-req="{{.Requires}}">{{end}}
  <label>{{.Label}}</label>
  {{if eq .Type "checkboxes"}}
  <div class="checks">
    {{range .Options}}
    <label class="check"><input type="checkbox" name="{{$f.Key}}" value="{{.}}"
      {{if index $f.Checked .}}checked{{end}}> {{.}}</label>
    {{end}}
  </div>
  {{else if eq .Type "switch"}}
  <div class="field">
    {{/* 开关 = checkbox + hidden 双保险: 勾了提交 true(在前, Get 取到它),
        没勾只剩 hidden 的 false。否则 unchecked 的 checkbox 什么都不提交,
        后端无法区分"没动它"和"关掉了"。 */}}
    <input class="tgl" type="checkbox" name="{{.Key}}" value="true" {{if eq .Value "true"}}checked{{end}}>
    <input type="hidden" name="{{.Key}}" value="false">
  </div>
  {{else if eq .Type "repos"}}
  <div id="repo-editor">
    <div id="repo-rows">
      {{range .Repos}}
      <div class="field repo-row" style="margin-top:6px">
        <input class="repo-path" placeholder="本地路径或远程地址, 如 https://github.com/user/repo.git" value="{{.Path}}">
        <input class="repo-name" placeholder="显示名(可选)" value="{{.Name}}">
        <button class="ghost mini" type="button" onclick="delRepoRow(this)">删</button>
      </div>
      {{end}}
    </div>
    <button class="ghost mini" type="button" onclick="addRepoRow()" style="margin-top:8px">+ 添加仓库</button>
    {{/* 提交载体: JS 把各行同步成 JSON 塞这里, 后端解析后重建 YAML 序列 */}}
    <input type="hidden" name="{{.Key}}" id="repos-json">
  </div>
  {{else}}
  <div class="field">
    <input name="{{.Key}}" type="{{.Type}}" value="{{.Value}}"
           {{if .Min}}min="{{.Min}}"{{end}} {{if .Max}}max="{{.Max}}"{{end}}>
    {{if eq .Picker "dir"}}
    <button class="ghost mini" type="button" onclick="pickDir('{{.Key}}')">浏览…</button>
    {{end}}
    {{if eq .Picker "locate"}}
    <button class="ghost mini" type="button" onclick="locatePath('{{.Key}}')">打开位置</button>
    {{end}}
    {{if eq .Type "password"}}
    <button class="ghost mini" type="button" onclick="togglePw('{{.Key}}', this)">显示</button>
    {{end}}
  </div>
  {{end}}
  {{if .Requires}}</div>{{end}}
  {{if eq .Key "git>author"}}
  <div id="author-picker">
    <button class="ghost mini" type="button" onclick="loadGitAuthors()">📖 从仓库读取作者</button>
    <div id="author-results" style="font-size:13px;color:#6b7280;margin:8px 0;line-height:1.9"></div>
    <button class="ghost mini" type="button" id="apply-authors" style="display:none"
            onclick="applyAuthors()">✔ 把勾选的作者填入上方输入框</button>
  </div>
  {{end}}
  {{if eq .Key "collect>conversations"}}
  <div id="chat-picker">
    <div class="field" style="margin-top:6px">
      <input type="date" id="search-date">
      <button class="ghost mini" type="button" onclick="searchChats()">🔍 搜索当天会话</button>
    </div>
    <div id="chat-results" style="font-size:13px;color:#6b7280;margin:8px 0;line-height:1.9"></div>
    <button class="ghost mini" type="button" id="apply-chats" style="display:none"
            onclick="applyChats()">✔ 把勾选的会话填入上方输入框</button>
  </div>
  {{end}}
  {{end}}
  </section>
  {{end}}
  <div class="row">
    <button class="primary" type="submit">保存配置</button>
  </div>
  </form>
  <section id="sec-generate"><h2>手动生成</h2>
    <div class="sub" style="margin-bottom:8px">不依赖钉钉——把今天做的事贴到这里(可选), 和已启用来源的消息合并后立刻生成日报。</div>
    <form method="POST" action="/generate">
      <textarea name="paste" rows="6"
        placeholder="例如: 修了登录接口的超时问题; 和 XX 对了方案… (整段粘贴即可, AI 自己会分条)"
        style="width:100%;box-sizing:border-box;padding:8px 10px;font-size:14px;
               border:1px solid #d1d5db;border-radius:6px;font-family:inherit"></textarea>
      <div class="field" style="margin-top:8px">
        <input type="date" name="gen-date">
        <button class="primary mini" type="submit">立即生成</button>
      </div>
    </form>
  </section>
<div class="row">
  <form method="POST" action="/notify-test"><button class="ghost" type="submit">测试通知</button></form>
  <form method="POST" action="/autostart">
    <button class="ghost" type="submit">开机自启: {{.Autostart}}</button>
  </form>
  <form method="POST" action="/edit-config">
    <button class="ghost" type="submit">打开配置文件位置(全量编辑)</button>
  </form>
</div>
<div class="warn">⚠ API Key 只显示在你本机浏览器。这台电脑的其它用户(如有)也能打开本页面, 公用机请注意。</div>
  </main>
</div>
<script>
// 目录选择: 后端弹 Windows 原生目录对话框(Shell.Application COM 对象),
// 选完把路径填回输入框。浏览器自身无法完成"选目录->回填路径", 必须借后端。
async function pickDir(key) {
  const btn = event.target; btn.disabled = true; btn.textContent = '选择中…';
  try {
    const r = await fetch('/pick-dir');
    const p = (await r.text()).trim();
    if (p) document.querySelector('[name="' + key + '"]').value = p;
  } finally { btn.disabled = false; btn.textContent = '浏览…'; }
}
// 打开位置: 在文件管理器中显示该路径(文件则显示它所在的目录)
async function locatePath(key) {
  const v = document.querySelector('[name="' + key + '"]').value.trim();
  await fetch('/open-path', {method: 'POST', body: new URLSearchParams({path: v})});
}
// 联动: .dep 元素带 data-req="复选框组名:选项值", 只有对应复选框勾着才显示。
// 纯前端切换, 不存状态 —— 真正的开关是保存后的 sources/channels 列表。
// 通用实现(7.2): 需求从"只有 api 一处"变成多处(api 渠道/git 来源), 写死 if 就要复制。
function refreshDeps() {
  document.querySelectorAll('.dep').forEach(el => {
    const req = el.dataset.req, i = req.lastIndexOf(':');
    const grp = req.slice(0, i), val = req.slice(i + 1);
    const cb = document.querySelector('[name="' + grp + '"][value="' + val + '"]');
    el.style.display = (cb && cb.checked) ? '' : 'none';
  });
}
document.querySelectorAll('.checks input[type=checkbox]').forEach(cb => {
  cb.addEventListener('change', refreshDeps);
});
refreshDeps();
// ---- git 仓库编辑器(行编辑 -> 隐藏域 JSON -> 后端重建 YAML 序列) ----
function syncRepos() { // 各行当前值 -> JSON 塞隐藏域; 空路径的行直接丢弃
  const rows = [...document.querySelectorAll('#repo-rows .repo-row')].map(r => ({
    path: r.querySelector('.repo-path').value.trim(),
    name: r.querySelector('.repo-name').value.trim(),
  })).filter(r => r.path !== '');
  const h = document.getElementById('repos-json');
  if (h) h.value = JSON.stringify(rows);
}
function repoRowHtml() {
  return '<div class="field repo-row" style="margin-top:6px">' +
    '<input class="repo-path" placeholder="本地路径或远程地址, 如 https://github.com/user/repo.git">' +
    '<input class="repo-name" placeholder="显示名(可选)">' +
    '<button class="ghost mini" type="button" onclick="delRepoRow(this)">删</button></div>';
}
function addRepoRow() {
  document.getElementById('repo-rows').insertAdjacentHTML('beforeend', repoRowHtml());
  syncRepos();
}
function delRepoRow(btn) {
  btn.closest('.repo-row').remove();
  syncRepos();
}
const repoRows = document.getElementById('repo-rows');
if (repoRows) {
  repoRows.addEventListener('input', syncRepos); // 输入即同步, 不依赖保存按钮
  syncRepos(); // 页面加载先把服务端渲染的初始值同步进隐藏域
}
// 密码显隐切换: 核对 key 时看一眼, 平时保持掩码
function togglePw(key, btn) {
  const input = document.querySelector('[name="' + key + '"]');
  const show = input.type === 'password';
  input.type = show ? 'text' : 'password';
  btn.textContent = show ? '隐藏' : '显示';
}
// ---- 会话选择器(选日期 -> 搜索 -> 勾选 -> 回填) ----
// 后端调 collector 的 list_chats 模式: 解密库扫描当天, 返回"有我发言"的会话列表。
// 单聊 title 由后端经用户资料表翻译成昵称; 翻译不了的(对方资料不在本地库)
// 才退回 uid, 靠消息摘要辨认。
function splitConvs() { // 上方输入框的值 -> 数组(中英文逗号都认, 与保存逻辑一致)
  const raw = document.querySelector('[name="collect>conversations"]').value;
  return raw.split(/[,，]/).map(s => s.trim()).filter(Boolean);
}
function escapeHtml(s) { // 列表内容来自聊天消息摘要, 必须转义防 XSS
  return s.replace(/[&<>"']/g, c =>
    ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
async function searchChats() {
  const btn = event.target;
  const box = document.getElementById('chat-results');
  btn.disabled = true; btn.textContent = '搜索中…(要解密数据库, 稍等)';
  try {
    const r = await fetch('/search-chats', {method: 'POST',
      body: new URLSearchParams({date: document.getElementById('search-date').value})});
    if (!r.ok) { box.innerHTML = '<span style="color:#dc2626">搜索失败: '
      + escapeHtml(await r.text()) + '</span>'; return; }
    renderChatList((await r.json()).chats || []);
  } finally { btn.disabled = false; btn.textContent = '🔍 搜索当天会话'; }
}
function renderChatList(chats) {
  const box = document.getElementById('chat-results');
  const applyBtn = document.getElementById('apply-chats');
  if (!chats.length) {
    box.innerHTML = '当天没有搜到有你发言的会话';
    applyBtn.style.display = 'none';
    return;
  }
  const picked = new Set(splitConvs()); // 已配置的会话回显成勾选状态
  box.innerHTML = '共 ' + chats.length + ' 个会话(按我发言条数排序):<br>' +
    chats.map(c =>
      '<label class="check" style="display:flex;margin:2px 0">' +
      '<input type="checkbox" class="chat-cb" value="' + escapeHtml(c.key) + '"' +
      (picked.has(c.key) ? ' checked' : '') + '> ' +
      '<span>' + escapeHtml(c.label) + '</span></label>'
    ).join('');
  applyBtn.style.display = '';
}
function applyChats() { // 勾选项 -> 回填上方输入框(逗号分隔), 保存流程不变
  const vals = [...document.querySelectorAll('.chat-cb:checked')].map(cb => cb.value);
  document.querySelector('[name="collect>conversations"]').value = vals.join(', ');
}
// ---- 作者选择器(读仓库 -> 多选 -> 回填), 交互抄会话选择器, 后端是 list_authors ----
// 勾选值用"姓名 <邮箱>"整体做子串匹配: 够精确(同名不同邮箱能区分), 又符合
// author 字段"子串匹配"的既有语义, 回填后 config.yaml 依然人类可读。
function splitAuthors() {
  const raw = document.querySelector('[name="git>author"]').value;
  return raw.split(/[,，]/).map(s => s.trim()).filter(Boolean);
}
async function loadGitAuthors() {
  const btn = event.target;
  const box = document.getElementById('author-results');
  syncRepos(); // 先把当前页(可能未保存)的仓库行同步进隐藏域, 读的是最新输入
  const repos = document.getElementById('repos-json').value;
  btn.disabled = true; btn.textContent = '读取中…(远程仓库首次会克隆)';
  try {
    const r = await fetch('/git-authors', {method: 'POST',
      body: new URLSearchParams({repos})});
    if (!r.ok) { box.innerHTML = '<span style="color:#dc2626">读取失败: '
      + escapeHtml(await r.text()) + '</span>'; return; }
    renderAuthorList((await r.json()).authors || []);
  } finally { btn.disabled = false; btn.textContent = '📖 从仓库读取作者'; }
}
function renderAuthorList(authors) {
  const box = document.getElementById('author-results');
  const applyBtn = document.getElementById('apply-authors');
  if (!authors.length) {
    box.innerHTML = '仓库里没读到任何提交记录';
    applyBtn.style.display = 'none';
    return;
  }
  const picked = new Set(splitAuthors()); // 已配置的作者回显成勾选
  box.innerHTML = '共 ' + authors.length + ' 位作者(全部仓库去重):<br>' +
    authors.map(a =>
      '<label class="check" style="display:flex;margin:2px 0">' +
      '<input type="checkbox" class="author-cb" value="' + escapeHtml(a) + '"' +
      (picked.has(a) ? ' checked' : '') + '> ' +
      '<span>' + escapeHtml(a) + '</span></label>'
    ).join('');
  applyBtn.style.display = '';
}
function applyAuthors() {
  const vals = [...document.querySelectorAll('.author-cb:checked')].map(cb => cb.value);
  document.querySelector('[name="git>author"]').value = vals.join(', ');
}
// 两个日期选择器(会话搜索 / 手动生成)。
// 手动生成的日期用 localStorage 记住上次选择(7.3 修) —— 生成/保存后页面整体
// 刷新, 新页面的输入框永远是空的,"只在空时填默认"等于每次都被刷回今天。
// 浏览器表单值撑不过 POST 跳转, 能跨刷新存值的只有 localStorage。
// 注意: localStorage 按"源(含端口)"隔离, daemon 重启换端口后记忆失效,
// 退回"今天" —— 可接受, 比在每次保存后丢选择强。
(function () {
  const t = new Date(), p = n => String(n).padStart(2, '0');
  const today = t.getFullYear() + '-' + p(t.getMonth() + 1) + '-' + p(t.getDate());
  // 两个日期都持久化(7.3): 补写历史日报的典型动作是"搜前一天的会话 ->
  // 回填 -> 选前一天生成", 两个日期必须能一起留住, 只记一个等于没修。
  const remember = (el, key) => {
    if (!el) return;
    el.value = localStorage.getItem(key) || today;
    el.addEventListener('change', () => localStorage.setItem(key, el.value));
  };
  const sdEl = document.getElementById('search-date');
  const gdEl = document.querySelector('[name="gen-date"]');
  remember(sdEl, 'dr-search-date');
  remember(gdEl, 'dr-gen-date');
  // 联动(7.3 实测踩坑): 用户心智里"我今天是来处理 22 号的" —— 改搜索日期
  // 就意味着生成日期也要是那天。两个独立选择器各管各的, 必然出现一个 22
  // 一个 23 的诡异状态。以搜索日期为主动方同步过去(反向不同步: 只想改
  // 生成日期、不想动搜索日期是合理操作)。
  if (sdEl && gdEl) {
    sdEl.addEventListener('change', () => {
      gdEl.value = sdEl.value;
      localStorage.setItem('dr-gen-date', gdEl.value);
    });
  }
})();
</script>
</body>
</html>`))

type fieldView struct {
	Key      string // 表单字段名, 用 ">" 连接的路径, 如 "llm>model"
	Label    string
	Type     string // input 的 type: text/password/time/number/checkboxes/switch/repos
	Min      string
	Max      string
	Picker   string
	Value    string
	Options  []string
	Checked  map[string]bool // list 类型: 哪些选项勾着
	Requires string
	Repos    []repoView // repos 类型: 当前已配置的仓库行
}

// repoView git 仓库编辑器的一行: 路径 + 显示名。
// repo 级 author(单仓库覆盖全局过滤)不进界面 —— 高级用法, 需要的人去 config.yaml 手写。
type repoView struct {
	Path string
	Name string
}

// sectionView 一个导航分区: 标题 + 属于它的字段。渲染顺序由 sectionDefs 决定,
// 字段归属由 fieldSpec.Section 决定 —— 两边靠字符串 ID 关联, 加新区不用动渲染代码。
type sectionView struct {
	ID     string
	Title  string
	Fields []fieldView
}

type pageData struct {
	Sections  []sectionView
	Saved     bool
	Note      string
	Autostart string
}

// ---- HTTP 处理器 ----

type configServer struct {
	root string
}

func (s *configServer) loadDoc() (*yaml.Node, []byte, error) {
	path := filepath.Join(s.root, "config.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("config.yaml 解析失败: %w", err)
	}
	return &doc, raw, nil
}

func (s *configServer) handlePage(w http.ResponseWriter, r *http.Request) {
	doc, _, err := s.loadDoc()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	data := pageData{Saved: r.URL.Query().Get("saved") == "1"}
	if v := r.URL.Query().Get("note"); v != "" {
		data.Note = v
	}
	// 按 sectionDefs 顺序建分区, 字段按 Section 归队; 找不到分区的字段
	// 直接跳过(配置漂移的显式失败, 比静默丢进第一页好排查)
	// 教训: 不能边 append data.Sections 边存指向它的指针 —— append 导致底层
	// 数组扩容搬迁, 旧指针全部失效, 前几个分区的字段全部丢失(6.5.8 实测踩坑)。
	// 正确姿势: 先一次性建好定长切片, 指针存进 map, 全部填完再整体挂上 data。
	sections := make([]sectionView, len(sectionDefs))
	secByID := map[string]*sectionView{}
	for i, sd := range sectionDefs {
		sections[i] = sectionView{ID: sd.ID, Title: sd.Title}
		secByID[sd.ID] = &sections[i]
	}
	for _, f := range editableFields {
		sec, ok := secByID[f.Section]
		if !ok {
			log.Printf("字段 %v 配置了未知分区 %q, 已跳过", f.Path, f.Section)
			continue
		}
		node := getNode(doc, f.Path)
		value := ""
		if node != nil {
			value = node.Value
		}
		view := fieldView{
			Key:      strings.Join(f.Path, ">"),
			Label:    f.Label,
			Type:     "text",
			Min:      f.Min,
			Max:      f.Max,
			Picker:   f.Picker,
			Value:    value,
			Options:  f.Options,
			Requires: f.Requires,
		}
		switch f.Kind {
		case "password":
			view.Type = "password"
		case "int":
			view.Type = "number"
		case "time":
			view.Type = "time" // 原生时间选择器: 弹系统时钟面板, 杜绝手滑格式错
			view.Value = normTimeValue(value)
		case "list":
			view.Type = "checkboxes"
			// 读 YAML 序列里勾了哪些(序列节点没有单一 Value, 逐个收集)
			view.Checked = map[string]bool{}
			if node != nil && node.Kind == yaml.SequenceNode {
				for _, item := range node.Content {
					view.Checked[item.Value] = true
				}
			}
		case "tags":
			// 自由标签: YAML 序列 ↔ 逗号分隔文本
			var items []string
			if node != nil && node.Kind == yaml.SequenceNode {
				for _, item := range node.Content {
					items = append(items, item.Value)
				}
			}
			view.Value = strings.Join(items, ", ")
		case "bool":
			// 开关: 读 YAML 布尔节点的文本形式("true"/"false")决定滑块位置
			view.Type = "switch"
		case "repos":
			// 序列的映射项 -> 行视图; 每项只取 path/name 两个键(多余的键原样留在文件里,
			// 保存时按"清单内字段"原则也只重建这两项)
			view.Type = "repos"
			if node != nil && node.Kind == yaml.SequenceNode {
				for _, item := range node.Content {
					if item.Kind != yaml.MappingNode {
						continue
					}
					var rv repoView
					for i := 0; i+1 < len(item.Content); i += 2 {
						switch item.Content[i].Value {
						case "path":
							rv.Path = item.Content[i+1].Value
						case "name":
							rv.Name = item.Content[i+1].Value
						}
					}
					view.Repos = append(view.Repos, rv)
				}
			}
		}
		sec.Fields = append(sec.Fields, view)
	}
	data.Sections = sections
	data.Autostart = "关"
	if on, _ := AutostartStatus(); on {
		data.Autostart = "开"
	}
	pageTmpl.Execute(w, data)
}

func (s *configServer) handleSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", 400)
		return
	}
	doc, _, err := s.loadDoc()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// 只应用清单内的字段: 页面之外的 POST 字段一律忽略(防注入乱写配置)。
	// 空值 = 不修改(所有类型一视同仁) —— 教训(6.5 实测踩坑):
	// 最初只对数字/布尔跳过空值, 字符串被空串覆盖, 一次只传单个字段的
	// 测试请求把 model/data_dir/uid 等全部清空。"清空"是极稀有的需求,
	// 不值得为它冒"误清空"的风险; 真要清空请手编辑 config.yaml。
	for _, f := range editableFields {
		key := strings.Join(f.Path, ">")

		// list(复选框)和 tags(逗号分隔)都重建 YAML 序列。
		// 空值是合法语义(全关=静默 / 全部会话), 不适用标量的"空=不修改"。
		if f.Kind == "list" {
			node := getNode(doc, f.Path)
			if node == nil || node.Kind != yaml.SequenceNode {
				continue
			}
			node.Content = nil
			for _, v := range r.Form[key] {
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
			}
			continue
		}
		if f.Kind == "tags" {
			node := getNode(doc, f.Path)
			if node == nil || node.Kind != yaml.SequenceNode {
				continue
			}
			node.Content = nil
			raw := r.Form.Get(key)
			for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
				return r == ',' || r == '，' // 中英文逗号都认
			}) {
				if v := strings.TrimSpace(part); v != "" {
					node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
				}
			}
			continue
		}
		if f.Kind == "repos" {
			// 前端把行编辑结果序列化成 JSON 提交, 这里解码后重建 YAML 序列。
			// 健壮性: 非法 JSON 不能静默丢弃用户输入 —— 显式 400, 页面会显示错误
			var repos []map[string]string
			if err := json.Unmarshal([]byte(r.Form.Get(key)), &repos); err != nil {
				http.Error(w, "仓库列表格式错误: "+err.Error(), 400)
				return
			}
			node := getNode(doc, f.Path)
			if node == nil || node.Kind != yaml.SequenceNode {
				continue
			}
			node.Style = 0 // 原节点可能是 flow 风格([]), 重建后显式回到块风格, 一行一个仓库
			// 行尾注释挂在"下一个 token"上: flow 时它在 repos 行尾看着正常,
			// 改块风格后会漂到下一个字段(author)后面, 张冠李戴。挪到 repos 自己头上。
			if lc := node.LineComment; lc != "" {
				node.LineComment = ""
				if node.HeadComment != "" {
					lc = node.HeadComment + "\n" + lc
				}
				node.HeadComment = lc
			}
			node.Content = nil
			for _, rp := range repos {
				m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				m.Content = append(m.Content, yamlStrNode("path"), yamlStrNode(rp["path"]))
				if name := rp["name"]; name != "" { // 空显示名不写进文件, 保持配置精简
					m.Content = append(m.Content, yamlStrNode("name"), yamlStrNode(name))
				}
				node.Content = append(node.Content, m)
			}
			continue
		}

		raw := strings.TrimSpace(r.Form.Get(key))
		if raw == "" {
			continue
		}
		setNodeValue(doc, f.Path, raw, f.Kind)
	}

	// 原子写: 先写临时文件再改名。中途断电/崩溃不会留下半个 config.yaml
	out, err := yaml.Marshal(doc)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	target := filepath.Join(s.root, "config.yaml")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.Rename(tmp, target); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("config.yaml 已保存(%d 项)", len(editableFields))
	http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
}

func (s *configServer) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	cfg, err := LoadHostConfig(s.root)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	errs := NotifyAll(s.root, cfg, "日报服务配置界面", "这条通知从配置界面发出 —— 渠道工作正常")
	note := "测试通知已发送"
	if len(errs) > 0 {
		note = "部分渠道失败: " + strings.Join(errs, "; ")
	}
	http.Redirect(w, r, "/?note="+url.QueryEscape(note), http.StatusSeeOther)
}

// handleSearchChats —— 会话选择器的后端: 调 collector 的 list_chats 模式,
// 列出指定日期内有我发言的会话(群名/单聊 + 消息摘要), 前端勾选后回填配置。
// date 为空 = 今天。耗时操作(解密+扫库)在这里同步做完, 前端只等一个响应。
func (s *configServer) handleSearchChats(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", 400)
		return
	}
	date := strings.TrimSpace(r.Form.Get("date"))
	res, code, err := RunSkill(s.root, "dingtalk-collector", map[string]any{
		"debug": false,
		"collect": map[string]any{
			"date":   date,
			"action": "list_chats",
		},
	})
	if err != nil || code != 0 || res["ok"] != true {
		// 教训(6.5.6): 第一版这里只回"未知错误", 排障时毫无线索。
		// 错误信息要带上退出码 —— exit code 是区分"没跑起来/跑了但崩了/跑成功但业务失败"的第一线索。
		msg := "skill 返回了非预期结果"
		switch {
		case err != nil:
			msg = err.Error()
		case res != nil:
			if e, ok := res["error"].(string); ok {
				msg = e
			}
		}
		log.Printf("search-chats 失败: exit=%d msg=%s", code, msg)
		http.Error(w, fmt.Sprintf("搜索失败: %s (skill exit=%d)", msg, code), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(res)
}

// handleGitAuthors —— 读取 git 仓库的作者列表, 给配置页"提交作者过滤"做多选框。
// 与 search-chats 同模式: 前端传当前页面上(可能还没保存)的仓库行,
// 经 stdin 覆盖喂给 git-collector 的 list_authors 动作 —— 用户新加了仓库
// 不用先保存配置就能读作者, 少一步"保存->读取->再保存"的折返。
func (s *configServer) handleGitAuthors(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", 400)
		return
	}
	var repos []map[string]any
	if raw := strings.TrimSpace(r.Form.Get("repos")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &repos); err != nil {
			http.Error(w, "仓库列表不是合法 JSON", 400)
			return
		}
	}
	if len(repos) == 0 {
		http.Error(w, "还没有配置任何仓库(先在上方添加并确保路径正确)", 400)
		return
	}
	res, code, err := RunSkill(s.root, "git-collector", map[string]any{
		"debug": false,
		"git": map[string]any{
			"action": "list_authors",
			"repos":  repos,
		},
	})
	if err != nil || code != 0 || res["ok"] != true {
		msg := "skill 返回了非预期结果"
		switch {
		case err != nil:
			msg = err.Error()
		case res != nil:
			if e, ok := res["error"].(string); ok {
				msg = e
			}
		}
		log.Printf("git-authors 失败: exit=%d msg=%s", code, msg)
		http.Error(w, fmt.Sprintf("读取作者失败: %s (skill exit=%d)", msg, code), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(res)
}

// handleGenerate —— 手动生成: 界面粘贴一段文字 + 选日期, 立刻跑一遍完整流水线。
// 粘贴内容经 pasteMessages 转成契约消息, 作为 extra 与启用来源的消息合并 ——
// 复用 RunPipeline, 不复制任何生成逻辑。生成结果走常规通知渠道(toast 弹出)。
func (s *configServer) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", 400)
		return
	}
	date := strings.TrimSpace(r.Form.Get("gen-date"))
	if err := RunPipeline(s.root, date, pasteMessages(r.Form.Get("paste"))); err != nil {
		log.Printf("手动生成失败: %v", err)
		http.Redirect(w, r, "/?note="+url.QueryEscape("生成失败: "+err.Error()),
			http.StatusSeeOther)
		return
	}
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	http.Redirect(w, r, "/?note="+url.QueryEscape("日报已生成: reports/"+date+".md"),
		http.StatusSeeOther)
}

func (s *configServer) handleAutostart(w http.ResponseWriter, r *http.Request) {
	on, _ := AutostartStatus()
	note := "开机自启已开启"
	if err := SetAutostart(!on); err != nil {
		note = "切换失败: " + err.Error()
	}
	if on {
		note = "开机自启已关闭"
	}
	http.Redirect(w, r, "/?note="+url.QueryEscape(note), http.StatusSeeOther)
}

// pickDir 弹出 Windows 原生"浏览文件夹"对话框, 返回选中的路径。
// 原理: Shell.Application COM 对象, 零第三方依赖; 取消选择返回空串。
// 注意: 对话框出现在"服务器"机器上 —— 我们只监听 127.0.0.1, 所以就是用户本机。
func (s *configServer) handlePickDir(w http.ResponseWriter, r *http.Request) {
	script := `
[Console]::OutputEncoding = [Text.Encoding]::UTF8  # 中文目录名不乱码
$p = (New-Object -COM Shell.Application).BrowseForFolder(0, '选择目录', 17, 0).Self.Path
if ($p) { Write-Output $p }
`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		// 用户取消时 COM 调用失败, 不视为错误, 返回空
		w.Write([]byte(""))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(bytes.TrimSpace(out))
}

// openPath 在文件管理器中显示指定路径(目录直接打开, 文件打开所在目录)。
func (s *configServer) handleOpenPath(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	p := strings.TrimSpace(r.Form.Get("path"))
	if p == "" {
		return // 空值静默: 输入框还没填时不打扰
	}
	// 相对路径按项目根解析 —— config.yaml 里很多路径本来就写相对
	// (如 notify.api.doc_file), 6.5.1 曾因此 400, 是 bug。
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.root, filepath.FromSlash(p))
	}
	info, err := os.Stat(p)
	if err != nil {
		http.Error(w, "路径不存在: "+p, 404)
		return
	}
	target := p
	if !info.IsDir() {
		target = filepath.Dir(p) // 文件 -> 显示它所在的目录
	}
	exec.Command("explorer", target).Start()
}

// editConfig 在文件管理器中显示 config.yaml(打开所在目录并高亮它)。
// 表单覆盖不了所有配置项(故意的: 清单只放"用户该碰的"), 这是通往全量编辑的逃生门。
// 为什么不用"直接打开编辑器": yaml 在多数机器上没有默认关联程序,
// start 命令会静默失败(6.5.3 实测踩坑); explorer /select 永远可靠。
// skills 每次运行都重新读 config.yaml, 所以编辑器里保存即生效, 无需重启服务。
func (s *configServer) handleEditConfig(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(s.root, "config.yaml")
	if _, err := os.Stat(p); err != nil {
		http.Error(w, "config.yaml 不存在", 404)
		return
	}
	exec.Command("explorer", "/select,", p).Start()
	http.Redirect(w, r, "/", http.StatusSeeOther) // 跳回首页, 不留空白标签页
}

// RunConfigServer 启动配置界面服务, 阻塞运行(在 goroutine 里调)。
// 端口 0 = 自动挑一个空闲端口, 避免和本机已占用端口打架。
// autoOpen=false 用于 daemon 内嵌(开机自启时不能每次弹浏览器)。
var configURL string // 服务启动后由这里暴露, 托盘菜单"打开配置界面"用

func RunConfigServer(root string, autoOpen bool) error {
	s := &configServer{root: root}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/save", s.handleSave)
	mux.HandleFunc("/notify-test", s.handleNotifyTest)
	mux.HandleFunc("/search-chats", s.handleSearchChats)
	mux.HandleFunc("/git-authors", s.handleGitAuthors)
	mux.HandleFunc("/generate", s.handleGenerate)
	mux.HandleFunc("/autostart", s.handleAutostart)
	mux.HandleFunc("/pick-dir", s.handlePickDir)
	mux.HandleFunc("/open-path", s.handleOpenPath)
	mux.HandleFunc("/edit-config", s.handleEditConfig)

	ln, err := net.Listen("tcp", "127.0.0.1:0") // 只绑回环地址 = 只有本机能访问
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	configURL = fmt.Sprintf("http://127.0.0.1:%d/", port)
	log.Printf("配置界面: %s", configURL)
	if autoOpen {
		fmt.Println(configURL) // stdout 也打一份, 手动 serve 时直接可见
		openBrowser(configURL)
	}
	return http.Serve(ln, mux)
}

// openBrowser 用系统默认浏览器打开 URL(cmd /c start 不阻塞)。
func openBrowser(url string) {
	go func() {
		cmd := exec.Command("cmd", "/c", "start", "", url)
		cmd.Run()
	}()
}
