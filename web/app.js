"use strict";

const STEPS = ["destinations", "source", "details", "transfer"];

const state = {
  step: "destinations",
  shares: [],
  selected: new Set(),
  mounts: [],
  mount: "",
  scan: null,
  name: "",
  transferring: false,
  finished: false,
};

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

// ---------- API ----------
async function api(path, opts) {
  const res = await fetch(path, opts);
  let body = null;
  try { body = await res.json(); } catch (_) {}
  if (!res.ok) {
    const msg = (body && body.error) || `request failed (${res.status})`;
    throw new Error(msg);
  }
  return body;
}

function fmtBytes(n) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(n) / Math.log(1024));
  return `${(n / Math.pow(1024, i)).toFixed(i ? 1 : 0)} ${u[i]}`;
}

function toast(msg, kind = "err") {
  const t = $("#toast");
  t.textContent = msg;
  t.className = `toast ${kind}`;
  t.hidden = false;
  clearTimeout(toast._t);
  toast._t = setTimeout(() => { t.hidden = true; }, 4200);
}

// ---------- Rendering ----------
function render() {
  // panels
  $$(".panel").forEach((p) => { p.hidden = p.dataset.panel !== state.step; });
  // stepper
  const idx = STEPS.indexOf(state.step);
  $$("#stepper li").forEach((li) => {
    const i = STEPS.indexOf(li.dataset.step);
    li.classList.toggle("active", i === idx);
    li.classList.toggle("done", i < idx);
  });
  renderSummary();
  renderNav();
}

function renderSummary() {
  const dest = $("#sum-dest");
  const sel = state.shares.filter((s) => state.selected.has(s.key));
  if (sel.length === 0) { dest.textContent = "none selected"; }
  else { dest.innerHTML = sel.map((s) => `${s.share} <span class="mono">@${s.host}</span>`).join("<br>"); }

  $("#sum-source").textContent = state.mount || "not set";

  const scanEl = $("#sum-scan");
  if (state.scan) scanEl.textContent = `${state.scan.fileCount} files · ${fmtBytes(state.scan.totalBytes)}`;
  else scanEl.textContent = "—";

  $("#sum-folder").textContent = state.name ? `${new Date().getFullYear()} - ${state.name}` : "—";
}

function renderNav() {
  const back = $("#nav-back");
  const next = $("#nav-next");
  const idx = STEPS.indexOf(state.step);
  back.hidden = idx === 0 || state.step === "transfer";

  if (state.step === "transfer") {
    if (state.finished) { next.hidden = false; next.textContent = "Start another →"; next.disabled = false; }
    else { next.hidden = false; next.textContent = "Cancel transfer"; next.disabled = false; next.classList.add("btn-cancel"); }
    return;
  }
  next.classList.remove("btn-cancel");
  next.hidden = false;
  next.disabled = false;
  next.textContent = state.step === "details" ? "Start transfer →" : "Continue →";
}

// ---------- Destinations ----------
function renderShares() {
  const wrap = $("#share-list");
  if (state.shares.length === 0) {
    wrap.innerHTML = `<div class="empty-note">No saved shares yet. Add one below.</div>`;
    return;
  }
  wrap.innerHTML = "";
  state.shares.forEach((s) => {
    const card = document.createElement("button");
    card.type = "button";
    card.className = "select-card" + (state.selected.has(s.key) ? " selected" : "");
    card.innerHTML = `
      <span class="check"></span>
      <span class="card-body">
        <span class="card-title">${esc(s.share)}</span>
        <span class="card-sub">${esc(s.host)}:${s.port}${s.basePath ? "/" + esc(s.basePath) : ""} · ${esc(s.username)}</span>
      </span>
      <span class="card-del" title="Remove">×</span>`;
    card.addEventListener("click", (e) => {
      if (e.target.classList.contains("card-del")) { deleteShare(s); return; }
      if (state.selected.has(s.key)) state.selected.delete(s.key);
      else state.selected.add(s.key);
      renderShares(); renderSummary();
    });
    wrap.appendChild(card);
  });
}

async function deleteShare(s) {
  if (!confirm(`Remove ${s.share} @ ${s.host}? (does not touch any files)`)) return;
  try {
    await api("/api/shares/delete", { method: "POST", headers: jsonH(), body: JSON.stringify({ key: s.key }) });
    state.shares = state.shares.filter((x) => x.key !== s.key);
    state.selected.delete(s.key);
    renderShares(); renderSummary();
  } catch (e) { toast(e.message); }
}

