const watch = { sessionKey: "", sessions: [], events: [], ws: null };

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
    if (watch.events.length > 300) watch.events = watch.events.slice(-300);
    renderWatch();
  };
}

function renderWatch() {
  document.getElementById("watch-title").textContent = watch.sessionKey || "Mateway Runtime";
  const latest = watch.events[watch.events.length - 1];
  const status = agentStatus(latest?.type);
  const avatar = document.getElementById("agent-avatar");
  avatar.className = `agent-avatar ${status}`;
  document.getElementById("watch-board").textContent = latest ? `${label(latest.type)} · ${detail(latest)}` : "idle";
  document.getElementById("watch-monitor").textContent = monitorText(latest);
  document.getElementById("watch-timeline").innerHTML = watch.events.slice(-80).reverse().map(raw => {
    const e = normalizeEvent(raw);
    return `<div class="event-row ${esc(e.type)}"><span>${esc(label(e.type))}</span><strong>${esc(detail(e))}</strong><div class="meta">${esc(e.time || "")}</div></div>`;
  }).join("") || `<div class="meta">等待实时事件</div>`;
  const usage = latestPayload("usage_delta") || {};
  const context = latestPayload("context_built") || {};
  document.getElementById("watch-metrics").innerHTML = [
    ["Input", usage.input_tokens || 0],
    ["Output", usage.output_tokens || 0],
    ["Total", usage.total_tokens || 0],
    ["Context", `${context.estimated_context || context.estimated_tokens || 0} est`],
  ].map(([k, v]) => `<div class="card"><span class="meta">${esc(k)}</span><strong>${esc(v)}</strong></div>`).join("");
}

function latestPayload(type) {
  for (let i = watch.events.length - 1; i >= 0; i--) {
    if (watch.events[i]?.type === type) return watch.events[i].payload || {};
  }
  return null;
}

function eventType(traceType) {
  return ({
    request: "runtime_started",
    agent_start: "task_created",
    task_created: "task_created",
    hook_event: "hook_event",
    context_built: "context_built",
    turn_start: "model_started",
    message_start: "model_finished",
    tool_execution_start: "tool_started",
    tool_execution_end: "tool_finished",
    model_usage: "usage_delta",
    reply: "reply",
    task_completed: "task_completed",
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
    hook_event: "Hook",
    context_built: "Context Built",
    model_started: "Model Started",
    model_finished: "Model Finished",
    tool_started: "Tool Started",
    tool_finished: "Tool Finished",
    usage_delta: "Usage",
    reply: "Reply",
    task_completed: "Completed",
    task_blocked: "Waiting",
    runtime_done: "Runtime Done",
    error: "Error",
  })[type] || type;
}

function detail(event) {
  const p = event?.payload || {};
  return p.text || p.summary || p.status || p.error || p.hook || p.tool_call?.Name || p.tool_call?.name || p.trace_id || "";
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

function normalizeEvent(event) {
  if (!event || typeof event !== "object") return { type: "unknown", payload: {} };
  return { ...event, type: event.type || "unknown", payload: event.payload || {} };
}

function agentStatus(type) {
  if (type === "model_started" || type === "model_finished" || type === "context_built") return "thinking";
  if (type === "tool_started" || type === "tool_finished") return "tooling";
  if (type === "task_blocked") return "waiting";
  if (type === "task_completed" || type === "runtime_done" || type === "reply") return "done";
  if (type === "error") return "error";
  return "idle";
}

document.addEventListener("click", event => {
  const button = event.target.closest("[data-session]");
  if (!button) return;
  watch.sessionKey = button.dataset.session;
  watch.events = [];
  renderSessions();
  loadRuns(watch.sessionKey).then(() => connect(watch.sessionKey));
});

document.getElementById("refresh-watch").onclick = refreshWatch;
refreshWatch().catch(err => {
  document.getElementById("watch-timeline").innerHTML = `<div class="card">Error: ${esc(err.message)}</div>`;
});
