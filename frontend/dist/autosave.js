// One serialized writer for all settings; drafts stay in CFG/DOM, never replaced by a response.
const settingsJobs = new Map();
let settingsRunning = null;
let settingsActiveJob = null;
let settingsConfirmed = null;
let sidebarVersion = null;
let sidebarServiceConfig = null;
let sidebarServiceState = "idle";
const settingsStatusTimers = new Map();
const outputSaveStates = new Map();
let settingsReady = false;
let closingSettings = false;
const settingsComposing = new Set();
const settingsFields = {
  "cfg-mineru-token": ["service", "mineru.token"], "cfg-mineru-mode": ["service", "mineru.model_version"],
  "cfg-llm-url": ["service", "llm.base_url"], "cfg-llm-key": ["service", "llm.api_key"],
  "cfg-llm-model": ["service", "llm.model"], "cfg-llm-conc": ["service", "llm.concurrency"],
  "cfg-bilingual": ["layout", "export.bilingual"], "cfg-toc": ["layout", "export.toc"],
  "cfg-twocolumn": ["layout", "export.two_column"], "cfg-skiprefs": ["layout", "translate.skip_references"],
  "cfg-repair": ["layout", "repair.spelling"], "cfg-repair-symbols": ["layout", "repair.symbols"],
  "cfg-outdir": ["output", "export.output_dir"],
};
const copySettings = (v) => JSON.parse(JSON.stringify(v));
const equalSettings = (a, b) => JSON.stringify(a) === JSON.stringify(b);
function mergeSettings(a, b) {
  const out = { ...a };
  for (const [k, v] of Object.entries(b)) out[k] = v && typeof v === "object" && !Array.isArray(v)
    ? mergeSettings(a?.[k] || {}, v) : v;
  return out;
}
// Compare submitted values with the last successful write, never the live editor CFG.
function changedSettings(patch, base) {
  const out = {};
  for (const [k, v] of Object.entries(patch)) {
    if (v && typeof v === "object" && !Array.isArray(v)) {
      const changed = changedSettings(v, base?.[k]);
      if (Object.keys(changed).length) out[k] = changed;
    } else if (!equalSettings(v, base?.[k])) out[k] = v;
  }
  return out;
}
function settingsFieldValue(id) {
  const el = $(id);
  return el.type === "checkbox" ? el.checked : id === "cfg-llm-conc" ? Number(el.value) : el.value;
}
function initSettingsBaseline() {
  sidebarServiceConfig = copySettings({mineru: CFG.mineru, llm: CFG.llm});
  settingsConfirmed = copySettings(CFG);
  settingsConfirmed.glossary_rows = copySettings(CFG.glossary);
  for (const [id, [, path]] of Object.entries(settingsFields)) {
    const [group, key] = path.split(".");
    settingsConfirmed[group][key] = settingsFieldValue(id);
  }
  const font = $("cfg-font").value;
  settingsConfirmed.export.font_preset = FONT_PRESETS[font] ? font : "custom";
  settingsConfirmed.export.user_font = FONT_PRESETS[font] ? "" : font;
}
function renderSidebarStatus() {
  if (sidebarVersion === null || !sidebarServiceConfig) return;
 if (settingsRecoveryBlocked) { $("sidebar-status").textContent = `${sidebarVersion} 配置待恢复`; return; }
  const c = sidebarServiceConfig;
  const missing = [];
  const text = value => String(value ?? "").trim();
  if (!text(c.mineru?.token)) missing.push("MinerU Token");
  if (!text(c.llm?.api_key)) missing.push("模型 API Key");
  if (!text(c.llm?.model)) missing.push("模型名");
  if (!text(c.llm?.base_url)) missing.push("模型 API 地址");
  else {
    try {
      const url = new URL(text(c.llm.base_url));
      if (!/^https?:\/\//i.test(text(c.llm.base_url)) || !["http:", "https:"].includes(url.protocol) || !url.hostname) missing.push("有效的 HTTP 或 HTTPS 模型 API 地址");
    } catch { missing.push("有效的 HTTP 或 HTTPS 模型 API 地址"); }
  }
  let label = missing.length ? "待配置" : "已配置";
  let color = missing.length ? "var(--warn)" : "var(--ok)";
  let detail = missing.length ? "请补全：" + missing.join("、") : "必要配置已保存；服务连接与实际任务是否可用仍需验证。";
  if (sidebarServiceState === "saving") {
    label = "保存中"; color = "var(--accent)"; detail = "服务配置正在保存，请稍候。";
  } else if (sidebarServiceState === "error") {
    label = "配置未保存"; color = "var(--bad)"; detail = "服务配置保存失败，请在设置页重试。";
  }
  const sb = $("sidebar-status");
  sb.textContent = `${sidebarVersion} `;
  sb.title = detail;
  sb.setAttribute("aria-label", `${sidebarVersion}，${label}。${detail}`);
  const dot = document.createElement("span");
  dot.className = "dot"; dot.textContent = "●"; dot.style.color = color;
  dot.setAttribute("aria-hidden", "true");
  sb.append(dot, document.createTextNode(" " + label));
}
function settingsState(section, state, message = "") {
  const card = section === "output-font" ? "output" : section;
  const el = $("save-" + card);
  if (!el) return;
  // A finished write must not confirm text still being composed in this card.
  const composing = [...settingsComposing].some(input => input.closest(".set-card[data-section]")?.dataset.section === card);
  if (["saved", "fading"].includes(state) && composing) state = "idle";
  if (section === "service" && state !== "fading") { sidebarServiceState = state; renderSidebarStatus(); }
  clearTimeout(settingsStatusTimers.get(section));
  settingsStatusTimers.delete(section);
  if (state === "saved") settingsStatusTimers.set(section, setTimeout(() => settingsState(section, "fading"), 1000));
  else if (state === "fading") settingsStatusTimers.set(section, setTimeout(() => settingsState(section, "idle"), 500));
  let states = [{section, state, message}];
  if (card === "output") {
    // Font and directory writes remain independent, but share one card-level status.
    outputSaveStates.set(section, states[0]);
    states = [...outputSaveStates.values()];
  }
  let display = ["error", "saving", "saved", "fading"].find(value => states.some(s => s.state === value)) || "idle";
  if (["saved", "fading"].includes(display) && composing) display = "idle";
  const failures = states.filter(s => s.state === "error");
  const errorText = failures.map(s => (card === "output" ? (s.section === "output-font" ? "字体：" : "归档目录：") : "") + s.message).join("；");
  el.className = "autosave-state " + display;
  const label = display === "error" ? "! 保存失败：" + errorText : display === "saving" ? "◌ 保存中…" : ["saved", "fading"].includes(display) ? "✓ 已保存" : "";
  if (el.textContent !== label) el.replaceChildren(document.createTextNode(label));
  for (const failure of failures) {
    const b = document.createElement("button"); b.className = "btn ghost-sm";
    b.disabled = settingsRecoveryBlocked;
    b.textContent = failures.length > 1 ? (failure.section === "output-font" ? "重试字体" : "重试归档目录") : "重试";
    b.addEventListener("click", () => { if (settingsRecoveryBlocked) return; const j = settingsJobs.get(failure.section); if (j) { j.error = null; j.ready = true; settingsState(failure.section, "saving"); drainSettings(); } });
    el.appendChild(b);
  }
}
function queueSettings(section, patch, delay = 0, run = null) {
  if (!settingsReady) return;
  const old = settingsJobs.get(section);
  // An in-flight value may still replace the confirmed value. Reverting it must be queued.
  const expected = mergeSettings(settingsConfirmed, settingsActiveJob?.patch || {});
  const changed = changedSettings(mergeSettings(old?.patch || {}, copySettings(patch)), expected);
  if (!Object.keys(changed).length) {
    if (old?.timer) clearTimeout(old.timer);
    if (old) {
      settingsJobs.delete(section);
      if (settingsActiveJob?.section !== section) settingsState(section, "idle");
    }
    return;
  }
  if (old && equalSettings(old.patch, changed)) {
    // Blur can flush a real pending edit, but cannot retry a failure or restart feedback.
    if (!delay && !old.error) {
      if (old.timer) clearTimeout(old.timer);
      old.timer = null; old.ready = true; drainSettings();
    }
    return;
  }
  if (old?.timer) clearTimeout(old.timer);
  const job = { section, patch: changed, run, ready: !delay, error: null };
  settingsJobs.set(section, job);
  settingsState(section, "saving");
  if (delay) job.timer = setTimeout(() => { job.timer = null; job.ready = true; drainSettings(); }, delay);
  else drainSettings();
}
function queueSettingsAction(section, run, patch = null) {
  if (!settingsReady) return;
  if (patch) { queueSettings(section, patch, 0, run); return; }
  settingsJobs.set(section, { section, run, ready: true, error: null });
  settingsState(section, "saving"); drainSettings();
}
function drainSettings() {
  if (settingsRunning) return settingsRunning;
  settingsRunning = (async () => {
    for (;;) {
      if (settingsRecoveryBlocked) break;
      const job = [...settingsJobs.values()].find(j => j.ready && !j.error);
      if (!job) break;
      settingsJobs.delete(job.section);
      if (job.patch) {
        job.patch = changedSettings(job.patch, settingsConfirmed);
        if (!Object.keys(job.patch).length) { settingsState(job.section, "idle"); continue; }
      }
      settingsActiveJob = job;
      try {
        const applied = job.run ? await job.run() : await App.UpdateSettings(job.patch);
        settingsConfirmed = mergeSettings(settingsConfirmed, job.patch || applied || {});
        // Only the committed service response drives the sidebar; never replace live editor data.
        if (job.section === "service") sidebarServiceConfig = copySettings({mineru: applied.mineru, llm: applied.llm});
        settingsState(job.section, settingsJobs.has(job.section) ? "saving" : "saved");
      } catch (e) {
        const newer = settingsJobs.get(job.section);
        if (newer?.timer) clearTimeout(newer.timer);
        if (newer?.run && !newer.patch) {
          // A newer import supersedes a failed selection; its resulting font is not known yet.
          job.patch = null; job.run = newer.run;
        } else if (newer) {
          job.patch = mergeSettings(job.patch || {}, newer.patch || {});
          if (newer.run) job.run = newer.run;
        }
        if (job.patch) {
          job.patch = changedSettings(job.patch, settingsConfirmed);
          if (!Object.keys(job.patch).length) {
            settingsJobs.delete(job.section); settingsState(job.section, "idle"); continue;
          }
        }
        job.error = String(e); job.ready = false; job.timer = null;
        settingsJobs.set(job.section, job);
        settingsState(job.section, "error", String(e));
        localLog("error", "设置保存失败（" + job.section + "）：" + e);
        await checkConfigRecovery();
      } finally { settingsActiveJob = null; }
    }
  })().finally(() => { settingsRunning = null; });
  return settingsRunning;
}
async function flushSettings(retry = false) {
  document.activeElement?.blur();
  for (;;) {
    if (settingsRecoveryBlocked && (settingsJobs.size || settingsActiveJob)) throw new Error("请先恢复配置，再重试保存未保存的修改");
    for (const job of settingsJobs.values()) {
      if (job.timer) clearTimeout(job.timer);
      job.timer = null;
      if (retry) job.error = null;
      if (!job.error) job.ready = true;
    }
    await drainSettings();
    // Drain edits made while the previous write was pending, including their debounce timers.
    if (![...settingsJobs.values()].some(j => !j.error)) break;
    retry = false;
  }
  if (settingsJobs.size) throw new Error("仍有设置未保存，请在设置页重试");
}
async function requireSettingsSaved() {
 if (settingsRecoveryBlocked) { await showModal({icon:"warn",title:"配置需要恢复",body:"请先在设置页重新读取或恢复配置，再启动任务。"}); return false; }
  let retry = false;
  for (;;) {
    try { await flushSettings(retry); return true; }
    catch {
      const choice = await showModal({ icon: "warn", title: "设置尚未保存", body: "请重试保存，或继续编辑并检查卡片中的错误提示。", buttons: [{ label: "重试" }, { label: "继续编辑", kind: "ghost" }] });
      if (choice !== 0) return false;
      retry = true;
    }
  }
}
async function closeWithSettings() {
  if (closingSettings) return;
  closingSettings = true;
  let retry = false;
  try {
    for (;;) {
      try { await flushSettings(retry); await App.FinishSettingsClose(); return; }
      catch {
        const choice = await showModal({ icon: "warn", title: "退出前保存失败", body: "输入仍保留在当前页面，可以重试或继续编辑。放弃并退出会丢失尚未保存的修改。", buttons: [{ label: "重试" }, { label: "继续编辑", kind: "ghost" }, { label: "放弃未保存修改并退出", kind: "danger" }] });
        if (choice === 2) { settingsJobs.clear(); await App.FinishSettingsClose(); return; }
        if (choice !== 0) return;
        retry = true;
      }
    }
  } finally { closingSettings = false; }
}
function bindAutosave() {
  document.addEventListener("compositionstart", (e) => {
    const section = e.target.closest(".set-card[data-section]")?.dataset.section;
    if (!section) return;
    settingsComposing.add(e.target);
    if ($("save-" + section)?.matches(".saved,.fading")) settingsState(section, "idle");
  });
  document.addEventListener("compositionend", (e) => settingsComposing.delete(e.target));
  for (const card of document.querySelectorAll(".set-card[data-section]")) {
    const section = card.dataset.section;
    const status = document.createElement("span"); status.id = "save-" + section; status.className = "autosave-state";
    status.setAttribute("role", "status"); status.setAttribute("aria-live", "polite");
    card.querySelector("h3").appendChild(status);
  }
  for (const [id, [section, path]] of Object.entries(settingsFields)) {
    const el = $(id);
    const text = el.tagName === "INPUT" && el.type !== "checkbox";
    const update = (immediate = false) => {
      if (!settingsReady || settingsComposing.has(el)) return;
      const value = settingsFieldValue(id);
      const [group, key] = path.split(".");
      CFG[group][key] = value;
      queueSettings(section, { [group]: { [key]: value } }, text && !immediate ? 600 : 0);
    };
    if (text) {
      el.addEventListener("compositionstart", () => settingsComposing.add(el));
      el.addEventListener("compositionend", () => { settingsComposing.delete(el); update(); });
      el.addEventListener("input", () => update());
      el.addEventListener("blur", () => update(true));
    } else el.addEventListener("change", () => update(true));
  }
  $("g-add").addEventListener("click", () => { CFG.glossary.push({en:"",zh:""}); renderGlossary(); saveGlossary(); });
  rt.EventsOn("settings:before-close", closeWithSettings);
}
function saveGlossary(delay = 0) { queueSettings("glossary", {glossary_rows: CFG.glossary}, delay); }
function saveRulesDraft(delay = 0) { queueSettings("rules", {rules: CFG.rules}, delay); }
function showSettingsUndo(section, restore) {
  const id = "undo-" + section;
  $(id)?.remove();
  const box = document.createElement("div"); box.id = id; box.className = "settings-undo";
  box.appendChild(document.createTextNode("已删除 · "));
  const button = document.createElement("button"); button.className = "btn ghost-sm"; button.textContent = "撤销";
  button.addEventListener("click", () => { if (restore() === false) return; clearTimeout(timer); box.remove(); });
  box.appendChild(button); document.querySelector(`.set-card[data-section="${section}"]`).appendChild(box);
  const timer = setTimeout(() => box.remove(), 10000);
}

let settingsRecoveryBlocked = false;
async function checkConfigRecovery() {
  const state = await App.GetConfigLoadState();
  settingsRecoveryBlocked = !state.ready;
  const panel = $("config-recovery");
  panel.hidden = state.ready;
  $("config-recovery-message").textContent = state.message || "";
  for (const el of document.querySelectorAll('.set-card[data-section] input,.set-card[data-section] textarea,.set-card[data-section] select,.set-card[data-section] button')) el.disabled = settingsRecoveryBlocked;
  panel.querySelector('[data-recover="restore_backup"]').disabled = !state.has_backup;
  if (settingsRecoveryBlocked) {
    settingsReady = false;
    for (const job of settingsJobs.values()) {
      clearTimeout(job.timer); job.timer = null; job.ready = false;
      job.error ||= "配置需要恢复，输入已保留";
      settingsState(job.section, "error", job.error);
    }
    $("sidebar-status").textContent = "配置待恢复";
  }
  for (const button of panel.querySelectorAll("[data-recover]")) {
    if (button.dataset.bound) continue;
    button.dataset.bound = "1";
    button.addEventListener("click", async () => {
      const action = button.dataset.recover;
      if (action === "reset") {
        const choice = await showModal({icon:"warn",title:"备份并重建配置",body:"将先保留当前配置文件，再建立默认配置。已有有效备份仍会保留。",buttons:[{label:"备份并重建",kind:"danger"},{label:"取消",kind:"ghost"}]});
        if (choice !== 0) return;
      }
      button.disabled = true;
      let recoveryError = "";
      try {
        await settingsRunning;
        await App.RecoverConfig(action);
        await loadSettings(); initSettingsBaseline();
        // Rebase only uncommitted leaves onto the recovered configuration.
        let pending = {};
        for (const job of settingsJobs.values()) pending = mergeSettings(pending, job.patch || {});
        await loadSettings(mergeSettings(CFG, pending));
        await checkConfigRecovery(); settingsReady = !settingsRecoveryBlocked;
        for (const [section, job] of settingsJobs) {
          if (job.patch) job.patch = changedSettings(job.patch, settingsConfirmed);
          if (job.patch && !Object.keys(job.patch).length && !job.run) {
            settingsJobs.delete(section); settingsState(section, "idle"); continue;
          }
          job.error = "配置已恢复，请重试保存未保存的修改"; job.ready = false;
          settingsState(section, "error", job.error);
        }
        renderSidebarStatus();
      } catch (e) {
        recoveryError = "恢复失败：" + String(e);
      } finally {
        await checkConfigRecovery();
        if (recoveryError) $("config-recovery-message").textContent = recoveryError;
      }
    });
  }
}