function readShareForm() {
  const f = $("#add-share-form");
  const g = (n) => f.elements[n].value.trim();
  return {
    host: g("host"),
    port: parseInt(g("port") || "445", 10) || 445,
    share: g("share"),
    base_path: g("basePath"),
    username: g("username"),
    password: f.elements["password"].value,
  };
}

async function testShare() {
  const body = readShareForm();
  if (!body.host || !body.share || !body.username) { setAddStatus("host, share and username are required", "err"); return; }
  setAddStatus("testing…", "");
  try {
    const r = await api("/api/shares/test", { method: "POST", headers: jsonH(), body: JSON.stringify(body) });
    if (r.ok) setAddStatus("connection works ✓", "ok");
    else setAddStatus(r.error, "err");
  } catch (e) { setAddStatus(e.message, "err"); }
}

async function saveShare(ev) {
  ev.preventDefault();
  const body = readShareForm();
  if (!body.host || !body.share || !body.username) { setAddStatus("host, share and username are required", "err"); return; }
  setAddStatus("testing & saving…", "");
  try {
    const saved = await api("/api/shares", { method: "POST", headers: jsonH(), body: JSON.stringify(body) });
    state.shares = upsert(state.shares, saved);
    state.selected.add(saved.key);
    $("#add-share-form").reset();
    $("#add-share-form").hidden = true;
    setAddStatus("", "");
    renderShares(); renderSummary();
    toast("Share saved", "ok");
  } catch (e) { setAddStatus(e.message, "err"); }
}

function setAddStatus(msg, kind) {
  const el = $("#add-share-status");
  el.textContent = msg;
  el.className = "inline-status " + (kind || "");
}

// ---------- Source ----------
function renderMounts() {
  const wrap = $("#mount-list");
  if (state.mounts.length === 0) {
    wrap.innerHTML = `<div class="empty-note">No volumes detected. Enter a path manually.</div>`;
    return;
  }
  wrap.innerHTML = "";
  state.mounts.forEach((m) => {
    const card = document.createElement("button");
    card.type = "button";
    card.className = "select-card radio" + (state.mount === m.path ? " selected" : "");
    const meta = m.fsType ? `${m.fsType}${m.source ? " · " + m.source : ""}` : "volume";
    card.innerHTML = `
      <span class="check"></span>
      <span class="card-body">
        <span class="card-title mono">${esc(m.path)}</span>
        <span class="card-sub">${esc(meta)}</span>
      </span>`;
    card.addEventListener("click", () => selectMount(m.path));
    wrap.appendChild(card);
  });
}

async function selectMount(path) {
  state.mount = path;
  $("#manual-mount-form").elements["mount"].value = "";
  renderMounts(); renderSummary();
  await runScan();
}

async function runScan() {
  const res = $("#scan-result");
  res.hidden = false;
  res.innerHTML = `
    <div class="scanning">
      <div class="scan-spinner"></div>
      <div class="scan-msg">
        <strong>Scanning card…</strong>
        <span class="mono">${esc(state.mount)}</span>
      </div>
    </div>
    <div class="indeterminate"><span></span></div>`;
  try {
    const s = await api("/api/scan", { method: "POST", headers: jsonH(), body: JSON.stringify({ mount: state.mount }) });
    state.scan = s;
    renderScan(s);
    renderSummary(); renderNav();
  } catch (e) {
    state.scan = null;
    res.innerHTML = `<p class="stat-line" style="color:var(--danger)">${esc(e.message)}</p>`;
    renderSummary();
  }
}

function renderScan(s) {
  const res = $("#scan-result");
  if (s.fileCount === 0) {
    res.innerHTML = `<p class="stat-line">No supported media found on this card.</p>`;
    return;
  }
  const dates = Object.keys(s.byDate || {}).sort();
  const maxN = Math.max(...dates.map((d) => s.byDate[d]), 1);
  const bars = dates.map((d) => `
    <div class="date-bar">
      <span class="d">${d}</span>
      <span class="track"><span class="bar" style="width:${(s.byDate[d] / maxN * 100).toFixed(0)}%"></span></span>
      <span class="n">${s.byDate[d]}</span>
    </div>`).join("");
  res.innerHTML = `
    <div class="scan-stats">
      <div class="scan-stat"><div class="num">${s.fileCount}</div><div class="lbl">files</div></div>
      <div class="scan-stat"><div class="num">${fmtBytes(s.totalBytes)}</div><div class="lbl">total</div></div>
      <div class="scan-stat"><div class="num">${dates.length}</div><div class="lbl">${dates.length === 1 ? "day" : "days"}</div></div>
    </div>
    <div class="date-bars">${bars}</div>
    <p class="scan-note">Grouping is approximate (by file date); the transfer files by precise EXIF capture date.</p>`;
}

