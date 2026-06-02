const state = { view: "chat", overview: null, sessionKey: null, sessions: [], pending: [], events: [], ws: null, wsSession: "" };

const titles = {
  chat: ["对话", "向本地 runtime 发布任务"],
  skills: ["技能", "查看 active/cold/hidden skills"],
  schedules: ["定时任务", "创建、暂停和激活计划任务"],
  config: ["配置", "模型、channels、agents 和非 secret 配置"],
  usage: ["记忆", "查看 memory、learning 和 proposals"],
};

async function api(path, options = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function h(text) {
  return String(text ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function setView(view) {
  state.view = view;
  document.querySelectorAll(".nav").forEach(btn => btn.classList.toggle("active", btn.dataset.view === view));
  document.getElementById("view-title").textContent = titles[view][0];
  document.getElementById("view-subtitle").textContent = titles[view][1];
  render();
}

function requestRender() {
  return render().catch(err => {
    document.getElementById("content").innerHTML = `<div class="card">Error: ${h(err.message)}</div>`;
  });
}

async function refreshOverview() {
  state.overview = await api("/api/overview");
  const s = state.overview;
  document.getElementById("status").innerHTML = [
    ["Home", s.home],
    ["Workspace", s.workspace],
    ["Model", s.model],
    ["Web", s.web_bind],
    ["Sessions", s.sessions],
    ["Context tokens", s.total_tokens || 0],
    ["Skill events", s.skill_usage],
    ["Realtime", s.realtime_enabled ? "on" : "off"],
  ].map(([k, v]) => `<div class="kv"><span>${h(k)}</span><strong>${h(v)}</strong></div>`).join("");
}

async function render() {
  await refreshOverview();
  const fn = {
    chat: renderChat,
    skills: renderSkills,
    schedules: renderSchedules,
    config: renderConfig,
    usage: renderUsage,
  }[state.view];
  await fn();
  document.getElementById("refresh").textContent = "已刷新";
  setTimeout(() => { document.getElementById("refresh").textContent = "刷新"; }, 900);
}

async function renderChat() {
  const sessionData = await api("/api/sessions");
  state.sessions = sessionData.sessions || [];
  if (state.sessionKey === null) {
    state.sessionKey = state.sessions[0]?.key || "";
  }
  let detail = null;
  if (state.sessionKey) {
    detail = await api(`/api/sessions/${encodeURIComponent(state.sessionKey)}`).catch(() => null);
  }
  const persisted = detail?.messages?.map(m => ({ role: String(m.Role || m.role || "").toLowerCase(), text: m.Content || m.content || "" })) || [];
  const pending = state.pending
    .filter(m => (m.sessionKey || "") === (state.sessionKey || ""))
    .filter(m => !persisted.some(p => baseRole(p.role) === baseRole(m.role) && p.text === m.text));
  const messages = [...persisted, ...pending];
  const tasks = detail?.tasks || [];
  document.getElementById("content").innerHTML = `
    <div class="chat-shell">
      <div class="card session-pane">
        <h2>Sessions</h2>
        <button class="primary" data-action="new-session">新建会话</button>
        <div class="session-list">
          ${state.sessions.map(sessionButton).join("") || `<div class="meta">暂无 session</div>`}
        </div>
      </div>
      <div class="conversation-pane">
        <div class="card session-context">
          <strong>${h(state.sessionKey || "新 Web 会话")}</strong>
          <span class="meta">继续此 session 会追加消息和任务树；长期记忆仍按 workspace/agent 规则沉淀。</span>
          ${tasks.length ? `<div class="meta">Tasks: ${tasks.slice(-3).map(t => `${h(t.id || t.ID)} ${h(t.status || t.Status || "")}`).join(" · ")}</div>` : ""}
        </div>
        <div class="card chat-log" id="chat-log">${messages.map(messageBubble).join("") || `<div class="meta">选择 session 或发送一条新消息。</div>`}</div>
        <div class="card">
          <textarea id="chat-input" placeholder="输入任务或问题"></textarea>
          <div class="composer-actions">
            <span class="meta">发送后会立即显示在当前 session，后台完成后再刷新回复。</span>
            <button class="primary" id="send-chat">发送</button>
          </div>
        </div>
      </div>
      <div class="card live-pane">
        <div class="live-head">
          <h2>Live Run</h2>
          <a href="/watch" target="_blank">Office Watch</a>
        </div>
        ${renderLiveStats()}
        <div class="timeline">${state.events.slice(-24).map(eventRow).join("") || `<div class="meta">发送任务后显示实时执行过程。</div>`}</div>
      </div>
    </div>`;
  scrollChatToBottom();
  document.getElementById("send-chat").onclick = async () => {
    const input = document.getElementById("chat-input");
    const text = input.value.trim();
    if (!text) return;
    const sessionKey = state.sessionKey || defaultSessionKey();
    state.sessionKey = sessionKey;
    ensureRealtime(sessionKey);
    input.value = "";
    state.pending = [
      { role: "user", text, sessionKey },
      { role: "assistant pending", text: "处理中...", sessionKey },
    ];
    await renderChat();
    try {
      const out = await api("/api/chat", { method: "POST", body: JSON.stringify({ message: text, session_key: sessionKey }) });
      state.sessionKey = out.session_key || state.sessionKey;
      state.pending = [
        { role: "user", text, sessionKey: state.sessionKey },
        { role: "assistant", text: replyText(out.reply), sessionKey: state.sessionKey },
      ];
    } catch (err) {
      state.pending = [{ role: "assistant pending", text: "发送失败：" + err.message, sessionKey }];
    } finally {
      await requestRender();
    }
  };
}

function ensureRealtime(sessionKey) {
  if (!sessionKey || state.wsSession === sessionKey && state.ws) return;
  closeRealtime();
  state.wsSession = sessionKey;
  state.events = [];
  const protocol = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${protocol}://${location.host}/api/events/ws?session_key=${encodeURIComponent(sessionKey)}`);
  state.ws = ws;
  ws.onmessage = event => {
    try {
      const data = normalizeEvent(JSON.parse(event.data));
      state.events.push(data);
      if (state.events.length > 200) state.events = state.events.slice(-200);
      updateLivePane();
    } catch (_) {}
  };
  ws.onclose = () => {
    if (state.ws === ws) state.ws = null;
  };
}

function closeRealtime() {
  if (state.ws) state.ws.close();
  state.ws = null;
  state.wsSession = "";
}

function updateLivePane() {
  const pane = document.querySelector(".live-pane");
  if (!pane) return;
  pane.innerHTML = `
    <div class="live-head">
      <h2>Live Run</h2>
      <a href="/watch" target="_blank">Office Watch</a>
    </div>
    ${renderLiveStats()}
    <div class="timeline">${state.events.slice(-24).map(eventRow).join("") || `<div class="meta">发送任务后显示实时执行过程。</div>`}</div>`;
}

function renderLiveStats() {
  const usage = latestPayload("usage_delta") || {};
  const context = latestPayload("context_built") || {};
  const counts = liveEventCounts();
  const latest = state.events[state.events.length - 1] || {};
  return `<div class="live-stats">
    <div><span>Stage</span><strong>${h(eventLabel(latest.type || "idle"))}</strong></div>
    <div><span>Events</span><strong>${h(state.events.length)}</strong></div>
    <div><span>Model</span><strong>${h(counts.model)}</strong></div>
    <div><span>Tools</span><strong>${h(counts.tool)}</strong></div>
    <div><span>Input</span><strong>${h(usage.input_tokens || 0)}</strong></div>
    <div><span>Total</span><strong>${h(usage.total_tokens || 0)}</strong></div>
    <div><span>Context</span><strong>${h(context.estimated_context || context.estimated_tokens || 0)} est</strong></div>
  </div>`;
}

function latestPayload(type) {
  for (let i = state.events.length - 1; i >= 0; i--) {
    if (state.events[i]?.type === type) return state.events[i].payload || {};
  }
  return null;
}

function liveEventCounts() {
  return state.events.reduce((acc, event) => {
    const type = event?.type || "";
    if (type === "model_started" || type === "model_finished") acc.model++;
    if (type === "tool_started" || type === "tool_finished") acc.tool++;
    return acc;
  }, { model: 0, tool: 0 });
}

function eventRow(event) {
  event = normalizeEvent(event);
  const payload = event.payload || {};
  const label = eventLabel(event.type);
  const detail = payload.tool_call?.Name || payload.tool_call?.name || payload.hook || payload.text || payload.summary || payload.status || payload.error || "";
  const finalDetail = payload.warning || detail;
  return `<div class="event-row ${h(event.type)}"><span>${h(label)}</span><strong>${h(finalDetail)}</strong></div>`;
}

function normalizeEvent(event) {
  if (!event || typeof event !== "object") return { type: "unknown", payload: {} };
  return { ...event, type: event.type || "unknown", payload: event.payload || {} };
}

function eventLabel(type) {
  return ({
    connected: "connected",
    runtime_started: "request",
    task_created: "task",
    hook_event: "hook",
    context_built: "context",
    model_started: "model start",
    model_finished: "model done",
    tool_started: "tool start",
    tool_finished: "tool done",
    usage_delta: "usage",
    reply: "reply",
    task_warning: "warning",
    task_completed: "done",
    task_blocked: "waiting",
    runtime_done: "finished",
    error: "error",
  })[type] || type;
}

function messageBubble(m) {
  const role = baseRole(m.role);
  const pending = String(m.role || "").includes("pending") ? " pending" : "";
  return `<div class="message ${h(role)}${pending}">${h(m.text)}</div>`;
}

function baseRole(role) {
  return String(role || "").replace(/[^a-z ]/g, "").trim().split(/\s+/)[0] || "assistant";
}

function replyText(reply) {
  if (typeof reply === "string") return reply || "已发送，等待后续输入。";
  if (!reply) return "已发送，等待后续输入。";
  for (const key of ["content", "Content", "text", "Text", "message", "Message", "reply", "Reply"]) {
    if (typeof reply[key] === "string" && reply[key]) return reply[key];
  }
  return JSON.stringify(reply, null, 2);
}

function scrollChatToBottom() {
  const log = document.getElementById("chat-log");
  if (log) log.scrollTop = log.scrollHeight;
}

function sessionButton(s) {
  const active = s.key === state.sessionKey ? "active" : "";
  return `<button class="session-item ${active}" data-action="select-session" data-session-key="${h(s.key)}">
    <strong>${h(s.key)}</strong>
    <div class="meta">${h(s.messages)} messages · ${h(s.tasks)} tasks · tokens ${h(s.usage?.total_tokens || 0)}</div>
    <div class="meta">${h(s.last_summary || "")}</div>
  </button>`;
}

function selectSession(key) {
  state.sessionKey = key;
  state.events = [];
  if (key) ensureRealtime(key);
  state.view = "chat";
  document.querySelectorAll(".nav").forEach(btn => btn.classList.toggle("active", btn.dataset.view === "chat"));
  requestRender();
}

function newSession() {
  const workspace = String(state.overview?.workspace || "default").split("/").filter(Boolean).pop() || "default";
  const stamp = new Date().toISOString().replace(/[-:.TZ]/g, "").slice(0, 14);
  selectSession(`web:${workspace}:${stamp}`);
}

function defaultSessionKey() {
  const workspace = String(state.overview?.workspace || "default").split("/").filter(Boolean).pop() || "default";
  return `web:${workspace}`;
}

async function renderSkills() {
  const data = await api("/api/skills");
  const cleanup = await api("/api/skills/cleanup").catch(() => null);
  document.getElementById("content").innerHTML = `
    <div class="grid">
      <div class="card"><h2>Active</h2><strong>${h(cleanup?.Active ?? cleanup?.active ?? 0)}</strong><p>完整注入 guidance</p></div>
      <div class="card"><h2>Cold</h2><strong>${h(cleanup?.Cold ?? cleanup?.cold ?? 0)}</strong><p>只保留召回卡片</p></div>
      <div class="card"><h2>Hidden</h2><strong>${h(cleanup?.Hidden ?? cleanup?.hidden ?? 0)}</strong><p>不进入上下文，可手动恢复</p></div>
    </div>
    <div class="card">${data.skills.map(skillRow).join("")}</div>`;
}

function skillRow(s) {
  const restore = s.state === "hidden" || s.state === "cold"
    ? `<button data-action="restore-skill" data-skill-id="${h(s.id)}">恢复</button>` : "";
  return `<div class="row">
    <div><strong>${h(s.name)}</strong> <span class="pill ${h(s.state)}">${h(s.state)}</span>
      <div class="meta">${h(s.scope)} · usage ${h(s.usage_count)} · ${h(s.description)}</div>
      <div class="meta">${h(s.reason || s.path)}</div>
    </div>${restore}</div>`;
}

async function restoreSkill(id) {
  await api(`/api/skills/${encodeURIComponent(id)}/restore`, { method: "POST" });
  render();
}

async function renderSchedules() {
  const data = await api("/api/schedules");
  document.getElementById("content").innerHTML = `
    <div class="card">
      <div class="grid">
        <input id="sch-text" placeholder="任务内容" />
        <input id="sch-run-at" placeholder="2026-06-02T18:00:00+08:00" />
        <input id="sch-interval" placeholder="interval 可选，如 24h" />
      </div>
      <div style="height:10px"></div><button class="primary" id="create-sch">创建</button>
    </div>
    <div class="card">${(data.schedules || []).map(scheduleRow).join("") || `<div class="meta">暂无定时任务。创建后会显示在这里。</div>`}</div>`;
  document.getElementById("create-sch").onclick = async () => {
    await api("/api/schedules", { method: "POST", body: JSON.stringify({
      text: document.getElementById("sch-text").value,
      run_at: document.getElementById("sch-run-at").value,
      interval: document.getElementById("sch-interval").value,
      require_test: true,
    })});
    render();
  };
}

function scheduleRow(t) {
  const next = t.status === "active" ? "pause" : "activate";
  const nextLabel = t.status === "active" ? "暂停" : "激活";
  return `<div class="row"><div><strong>${h(t.id)}</strong> <span class="pill">${h(t.status)}</span>
    <div class="meta">${h(t.run_at)} · ${h(t.interval || "one-shot")}</div>
    <div>${h(t.text)}</div></div><div><button data-action="schedule" data-schedule-id="${h(t.id)}" data-schedule-action="test">测试</button> <button data-action="schedule" data-schedule-id="${h(t.id)}" data-schedule-action="${h(next)}">${nextLabel}</button></div></div>`;
}

async function scheduleAction(id, action) {
  await api(`/api/schedules/${encodeURIComponent(id)}/${action}`, { method: "PATCH" });
  render();
}

async function renderSessions() {
  const data = await api("/api/sessions");
  document.getElementById("content").innerHTML = `<div class="card">${data.sessions.map(s => `
    <div class="row"><div><strong>${h(s.key)}</strong><div class="meta">${h(s.messages)} messages · ${h(s.tasks)} tasks · tokens ${h(s.usage?.total_tokens || 0)}</div><div>${h(s.last_summary)}</div></div></div>`).join("")}</div>`;
}

async function renderChannels() {
  const data = await api("/api/channels");
  return `<div class="card"><h2>Channels</h2>${data.channels.map(c => `
    <div class="row"><div><strong>${h(c.id)}</strong> <span class="pill ${c.enabled ? "active" : "hidden"}">${c.enabled ? "enabled" : "disabled"}</span><div class="meta">${h(c.config || c.bind)}</div></div>
    ${c.id === "web" ? `<span class="meta">随 gateway serve 启动</span>` : `<button data-action="toggle-channel" data-channel-id="${h(c.id)}" data-enabled="${c.enabled ? "false" : "true"}">${c.enabled ? "关闭" : "开启"}</button>`}</div>`).join("")}</div>`;
}

async function toggleChannel(id, enabled) {
  await api(`/api/channels/${encodeURIComponent(id)}/enabled`, { method: "PATCH", body: JSON.stringify({ enabled }) });
  render();
}

async function renderAgents() {
  const data = await api("/api/agents");
  return `<div class="card"><h2>Agents</h2>${data.agents.map(a => `
    <div class="row"><div><strong>${h(a.ID || a.id)}</strong><div class="meta">${h(a.Name || a.name || "")}</div></div></div>`).join("")}</div>`;
}

async function renderConfig() {
  const data = await api("/api/config");
  const channels = await renderChannels();
  const agents = await renderAgents();
  const models = data.models || [];
  const options = models.map(m => `<option value="${h(m.name)}" ${m.name === data.model?.default ? "selected" : ""}>${h(m.name)} · ${h(m.provider || "")}</option>`).join("");
  document.getElementById("content").innerHTML = `
    <div class="card">
      <h2>大模型</h2>
      <div class="grid">
        <label>默认模型<select id="model-default">${options}</select></label>
        <label>Web 监听地址<input id="web-bind" value="${h(data.web?.bind || "")}" /></label>
      </div>
      <div style="height:10px"></div>
      <button class="primary" id="save-config">保存配置</button>
    </div>
    ${channels}
    ${agents}
    <div class="card"><h2>当前配置</h2><pre>${h(JSON.stringify(data, null, 2))}</pre></div>`;
  document.getElementById("save-config").onclick = async () => {
    await api("/api/config", { method: "PATCH", body: JSON.stringify({
      model: { ...data.model, default: document.getElementById("model-default").value },
      web: { ...data.web, bind: document.getElementById("web-bind").value },
    })});
    render();
  };
}

async function renderUsage() {
  const data = await api("/api/memory/report");
  const learning = data.learning || {};
  const memory = data.memory || {};
  document.getElementById("content").innerHTML = `
    <div class="grid">
      <div class="card"><h2>Learning Tasks</h2><strong>${h(learning.Tasks ?? learning.tasks ?? 0)}</strong><p>已完成任务证据</p></div>
      <div class="card"><h2>Skill Usage</h2><strong>${h(learning.SkillUsage ?? learning.skill_usage ?? 0)}</strong><p>skills 调用证据</p></div>
      <div class="card"><h2>Pending Proposals</h2><strong>${h(learning.MemoryProposalsPending ?? learning.memory_proposals_pending ?? 0)}</strong><p>待审核长期记忆</p></div>
    </div>
    <div class="grid">
      <div class="card"><h2>Learning</h2><pre>${h(JSON.stringify(learning, null, 2))}</pre></div>
      <div class="card"><h2>Memory</h2><pre>${h(JSON.stringify(memory, null, 2))}</pre></div>
    </div>`;
}

document.addEventListener("click", async event => {
  const actionEl = event.target.closest("[data-action]");
  if (!actionEl) return;
  const action = actionEl.dataset.action;
  if (action === "select-session") {
    selectSession(actionEl.dataset.sessionKey || "");
  } else if (action === "new-session") {
    newSession();
  } else if (action === "restore-skill") {
    await restoreSkill(actionEl.dataset.skillId);
  } else if (action === "schedule") {
    await scheduleAction(actionEl.dataset.scheduleId, actionEl.dataset.scheduleAction);
  } else if (action === "toggle-channel") {
    await toggleChannel(actionEl.dataset.channelId, actionEl.dataset.enabled === "true");
  }
});

document.querySelectorAll(".nav").forEach(btn => btn.onclick = () => setView(btn.dataset.view));
document.getElementById("refresh").onclick = requestRender;
requestRender();
