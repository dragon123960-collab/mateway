const watch = { sessionKey: "", sessions: [], events: [], ws: null, filter: "all" };

function esc(text) {
  return String(text ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

async function getJSON(path) {
  const res = await fetch(path);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

async function refreshWatch() {
  const data = await getJSON("/api/sessions");
  watch.sessions = data.sessions || [];
  if (!watch.sessionKey && watch.sessions[0]) watch.sessionKey = watch.sessions[0].key;
  renderSessions();
  if (watch.sessionKey) {
    await loadRuns(watch.sessionKey);
    connect(watch.sessionKey);
  } else {
    renderWatch();
  }
}

function renderSessions() {
  document.getElementById("watch-sessions").innerHTML = watch.sessions.map(s => `
    <button class="session-item ${s.key === watch.sessionKey ? "active" : ""}" data-session="${esc(s.key)}">
      <strong>${esc(s.key)}</strong>
      <div class="meta">${esc(s.tasks)} tasks · tokens ${esc(s.usage?.total_tokens || 0)}</div>
      <div class="meta">${esc(s.last_summary || s.pending || "")}</div>
    </button>`).join("") || `<div class="meta">暂无 session</div>`;
}

async function loadRuns(sessionKey) {
  const runs = await getJSON(`/api/runs?session_key=${encodeURIComponent(sessionKey)}`).catch(() => ({ runs: [] }));
  const latest = runs.runs?.[0];
  if (latest?.trace_id) {
    const detail = await getJSON(`/api/runs/${encodeURIComponent(latest.trace_id)}`).catch(() => null);
    watch.events = (detail?.events || []).map(e => normalizeEvent({ type: eventType(e?.type), time: e?.time, trace_id: e?.trace_id, payload: e || {} }));
  } else {
    watch.events = [];
  }
  renderWatch();
}

function connect(sessionKey) {
  if (!sessionKey) return;
  if (watch.ws) watch.ws.close();
  const protocol = location.protocol === "https:" ? "wss" : "ws";
  watch.ws = new WebSocket(`${protocol}://${location.host}/api/events/ws?session_key=${encodeURIComponent(sessionKey)}`);
  watch.ws.onmessage = event => {
    const data = normalizeEvent(JSON.parse(event.data));
    watch.events.push(data);
    if (watch.events.length > 500) watch.events = watch.events.slice(-500);
    renderWatch();
  };
}

function renderWatch() {
  document.getElementById("watch-title").textContent = watch.sessionKey || "Mateway Runtime";
  const state = agentState();
  const latest = watch.events[watch.events.length - 1];
  const avatar = document.getElementById("agent-avatar");
  avatar.className = `agent-avatar ${state.key}`;
  document.getElementById("watch-board").textContent = state.board;
  document.getElementById("watch-monitor").textContent = state.monitor;
  document.getElementById("agent-state-pill").className = `state-pill ${state.key}`;
  document.getElementById("agent-state-pill").textContent = state.label;
  document.getElementById("agent-state-detail").innerHTML = stateDetailHTML(state, latest);
  document.getElementById("watch-skills").innerHTML = skillsHTML();
  document.getElementById("watch-tools").innerHTML = toolsHTML();
  document.getElementById("watch-timeline").innerHTML = timelineHTML();
  document.getElementById("watch-metrics").innerHTML = metricsHTML(state);
}

function agentState() {
  const latest = watch.events[watch.events.length - 1];
  const activeTool = lastToolStartWithoutEnd();
  if (activeTool) {
    return {
      key: "tooling",
      label: "Working",
      board: `Tool · ${toolName(activeTool)}`,
      monitor: toolMonitor(activeTool),
      event: activeTool,
    };
  }
  const type = latest?.type || "idle";
  if (type === "model_started" || type === "context_built") {
    return { key: "thinking", label: "Thinking", board: label(type), monitor: monitorText(latest), event: latest };
  }
  if (type === "model_finished") {
    const calls = latest.payload?.message?.ToolCalls || latest.payload?.message?.tool_calls || [];
    if (calls.length) {
      return { key: "thinking", label: "Choosing Tools", board: `${calls.length} tool call(s) selected`, monitor: calls.map(c => c.Name || c.name).join("\n"), event: latest };
    }
    return { key: "thinking", label: "Composing", board: "Model response", monitor: monitorText(latest), event: latest };
  }
  if (type === "task_blocked") return { key: "waiting", label: "Waiting", board: "Waiting for input", monitor: monitorText(latest), event: latest };
  if (type === "task_completed" || type === "runtime_done" || type === "reply") return { key: "done", label: "Done", board: "Completed", monitor: monitorText(latest), event: latest };
  if (type === "error" || type === "task_warning") return { key: "error", label: "Issue", board: "Needs attention", monitor: monitorText(latest), event: latest };
  return { key: "idle", label: "Resting", board: "Idle", monitor: "等待任务", event: latest };
}

function stateDetailHTML(state, latest) {
  const p = state.event?.payload || latest?.payload || {};
  return `
    <div class="state-line"><span>状态</span><strong>${esc(state.label)}</strong></div>
    <div class="state-line"><span>当前</span><strong>${esc(state.board)}</strong></div>
    <div class="state-copy">${esc(state.monitor || detail(latest))}</div>
    ${p.task_id ? `<div class="meta">task ${esc(p.task_id)}</div>` : ""}
    ${p.trace_id ? `<div class="meta">trace ${esc(p.trace_id)}</div>` : ""}
  `;
}

function skillsHTML() {
  const skills = latestSkills();
  if (!skills.length) return `<div class="empty">本轮还没有 skill 选择事件</div>`;
  return skills.map(s => `
    <div class="skill-card ${esc(s.state || "active")}">
      <div>
        <strong>${esc(s.name)}</strong>
        <span>${esc(s.scope || "workspace")} · ${esc(s.state || "active")}</span>
      </div>
      <p>${esc(s.description || firstListValue(s.when_to_use) || "guidance injected")}</p>
      ${s.stage || s.priority ? `<div class="meta">${esc([s.stage && `stage ${s.stage}`, s.priority && `priority ${s.priority}`].filter(Boolean).join(" · "))}</div>` : ""}
    </div>
  `).join("");
}

function latestSkills() {
  for (let i = watch.events.length - 1; i >= 0; i--) {
    if (watch.events[i].type !== "skills_selected") continue;
    return watch.events[i].payload?.skills || [];
  }
  return [];
}

function toolsHTML() {
  const runs = toolRuns();
  if (!runs.length) return `<div class="empty">还没有工具执行</div>`;
  return runs.slice(-12).reverse().map(run => `
    <article class="tool-card ${esc(run.status)}">
      <header>
        <strong>${esc(run.name)}</strong>
        <span>${esc(run.statusLabel)}</span>
      </header>
      <div class="tool-args">${kvHTML(run.args)}</div>
      ${run.summary ? `<p>${esc(run.summary)}</p>` : ""}
      ${run.duration_ms ? `<div class="meta">${esc(run.duration_ms)} ms</div>` : ""}
      ${run.evidence ? `<details><summary>Evidence</summary>${kvHTML(run.evidence)}</details>` : ""}
    </article>
  `).join("");
}

function toolRuns() {
  const byID = new Map();
  const order = [];
  for (const event of watch.events) {
    if (event.type !== "tool_started" && event.type !== "tool_finished") continue;
    const t = toolPayload(event);
    const id = t.id || t.result_id || `${event.time}-${t.name}`;
    if (!byID.has(id)) {
      byID.set(id, { id, name: t.name || "", args: t.args || {}, status: "running", statusLabel: "running" });
      order.push(id);
    }
    const run = byID.get(id);
    run.name = t.name || run.name;
    run.args = t.args || run.args;
    if (event.type === "tool_finished") {
      run.status = t.status === "error" ? "error" : "done";
      run.statusLabel = t.status === "error" ? "error" : "done";
      run.summary = t.summary || t.content || "";
      run.evidence = t.evidence;
      run.duration_ms = t.duration_ms;
    }
  }
  return order.map(id => byID.get(id));
}

function timelineHTML() {
  const filtered = watch.events.filter(event => matchesFilter(event, watch.filter));
  return filtered.slice(-120).reverse().map(raw => {
    const e = normalizeEvent(raw);
    return `<div class="event-row ${esc(eventClass(e))}">
      <div class="event-main">
        <span>${esc(label(e.type))}</span>
        <strong>${esc(detail(e))}</strong>
      </div>
      ${eventExtraHTML(e)}
      <div class="meta">${esc(shortTime(e.time))}</div>
    </div>`;
  }).join("") || `<div class="meta">等待实时事件</div>`;
}

function metricsHTML(state) {
  const usage = latestPayload("usage_delta") || {};
  const context = latestPayload("context_built") || {};
  const counts = eventCounts();
  const tools = toolRuns();
  return [
    ["Agent", state.label],
    ["Events", watch.events.length],
    ["Model Turns", counts.model],
    ["Tool Runs", tools.length],
    ["Skills", latestSkills().length],
    ["Input Tokens", usage.input_tokens || 0],
    ["Output Tokens", usage.output_tokens || 0],
    ["Total Tokens", usage.total_tokens || 0],
    ["Context", `${context.estimated_context || context.estimated_tokens || 0} est`],
  ].map(([k, v]) => `<div class="card"><span class="meta">${esc(k)}</span><strong>${esc(v)}</strong></div>`).join("");
}

function latestPayload(type) {
  for (let i = watch.events.length - 1; i >= 0; i--) {
    if (watch.events[i]?.type === type) return watch.events[i].payload || {};
  }
  return null;
}

function eventCounts() {
  return watch.events.reduce((acc, event) => {
    const type = event?.type || "";
    if (type === "model_started") acc.model++;
    if (type === "tool_finished") acc.tool++;
    return acc;
  }, { model: 0, tool: 0 });
}

function lastToolStartWithoutEnd() {
  const done = new Set();
  for (let i = watch.events.length - 1; i >= 0; i--) {
    const event = watch.events[i];
    if (event.type !== "tool_started" && event.type !== "tool_finished") continue;
    const t = toolPayload(event);
    const id = t.id || t.result_id || t.name;
    if (event.type === "tool_finished") {
      done.add(id);
      continue;
    }
    if (!done.has(id)) return event;
  }
  return null;
}

function eventType(traceType) {
  return ({
    request: "runtime_started",
    agent_start: "task_created",
    task_created: "task_created",
    skills_selected: "skills_selected",
    hook_event: "hook_event",
    context_built: "context_built",
    turn_start: "model_started",
    message_start: "model_finished",
    tool_execution_start: "tool_started",
    tool_execution_end: "tool_finished",
    model_usage: "usage_delta",
    reply: "reply",
    task_completed: "task_completed",
    task_warning: "task_warning",
    task_blocked: "task_blocked",
    runtime_done: "runtime_done",
    model_error: "error",
    hook_warning: "error",
  })[traceType] || traceType;
}

function label(type) {
  return ({
    connected: "Connected",
    runtime_started: "Task Published",
    task_created: "Task Created",
    skills_selected: "Skills Selected",
    hook_event: "Hook",
    context_built: "Context Built",
    model_started: "Model Started",
    model_finished: "Model Finished",
    tool_started: "Tool Started",
    tool_finished: "Tool Finished",
    usage_delta: "Usage",
    reply: "Reply",
    task_completed: "Completed",
    task_warning: "Warning",
    task_blocked: "Waiting",
    runtime_done: "Runtime Done",
    error: "Error",
  })[type] || type;
}

function detail(event) {
  const p = event?.payload || {};
  if (event?.type === "skills_selected") return `${(p.skills || []).length} skill(s)`;
  if (event?.type === "tool_started" || event?.type === "tool_finished") return toolName(event);
  if (event?.type === "model_finished") {
    const calls = p.message?.ToolCalls || p.message?.tool_calls || [];
    if (calls.length) return calls.map(c => c.Name || c.name).join(", ");
  }
  return p.warning || p.text || p.summary || p.status || p.error || p.hook || p.trace_id || "";
}

function eventExtraHTML(event) {
  if (event.type === "tool_started" || event.type === "tool_finished") {
    const t = toolPayload(event);
    return `<div class="event-extra">${kvHTML(t.args)}${t.summary ? `<p>${esc(t.summary)}</p>` : ""}</div>`;
  }
  if (event.type === "skills_selected") {
    const skills = event.payload?.skills || [];
    return `<div class="event-extra chips">${skills.map(s => `<span>${esc(s.name)} · ${esc(s.state || "active")}</span>`).join("")}</div>`;
  }
  if (event.type === "context_built") {
    const p = event.payload || {};
    return `<div class="event-extra"><span class="meta">${esc(p.estimated_context || 0)} est / window ${esc(p.context_window || 0)}</span></div>`;
  }
  return "";
}

function monitorText(event) {
  event = normalizeEvent(event);
  if (!event) return "等待任务";
  if (event.type === "context_built") {
    const p = event.payload || {};
    return `context ${p.estimated_context || p.estimated_tokens || 0} est / window ${p.context_window || 0}`;
  }
  if (event.type === "usage_delta") {
    const p = event.payload || {};
    return `usage input ${p.input_tokens || 0}, output ${p.output_tokens || 0}, total ${p.total_tokens || 0}`;
  }
  return `${label(event.type)}\n${detail(event)}`;
}

function toolMonitor(event) {
  const t = toolPayload(event);
  return `${t.name || "tool"}\n${Object.entries(t.args || {}).map(([k, v]) => `${k}: ${formatValue(v)}`).join("\n")}`;
}

function toolPayload(event) {
  const p = event?.payload || {};
  const t = p.tool || {};
  const call = p.tool_call || {};
  const result = p.tool_result || {};
  return {
    id: t.id || call.ID || call.id || result.ToolCallID || result.tool_call_id || "",
    result_id: t.result_id || result.ToolCallID || result.tool_call_id || "",
    name: t.name || call.Name || call.name || "",
    args: t.args || call.Args || call.args || {},
    status: t.status || (result.IsError || result.is_error ? "error" : result.ToolCallID ? "accepted" : ""),
    summary: t.summary || "",
    content: t.content || result.Content || result.content || "",
    evidence: t.evidence || result.Evidence || result.evidence,
    duration_ms: t.duration_ms || p.duration_ms || 0,
  };
}

function toolName(event) {
  return toolPayload(event).name || "tool";
}

function kvHTML(value) {
  const entries = Object.entries(value || {});
  if (!entries.length) return `<div class="meta">无参数</div>`;
  return `<dl>${entries.slice(0, 8).map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(formatValue(v))}</dd>`).join("")}</dl>`;
}

function formatValue(value) {
  if (value == null) return "";
  if (typeof value === "string") return value.length > 240 ? `${value.slice(0, 240)}...` : value;
  const text = JSON.stringify(value);
  return text.length > 240 ? `${text.slice(0, 240)}...` : text;
}

function firstListValue(value) {
  return Array.isArray(value) ? value[0] : "";
}

function normalizeEvent(event) {
  if (!event || typeof event !== "object") return { type: "unknown", payload: {} };
  return { ...event, type: event.type || "unknown", payload: event.payload || {} };
}

function matchesFilter(event, filter) {
  if (filter === "all") return true;
  if (filter === "tool") return event.type === "tool_started" || event.type === "tool_finished";
  if (filter === "skill") return event.type === "skills_selected";
  if (filter === "model") return event.type === "model_started" || event.type === "model_finished" || event.type === "context_built" || event.type === "usage_delta";
  return true;
}

function eventClass(event) {
  if (event.type.startsWith("tool_")) return "tool";
  if (event.type.startsWith("task_") || event.type === "error") return event.type;
  return event.type;
}

function shortTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleTimeString();
}

document.addEventListener("click", event => {
  const sessionButton = event.target.closest("[data-session]");
  if (sessionButton) {
    watch.sessionKey = sessionButton.dataset.session;
    watch.events = [];
    renderSessions();
    loadRuns(watch.sessionKey).then(() => connect(watch.sessionKey));
    return;
  }
  const filterButton = event.target.closest("[data-filter]");
  if (filterButton) {
    watch.filter = filterButton.dataset.filter;
    document.querySelectorAll("[data-filter]").forEach(btn => btn.classList.toggle("active", btn === filterButton));
    renderWatch();
  }
});

document.getElementById("refresh-watch").onclick = refreshWatch;
refreshWatch().catch(err => {
  document.getElementById("watch-timeline").innerHTML = `<div class="card">Error: ${esc(err.message)}</div>`;
});