// ---------- Navigation ----------
function next() {
  if (state.step === "transfer") {
    if (state.finished) return resetFlow();
    return cancelTransfer();
  }
  if (state.step === "destinations") {
    if (state.selected.size === 0) return toast("Select at least one destination");
    goto("source");
    if (!state.mount && state.mounts.length === 0) {} // user types manually
  } else if (state.step === "source") {
    if (!state.mount) return toast("Choose a source card or enter a path");
    if (!state.scan) return toast("Still scanning… one sec");
    if (state.scan.fileCount === 0) return toast("No media to transfer on this card");
    goto("details");
    updateFolderPreview();
  } else if (state.step === "details") {
    const name = $("#details-form").elements["name"].value.trim();
    if (!name) return toast("Name the shoot first");
    state.name = name;
    startTransfer();
  }
}

function back() {
  const idx = STEPS.indexOf(state.step);
  if (idx > 0) goto(STEPS[idx - 1]);
}

function goto(step) { state.step = step; render(); }

function updateFolderPreview() {
  const name = $("#details-form").elements["name"].value.trim();
  $("#folder-preview").textContent = name ? `${new Date().getFullYear()} - ${name}` : "—";
  state.name = name;
  renderSummary();
}

// ---------- Transfer ----------
let evtSource = null;

async function startTransfer() {
  goto("transfer");
  state.transferring = true;
  state.finished = false;
  setProgress(0, 0, "");
  $("#summary-block").hidden = true;
  $("#transfer-title").textContent = "Transferring…";
  $("#transfer-sub").textContent = "Streaming straight from card to NAS.";
  renderNav();

  try {
    await api("/api/transfer", {
      method: "POST", headers: jsonH(),
      body: JSON.stringify({ mount: state.mount, name: state.name, shareKeys: Array.from(state.selected) }),
    });
  } catch (e) {
    state.transferring = false;
    toast(e.message);
    goto("details");
    return;
  }
  openEvents();
}

function openEvents() {
  if (evtSource) evtSource.close();
  evtSource = new EventSource("/api/transfer/events");
  evtSource.onmessage = (e) => {
    let ev; try { ev = JSON.parse(e.data); } catch (_) { return; }
    if (ev.type === "progress" || ev.type === "started") {
      if (ev.total) setProgress(ev.completed || 0, ev.total, ev.file || "");
      else $("#current-file").textContent = ev.file || "preparing…";
    } else if (ev.type === "finished") {
      finishTransfer(ev);
      evtSource.close();
    }
  };
  evtSource.onerror = () => { /* server closes the stream on completion; ignore */ };
}

function setProgress(done, total, file) {
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  $("#progress-fill").style.width = pct + "%";
  $("#progress-count").textContent = `${done} / ${total}`;
  $("#progress-pct").textContent = pct + "%";
  if (file) $("#current-file").textContent = file;
}

function finishTransfer(ev) {
  state.transferring = false;
  state.finished = true;
  if (ev.total) setProgress(ev.completed, ev.total, "");
  const ok = !ev.fatalError && (!ev.errors || ev.errors.length === 0);
  const block = $("#summary-block");
  block.hidden = false;
  block.className = "summary-block " + (ok ? "ok" : "err");
  const secs = ev.durationMs ? (ev.durationMs / 1000).toFixed(1) : "0";
  let html = `<h3 class="${ok ? "verdict-ok" : "verdict-err"}">${ok ? "Transfer complete" : "Completed with issues"}</h3>`;
  html += `<p class="stat-line">${ev.completed} of ${ev.total} files · ${secs}s</p>`;
  if (ev.fatalError) html += `<p class="stat-line" style="color:var(--danger)">${esc(ev.fatalError)}</p>`;
  if (ev.errors && ev.errors.length) {
    html += `<div class="error-list">` +
      ev.errors.slice(0, 8).map((e) => `<span class="e">${esc(e.file)} → ${esc(e.share)}: ${esc(e.error)}</span>`).join("") +
      (ev.errors.length > 8 ? `<span class="e">…and ${ev.errors.length - 8} more</span>` : "") +
      `</div>`;
  }
  block.innerHTML = html;
  $("#transfer-title").textContent = ok ? "Done" : "Finished with errors";
  $("#transfer-sub").textContent = ok ? "Your photos are safely on the NAS." : "Some files need another look.";
  renderNav();
}

async function cancelTransfer() {
  try { await api("/api/transfer/cancel", { method: "POST" }); } catch (_) {}
}

function resetFlow() {
  state.mount = ""; state.scan = null; state.name = "";
  state.finished = false; state.transferring = false;
  $("#scan-result").hidden = true;
  $("#details-form").reset();
  refreshMounts();
  goto("destinations");
}

// ---------- helpers ----------
function jsonH() { return { "Content-Type": "application/json" }; }
function esc(s) { return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])); }
function upsert(list, item) {
  const i = list.findIndex((x) => x.key === item.key);
  if (i >= 0) { const c = list.slice(); c[i] = item; return c; }
  return [...list, item];
}

async function refreshMounts() {
  try { state.mounts = await api("/api/mounts"); } catch (_) { state.mounts = []; }
  renderMounts();
}

// ---------- init ----------
async function init() {
  $("#year-hint").textContent = `${new Date().getFullYear()} - <name>`;

  try { state.shares = await api("/api/shares"); } catch (e) { toast("Couldn't load shares: " + e.message); }
  renderShares();
  await refreshMounts();

  $("#nav-next").addEventListener("click", next);
  $("#nav-back").addEventListener("click", back);
  $("#show-add-share").addEventListener("click", () => {
    const f = $("#add-share-form"); f.hidden = !f.hidden;
    if (!f.hidden) f.elements["host"].focus();
  });
  $("#cancel-add-share").addEventListener("click", () => { $("#add-share-form").hidden = true; setAddStatus("", ""); });
  $("#test-share").addEventListener("click", testShare);
  $("#add-share-form").addEventListener("submit", saveShare);

  $("#manual-mount-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const v = e.target.elements["mount"].value.trim();
    if (!v) return;
    state.mount = v;
    renderMounts(); renderSummary(); runScan();
  });

  $("#browse-btn").addEventListener("click", async () => {
    const btn = $("#browse-btn");
    const prev = btn.textContent;
    btn.textContent = "Opening…"; btn.disabled = true;
    try {
      const r = await api("/api/browse", { method: "POST" });
      if (r && r.path) {
        state.mount = r.path;
        $("#manual-mount-form").elements["mount"].value = r.path;
        renderMounts(); renderSummary(); runScan();
      }
    } catch (e) { toast(e.message); }
    finally { btn.textContent = prev; btn.disabled = false; }
  });

  $("#details-form").addEventListener("input", updateFolderPreview);
  $("#details-form").addEventListener("submit", (e) => { e.preventDefault(); next(); });

  setupSettings();
  render();
}

// ---------- Settings (ntfy) ----------
function setupSettings() {
  const modal = $("#settings-modal");
  const form = $("#ntfy-form");
  const status = $("#ntfy-status");

  const open = async () => {
    setNtfyStatus("", "");
    try {
      const s = await api("/api/settings");
      const n = s.ntfy || {};
      form.elements["server"].value = n.server || "";
      form.elements["topic"].value = n.topic || "";
      form.elements["username"].value = n.username || "";
      form.elements["token"].value = "";
      form.elements["password"].value = "";
      const savedHint = "•••••• (saved — leave blank to keep)";
      form.elements["token"].placeholder = n.hasToken ? savedHint : "tk_…";
      form.elements["password"].placeholder = n.hasPassword ? savedHint : "";
    } catch (_) {}
    modal.hidden = false;
  };
  const close = () => { modal.hidden = true; };

  $("#settings-btn").addEventListener("click", open);
  modal.querySelectorAll("[data-close]").forEach((el) => el.addEventListener("click", close));
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && !modal.hidden) close(); });

  $("#ntfy-test").addEventListener("click", async () => {
    const body = ntfyBody();
    if (!body.server || !body.topic) { setNtfyStatus("server and topic required", "err"); return; }
    setNtfyStatus("sending…", "");
    try {
      const r = await api("/api/settings/ntfy/test", { method: "POST", headers: jsonH(), body: JSON.stringify(body) });
      if (r.ok) setNtfyStatus("test sent ✓", "ok");
      else setNtfyStatus(r.error || "failed", "err");
    } catch (e) { setNtfyStatus(e.message, "err"); }
  });

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    setNtfyStatus("saving…", "");
    try {
      await api("/api/settings", { method: "POST", headers: jsonH(), body: JSON.stringify(ntfyBody()) });
      setNtfyStatus("saved ✓", "ok");
      toast("Notification settings saved", "ok");
      setTimeout(close, 600);
    } catch (e) { setNtfyStatus(e.message, "err"); }
  });

  function ntfyBody() {
    return {
      server: form.elements["server"].value.trim(),
      topic: form.elements["topic"].value.trim(),
      token: form.elements["token"].value.trim(),
      username: form.elements["username"].value.trim(),
      password: form.elements["password"].value,
    };
  }
  function setNtfyStatus(msg, kind) { status.textContent = msg; status.className = "inline-status " + (kind || ""); }
}

document.addEventListener("DOMContentLoaded", init);
