// main.js — Wails 绑定胶水：任务队列、设置表单、日志流、主题与模式开关
const App = window.go.app.App;
const rt = window.runtime;

let CFG = null;
let logFilter = "all";
let logLines = [];
const logPending = []; // log:line 事件到达太快时先排队

// ------------------------------------------------ 工具

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
const escAttr = (s) => esc(s).replace(/"/g, "&quot;"); // 用于 value="..." 属性位
const fileCode = (path) => {
  const base = path.split(/[\\/]/).pop().replace(/\.(md|pdf)$/i, "");
  const m = base.match(/^([A-Za-z]+\d+)/);
  return m ? m[1] : base.slice(0, 12);
};

// ------------------------------------------------ 视图切换

function bindNav() {
  document.querySelectorAll(".nav-item").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll(".nav-item").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      document.querySelectorAll(".view").forEach((v) => v.classList.remove("active"));
      $("view-" + btn.dataset.view).classList.add("active");
      if (btn.dataset.view === "logs") renderLog();
    });
  });
}

function bindTitlebar() {
  const maxBtn = $("win-max");
  // Windows 惯例：最大化后按钮切「向下还原」❐ 图标并同步提示文案
  const setMaxIcon = (maxed) => {
    maxBtn.classList.toggle("restore", maxed);
    maxBtn.title = maxed ? "向下还原" : "最大化";
    maxBtn.setAttribute("aria-label", maxBtn.title);
  };
  const syncMaxState = async () => {
    try {
      setMaxIcon(await rt.WindowIsMaximised());
    } catch (e) {
      /* 壳缺失时保持默认 □ */
    }
  };
  const toggleMax = async () => {
    try {
      await rt.WindowToggleMaximise();
      await syncMaxState();
    } catch (e) {
      /* 忽略 */
    }
  };
  $("win-min").addEventListener("click", () => rt.WindowMinimise());
  maxBtn.addEventListener("click", toggleMax);
  $("win-close").addEventListener("click", () => rt.Quit());
  // 双击标题栏最大化/还原（Windows 惯例）；右侧按钮区已是 no-drag，排除之
  $("titlebar").addEventListener("dblclick", (e) => {
    if (e.target.closest(".win-controls") || e.target.closest(".theme-btn")) return;
    toggleMax();
  });
  // 系统 Snap / Win+↑ 等非按钮触发也同步图标（防抖）
  let rzTimer = 0;
  window.addEventListener("resize", () => {
    clearTimeout(rzTimer);
    rzTimer = setTimeout(syncMaxState, 120);
  });
  syncMaxState();
}

// ------------------------------------------------ 任务队列

// 三种任务形态的步进器阶段表（按 kind 取用）
const STAGES_BY_KIND = {
  pdf: ["解析", "翻译", "排版", "完成"],
  md: ["翻译", "排版", "完成"],
  export: ["排版", "完成"],
};
const STAGE_IDX_BY_KIND = {
  pdf: { "解析中": 0, "翻译中": 1, "排版中": 2, "完成": 3 },
  md: { "翻译中": 0, "排版中": 1, "完成": 2 },
  export: { "排版中": 0, "完成": 1 },
};
const stagesOf = (t) => STAGES_BY_KIND[t.kind] || STAGES_BY_KIND.md;
const stageIdxOf = (t) => STAGE_IDX_BY_KIND[t.kind] || STAGE_IDX_BY_KIND.md;
// 记录每张卡上次渲染到的阶段数：新出现的阶段带入场动画
const lastStageCount = {};
// 只在任务首次出现时播放入场动画（进度刷新不重播）
const cardSeen = {};

function visibleStages(t) {
  // 渐进显示：未到的阶段不存在。
  // 等待→只有第一枚；进行→当前+1；完成→全部；失败→停在失败那一步。
  const total = stagesOf(t).length;
  if (t.status === "等待") return 1;
  if (t.status === "完成") return total;
  const idx = stageIdxOf(t);
  const cur = idx[t.status] ?? idx[t.stage];
  if (t.status === "失败") return (cur === undefined ? 0 : cur) + 1;
  return cur === undefined ? 1 : Math.min(cur + 1, total);
}

function taskCard(t) {
  const code = fileCode(t.source_path);
  const fileName = t.source_path.split(/[\\/]/).pop();
  const chipClass = { "解析中": "run", "翻译中": "run", "排版中": "run", "完成": "ok", "失败": "bad" }[t.status] || "";

  // 步进器：只渲染已到达的阶段，成功一步亮一步
  const vis = visibleStages(t);
  const cur = stageIdxOf(t)[t.status];
  const prevVis = lastStageCount[t.id] ?? 1;
  let stages = "";
  for (let i = 0; i < vis; i++) {
    const name = stagesOf(t)[i];
    let st, dot = "";
    if (t.status === "完成") { st = "done"; dot = "✓"; }
    else if (t.status === "失败") {
      st = i === vis - 1 ? "fail" : "done";
      dot = st === "fail" ? "✕" : "✓";
    } else if (t.status === "等待") { st = ""; } // 未开始：静态灰点，不脉冲
    else if (i < cur) { st = "done"; dot = "✓"; }
    else { st = "cur"; }
    const isNew = vis > prevVis && i === vis - 1;
    stages += `<span class="stp ${st}${isNew ? " new" : ""}"><span class="dot">${dot}</span>${name}</span>`;
    if (i < vis - 1) stages += `<span class="stp-line"></span>`;
  }
  lastStageCount[t.id] = vis;

  let progressHtml = "";
  if (t.status === "翻译中" && t.total > 0) {
    const segs = Math.min(t.total, 30);
    const filled = Math.round((t.done / t.total) * segs);
    let bars = "";
    for (let i = 0; i < segs; i++) {
      bars += `<i class="${i < filled ? "f" : i === filled ? "cur" : ""}"></i>`;
    }
    progressHtml = `
      <div class="segbar" aria-label="翻译进度">${bars}</div>
      <div class="seg-cap"><span>第 ${t.done} / ${t.total} 段</span><span class="eta">失败 ${t.failed}</span></div>`;
  }

  // 状态相关的 meta 文案（信息，不做装饰）；kind 标注实际走的路径
  const kindLabel = {
    pdf: "PDF 原文 · 完整流程",
    md: "英文 MD · 翻译+排版",
    export: "中文 MD · 仅排版",
  }[t.kind] || "英文 MD · 翻译+排版";
  let meta;
  if (t.status === "完成") {
    meta = t.kind === "export" ? "排版完成" : `${t.done} 段译文` + (t.refs_kept > 0 ? ` · 参考文献 ${t.refs_kept} 条保留原文` : "");
  } else if (t.status === "失败") {
    meta = `${kindLabel} · 卡在「${stagesOf(t)[vis - 1]}」这一步`;
  } else {
    meta = `${kindLabel} · ${t.stage || t.status}`;
  }

  let sideActions = "";
  if (t.status === "完成") {
    sideActions = `
      <div class="btn-rows">
        <button class="btn primary" data-open="${escAttr(t.out_pdf)}">中文 PDF</button>
        <button class="btn" data-open="${escAttr(t.work_dir)}">打开文件夹</button>
        <button class="btn" data-retry="${t.id}">重跑</button>
        <button class="btn" data-remove="${t.id}" title="从队列移除这张卡片">移除</button>
      </div>`;
  } else if (t.status === "失败") {
    sideActions = `
      <div class="btn-rows">
        <button class="btn primary" data-retry="${t.id}">重试</button>
        <button class="btn" data-open="${escAttr(t.work_dir || t.source_path)}">打开文件夹</button>
        <button class="btn" data-remove="${t.id}" title="从队列移除这张卡片">移除</button>
      </div>`;
  } else if (t.status === "等待") {
    sideActions = `<div class="btn-rows">
      <button class="btn primary" data-startone="${t.id}" title="只跑这一篇，跑完即暂停">开始</button>
      <button class="btn" data-remove="${t.id}">取消排队</button>
    </div>`;
  } else {
    sideActions = `<div class="btn-rows"><button class="btn" data-retry="${t.id}">重新排队</button></div>`;
  }
  const failNote = t.status === "失败" && t.err
    ? `<div class="fail-note">${esc(t.err.slice(0, 160))}</div>` : "";
  const enterCls = cardSeen[t.id] ? "" : " enter";
  cardSeen[t.id] = true;
  return `
  <div class="task${enterCls}" id="task-${t.id}">
    <div class="codetile${t.kind !== "pdf" ? " md" : ""}" title="${esc(t.source_path)}">
      <div class="codetile-code">${esc(code)}</div>
      <div class="codetile-kind">${t.kind === "pdf" ? "文献原文" : t.kind === "export" ? "中文稿" : "已解析稿"}</div>
    </div>
    <div class="task-body">
      <div class="task-name" title="${esc(t.source_path)}">${esc(fileName)}</div>
      <div class="task-meta">${esc(meta)}</div>
      <div class="stepper">${stages}</div>
      ${progressHtml}
      ${failNote}
    </div>
    <div class="task-side">
      <span class="status-chip ${chipClass}">${esc(t.status)}</span>
      ${sideActions}
    </div>
  </div>`;
}

let QUEUE_STATE = { running: false, paused: true, pending: 0 };

function renderQueue() {
  Promise.all([App.QueueList(), App.QueueState()]).then(([tasks, st]) => {
    QUEUE_STATE = st;
    const list = $("queue-list");
    list.innerHTML = tasks.map(taskCard).join("");
    $("queue-count").textContent = tasks.length + " 个任务";
    $("nav-task-count").textContent = String(tasks.filter((t) => t.status !== "完成" && t.status !== "失败").length);
    $("queue-empty").style.display = tasks.length ? "none" : "block";
    // 开始/暂停开关：跑着 → 暂停；停着且有等待任务 → 开始；否则隐藏
    const toggle = $("btn-queue-toggle");
    if (st.running) {
      toggle.style.display = "";
      toggle.textContent = "暂停";
      toggle.className = "btn ghost-sm";
      toggle.title = st.paused ? "已暂停：当前任务完成后停止" : "当前任务完成后不再开始下一篇";
    } else if (st.pending > 0) {
      toggle.style.display = "";
      toggle.textContent = "开始";
      toggle.className = "btn primary";
      toggle.title = "按顺序处理队列中的等待任务";
    } else {
      toggle.style.display = "none";
    }
    bindCardButtons();
  }).catch((e) => console.error("QueueList", e));
}

function upsertTask(t) {
  renderQueue();
}

function bindCardButtons() {
  document.querySelectorAll("[data-open]").forEach((b) => {
    if (b.dataset.bound) return;
    b.dataset.bound = "1";
    b.addEventListener("click", () => App.OpenPath(b.dataset.open));
  });
  document.querySelectorAll("[data-retry]").forEach((b) => {
    if (b.dataset.bound) return;
    b.dataset.bound = "1";
    b.addEventListener("click", () => App.QueueRetry(b.dataset.retry).then(() => renderQueue()));
  });
  document.querySelectorAll("[data-remove]").forEach((b) => {
    if (b.dataset.bound) return;
    b.dataset.bound = "1";
    b.addEventListener("click", () => App.QueueRemove(b.dataset.remove).then(() => renderQueue()));
  });
  document.querySelectorAll("[data-startone]").forEach((b) => {
    if (b.dataset.bound) return;
    b.dataset.bound = "1";
    b.addEventListener("click", async () => {
      try {
        await App.QueueStartOne(b.dataset.startone);
        renderQueue();
      } catch (e) {
        await showModal({ icon: "warn", title: "开始失败", body: esc(String(e)) });
      }
    });
  });
}

function bindDrop() {
  const dz = $("dropzone");
  dz.addEventListener("click", pickFiles);
  dz.addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pickFiles(); }
  });
  rt.OnFileDrop((x, y, paths) => addPaths(paths));
}

function localLog(level, msg) {
  queueLog({ level, time: new Date().toLocaleTimeString(), msg });
}

async function pickFiles() {
  try {
    const sel = await App.PickFiles();
    if (sel && sel.length) addPaths(sel);
  } catch (e) {
    localLog("error", "打开文件选择框失败：" + e);
  }
}

// 入队模式（会话级，不落盘）：auto 自动路由 | translate 强制翻译 | export 仅排版
let MODE = "auto";

async function addPaths(paths) {
  const pdfs = [], mds = [];
  for (const p of paths || []) {
    if (/\.pdf$/i.test(p)) pdfs.push(p);
    else if (/\.md$/i.test(p)) mds.push(p); // 中文 md（含 _zh.md）由后端按语言/模式路由
  }
  const jobs = [];
  if (mds.length) jobs.push(App.QueueAddMD(mds, MODE));
  if (pdfs.length) {
    if (MODE === "export") {
      localLog("warn", `导出模式仅支持 MD：已跳过 ${pdfs.length} 个 PDF`);
      await showModal({
        icon: "warn",
        title: "导出模式仅支持 MD 文件",
        body: `已跳过 <b>${pdfs.length} 个 PDF</b>。PDF 请用「自动」模式处理。`,
      });
    } else {
      jobs.push(App.QueueAddPDF(pdfs, MODE));
    }
  }
  Promise.all(jobs).then(() => renderQueue()).catch(async (e) => {
    localLog("error", "任务入队失败：" + e);
    await showModal({ icon: "warn", title: "任务入队失败", body: esc(String(e)) });
    renderQueue(); // 部分文件可能已入队，失败路径同样要刷新
  });
}

function positionModePill() {
  const pill = $("mode-pill");
  const active = document.querySelector(".mode-btn.active");
  if (!pill || !active) return;
  pill.style.width = active.offsetWidth + "px";
  pill.style.transform = `translateX(${active.offsetLeft - 3}px)`;
}

function bindQueueButtons() {
  const clear = $("btn-clear-finished");
  if (!clear.dataset.bound) {
    clear.dataset.bound = "1";
    clear.addEventListener("click", async () => {
      try { await App.QueueClearFinished(); renderQueue(); }
      catch (e) { localLog("error", "清理失败：" + e); }
    });
  }
}

function bindModeSwitch() {
  const box = $("mode-switch");
  box.addEventListener("click", (e) => {
    const btn = e.target.closest(".mode-btn");
    if (!btn || btn.dataset.mode === MODE) return;
    MODE = btn.dataset.mode;
    box.querySelectorAll(".mode-btn").forEach((b) => b.classList.toggle("active", b === btn));
    positionModePill();
    localLog("info", "入队模式已切换为「" + btn.textContent + "」");
  });
  window.addEventListener("resize", positionModePill);
}

// ------------------------------------------------ 设置页

async function loadSettings() {
  CFG = await App.GetConfig();
  $("cfg-mineru-token").value = CFG.mineru?.token || "";
  $("cfg-mineru-mode").value = CFG.mineru?.model_version || "vlm";
  $("cfg-llm-url").value = CFG.llm?.base_url || "";
  $("cfg-llm-key").value = CFG.llm?.api_key || "";
  $("cfg-llm-model").value = CFG.llm?.model || "";
  $("cfg-llm-conc").value = String(CFG.llm?.concurrency || 3);
  $("cfg-bilingual").checked = !!CFG.export?.bilingual;
  $("cfg-toc").checked = !!CFG.export?.toc;
  $("cfg-twocolumn").checked = !!CFG.export?.two_column;
  $("cfg-skiprefs").checked = CFG.translate?.skip_references !== false;
  $("cfg-repair").checked = CFG.repair?.spelling !== false;
  $("cfg-outdir").value = CFG.export?.output_dir || "";
  DEFAULT_RULES_TEXT = await App.IronRules();
  await loadFontSection();
  renderGlossary();
  renderRules();
}

function renderGlossary() {
  const rows = (CFG.glossary || []).map(
    (g, i) => `<div class="g-row">
      <input data-gi="${i}" data-gk="en" value="${escAttr(g.en)}">
      <input class="zh" data-gi="${i}" data-gk="zh" value="${escAttr(g.zh)}">
      <button class="g-del" data-gdel="${i}" aria-label="删除">✕</button></div>`
  ).join("");
  $("glossary-box").innerHTML =
    `<div class="g-row head"><span>原文术语</span><span>统一译名</span><span></span></div>` + rows;
  bindGlossary();
}

function bindGlossary() {
  document.querySelectorAll("#glossary-box [data-gi]").forEach((inp) => {
    inp.addEventListener("change", () => {
      const i = +inp.dataset.gi, k = inp.dataset.gk;
      if (CFG.glossary[i]) CFG.glossary[i][k] = inp.value;
    });
  });
  document.querySelectorAll("[data-gdel]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const g = CFG.glossary[+btn.dataset.gdel];
      if (!g) return;
      if ((await showModal({
        icon: "bad",
        title: "删除这条术语？",
        body: `<b>${esc(g.en)} → ${esc(g.zh)}</b> 将从术语表移除。`,
        buttons: [{ label: "取消", kind: "ghost" }, { label: "删除", kind: "danger" }],
      })) !== 1) return;
      CFG.glossary.splice(+btn.dataset.gdel, 1);
      renderGlossary();
    });
  });
}

// ------------------------------------------------ 规则预设（折叠卡片列表）

// 正文预设字体栈：mac 的宋体/黑体名（Songti SC / PingFang SC）写进栈里做
// 零成本回退，Windows 上不存在时自动跳过；PDF 字形在导出时嵌入。
const FONT_PRESETS = {
  lxgw: { label: "霞鹜文楷（内嵌 · 译文风）", stack: '"Times New Roman","LXGW WenKai","Microsoft YaHei",serif' },
  songti: { label: "宋体 · 刊物排版风（系统）", stack: '"Times New Roman","SimSun","Songti SC","STSong",serif' },
  yahei: { label: "黑体 · 屏读风（系统）", stack: '"Arial","Microsoft YaHei","PingFang SC",sans-serif' },
};

let DEFAULT_RULES_TEXT = "";
const RULES_MAX = 4000;
let ruleOpen = -1;       // 展开中的预设下标，-1 = 全部折叠
let ruleEditing = false; // 展开中的卡是否处于编辑态

function presets() { return CFG.rules?.presets || (CFG.rules = { active: "", presets: [] }).presets; }

function renderRules() {
  const ps = presets();
  if (!CFG.rules.active && ps.length) CFG.rules.active = ps[0].name;
  if (ruleOpen >= ps.length) ruleOpen = -1;
  $("rule-list").innerHTML = ps.map((p, i) => ruleCardHTML(p, i)).join("");
  const ta = document.querySelector("#rule-list [data-rtext]");
  if (ta) updateRuleCount(ta);
}

function ruleCardHTML(p, i) {
  const live = CFG.rules.active === p.name;
  const open = i === ruleOpen;
  const peek = (p.text || "").split("\n").find((l) => l.trim()) || "（空）";
  let body = "";
  if (open && ruleEditing) {
    body = `
      <textarea class="in rules-edit" data-rtext rows="12" spellcheck="false">${esc(p.text)}</textarea>
      <div class="rule-actions">
        <button class="btn primary" data-rsave="${i}">保存</button>
        <button class="btn ghost-sm" data-rcancel>取消</button>
        <button class="btn ghost-sm" data-rreset="${i}">恢复默认</button>
        <span class="rules-count" data-rcount></span>
      </div>`;
  } else if (open) {
    body = `
      <div class="rule-preview">${esc(p.text)}</div>
      <div class="rule-actions">
        ${live ? "" : `<button class="btn ghost-sm" data-rlive="${i}">设为生效</button>`}
        <button class="btn ghost-sm" data-redit="${i}">编辑</button>
        ${presets().length > 1 ? `<button class="btn ghost-sm danger" data-rdel="${i}">删除</button>` : ""}
      </div>`;
  }
  return `
    <div class="rule-card ${live ? "live" : ""} ${open ? "open" : ""}" data-ri="${i}">
      <div class="rule-row" data-rrow="${i}" role="button" tabindex="0" aria-expanded="${open}" title="展开查看">
        <span class="r-dot"></span>
        <span class="r-name" data-rname="${i}" title="点击改名">${esc(p.name)}</span>
        ${live ? `<span class="r-live">生效中</span>` : ""}
        <span class="r-peek">${esc(peek)}</span>
        <svg class="r-chev" viewBox="0 0 12 12" aria-hidden="true"><path d="M2.6 4.2 L6 7.6 L9.4 4.2" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
      </div>
      ${body ? `<div class="rule-body">${body}</div>` : ""}
    </div>`;
}

// bindRulesStatic 用事件委托绑 #rule-list（卡片随渲染重建，容器只绑一次）。
function bindRulesStatic() {
  $("btn-rule-add").addEventListener("click", addRulePreset);
  const list = $("rule-list");
  list.addEventListener("click", onRuleListClick);
  list.addEventListener("input", (e) => {
    if (e.target.matches("[data-rtext]")) updateRuleCount(e.target);
  });
  list.addEventListener("keydown", (e) => {
    if (e.target.matches(".rule-row") && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      toggleRuleCard(+e.target.dataset.rrow);
    }
  });
}

async function onRuleListClick(e) {
  const nameEl = e.target.closest("[data-rname]");
  if (nameEl) {
    e.stopPropagation();
    startRuleRename(+nameEl.dataset.rname);
    return;
  }
  if (e.target.closest(".prename") || e.target.closest("[data-rtext]")) return;
  const m = (sel) => e.target.closest(`[${sel}]`);
  let hit;
  if ((hit = m("data-rsave"))) return saveRuleText(+hit.dataset.rsave);
  if (m("data-rcancel")) { ruleEditing = false; renderRules(); return; }
  if ((hit = m("data-rreset"))) return resetRuleText();
  if ((hit = m("data-rlive"))) return setRuleLive(+hit.dataset.rlive);
  if ((hit = m("data-redit"))) { ruleEditing = true; ruleOpen = +hit.dataset.redit; renderRules(); return; }
  if ((hit = m("data-rdel"))) return deleteRulePreset(+hit.dataset.rdel);
  if ((hit = m("data-rrow"))) return toggleRuleCard(+hit.dataset.rrow);
}

function editDirty() {
  if (!ruleEditing || ruleOpen < 0) return false;
  const ta = document.querySelector("#rule-list [data-rtext]");
  return !!ta && ta.value !== presets()[ruleOpen].text;
}

async function confirmDiscard() {
  if (!editDirty()) return true;
  return (await showModal({
    icon: "warn",
    title: "放弃未保存的修改？",
    body: "预设的规则正文有<b>未保存的改动</b>，继续操作将丢弃这些修改。",
    buttons: [{ label: "继续编辑", kind: "ghost" }, { label: "放弃修改" }],
  })) === 1;
}

async function toggleRuleCard(i) {
  if (ruleOpen === i) {
    if (!(await confirmDiscard())) return;
    ruleEditing = false;
    ruleOpen = -1;
    renderRules();
    return;
  }
  if (!(await confirmDiscard())) return;
  ruleEditing = false;
  ruleOpen = i;
  renderRules();
}

async function persistRules() {
  try {
    await App.SaveRules(CFG.rules.active, CFG.rules.presets);
    return true;
  } catch (e) {
    localLog("error", "规则保存失败：" + e);
    return false;
  }
}

function flashSaved(i) {
  const row = document.querySelector(`.rule-card[data-ri="${i}"] .rule-row`);
  if (!row) return;
  const s = document.createElement("span");
  s.className = "r-saved";
  s.textContent = "✓ 已保存";
  row.appendChild(s);
  setTimeout(() => s.remove(), 1800);
}

async function saveRuleText(i) {
  const ta = document.querySelector("#rule-list [data-rtext]");
  presets()[i].text = [...(ta ? ta.value : "")].slice(0, RULES_MAX).join("");
  if (!(await persistRules())) return;
  ruleEditing = false;
  renderRules();
  flashSaved(i);
}

function resetRuleText() {
  const ta = document.querySelector("#rule-list [data-rtext]");
  if (!ta) return;
  ta.value = DEFAULT_RULES_TEXT;
  updateRuleCount(ta);
  ta.focus();
}

async function setRuleLive(i) {
  CFG.rules.active = presets()[i].name;
  if (await persistRules()) {
    renderRules();
    flashSaved(i);
  }
}

async function deleteRulePreset(i) {
  const ps = presets();
  if ((await showModal({
    icon: "bad",
    title: `删除预设「${ps[i].name}」？`,
    body: "该预设将被永久删除，<b>不可恢复</b>。",
    buttons: [{ label: "取消", kind: "ghost" }, { label: "删除", kind: "danger" }],
  })) !== 1) return;
  ps.splice(i, 1);
  if (!ps.some((p) => p.name === CFG.rules.active)) CFG.rules.active = ps[0].name;
  ruleEditing = false;
  if (ruleOpen === i) ruleOpen = -1;
  else if (ruleOpen > i) ruleOpen -= 1;
  if (await persistRules()) renderRules();
}

async function addRulePreset() {
  if (!(await confirmDiscard())) return;
  const ps = presets();
  const names = new Set(ps.map((p) => p.name));
  let n = ps.length + 1;
  while (names.has(`预设${n}`)) n++;
  ps.push({ name: `预设${n}`, text: DEFAULT_RULES_TEXT });
  ruleEditing = true;
  ruleOpen = ps.length - 1;
  await persistRules();
  renderRules();
  const ta = document.querySelector("#rule-list [data-rtext]");
  if (ta) { ta.focus(); ta.setSelectionRange(0, 0); }
}

function updateRuleCount(ta) {
  const el = document.querySelector("#rule-list [data-rcount]");
  if (!el) return;
  const n = [...ta.value].length;
  el.textContent = `${n} / ${RULES_MAX}`;
  el.classList.toggle("over", n >= RULES_MAX);
}

// startRuleRename 名称就地改名：随时可改、立即落盘，与内容编辑态无关。
async function startRuleRename(i) {
  if (ruleEditing) {
    if (!(await confirmDiscard())) return; // 与切换卡/新建一致：未保存先确认
    ruleEditing = false; ruleOpen = -1; renderRules();
  }
  const nameEl = document.querySelector(`.rule-card[data-ri="${i}"] .r-name`);
  if (!nameEl) return;
  const p = presets()[i];
  nameEl.innerHTML = `<input class="prename" value="${escAttr(p.name)}" maxlength="20">`;
  const input = nameEl.querySelector("input");
  input.focus();
  input.select();
  input.addEventListener("click", (e) => e.stopPropagation());
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter") input.blur();
    if (e.key === "Escape") { input.dataset.cancel = "1"; input.blur(); }
  });
  input.addEventListener("blur", async () => {
    const name = input.dataset.cancel ? "" : input.value.trim().slice(0, 20);
    if (!name || name === p.name) { renderRules(); return; }
    if (presets().some((q, j) => q.name === name && j !== i)) {
      localLog("warn", `已存在名为「${name}」的预设，换一个名字吧`);
      renderRules();
      return;
    }
    if (CFG.rules.active === p.name) CFG.rules.active = name;
    p.name = name;
    if (await persistRules()) {
      renderRules();
      flashSaved(i);
    }
  });
}

async function loadFontSection() {
  // || []：绑定层万一返回 null（Go nil 切片序列化形态）时不崩，空列表处理
  const list = (await App.FontList()) || [];
  const sel = $("cfg-font");
  sel.innerHTML = Object.entries(FONT_PRESETS)
    .map(([k, p]) => `<option value="${k}">${p.label}</option>`).join("") +
    list.map((uf) => `<option value="${esc(uf.internal_name)}">${esc(uf.display_name)}（导入）</option>`).join("");
  sel.value = CFG.export?.user_font || CFG.export?.font_preset || "lxgw";
  if (sel.selectedIndex < 0) sel.value = "lxgw";
  updatePreview();
  const imp = $("font-import");
  if (imp.dataset.bound) return;
  imp.dataset.bound = "1";
  imp.addEventListener("click", async () => {
    let f = "";
    try {
      f = await App.PickFontFile();
    } catch (e) {
      localLog("error", "打开字体选择框失败：" + e);
      return;
    }
    if (!f) return;
    try {
      const uf = await App.FontAdd(f);
      await App.SetFontPreset("custom", uf.internal_name);
      CFG = await App.GetConfig();
      await loadFontSection();
      localLog("info", `已导入字体：${uf.display_name}`);
    } catch (e) {
      await showModal({ icon: "warn", title: "导入失败", body: esc(String(e)) });
    }
  });
  sel.addEventListener("change", async () => {
    const v = sel.value;
    try {
      await App.SetFontPreset(FONT_PRESETS[v] ? v : "custom", FONT_PRESETS[v] ? "" : v);
      CFG = await App.GetConfig();
      updatePreview();
    } catch (e) {
      localLog("error", "切换字体失败：" + e);
    }
  });
}

// 预览块公式：KaTeX 就绪后逐个渲染（katex.render 自带清空重绘）
function renderPreviewKatex() {
  if (!window.katex) return;
  document.querySelectorAll("#font-preview .tex").forEach((el) => {
    if (el.dataset.done) return; // 只渲染一次：公式用 KaTeX 自带数学字体，不随正文字体变
    katex.render(el.dataset.tex, el, { throwOnError: false });
    el.dataset.done = "1";
  });
}

function updatePreview() {
  const v = $("cfg-font").value;
  let stack;
  if (FONT_PRESETS[v]) {
    stack = FONT_PRESETS[v].stack;
  } else {
    const uf = (CFG.user_fonts || []).find((u) => u.internal_name === v);
    stack = `"${uf ? uf.internal_name : v}","Times New Roman","LXGW WenKai","Microsoft YaHei",serif`;
  }
  $("font-preview").style.fontFamily = stack;
  renderPreviewKatex();
}

function collectSettings() {
  const c = JSON.parse(JSON.stringify(CFG));
  c.mineru.token = $("cfg-mineru-token").value;
  c.mineru.model_version = $("cfg-mineru-mode").value;
  c.llm.base_url = $("cfg-llm-url").value;
  c.llm.api_key = $("cfg-llm-key").value;
  c.llm.model = $("cfg-llm-model").value;
  c.llm.concurrency = parseInt($("cfg-llm-conc").value) || 3;
  // 规则预设：正文与 active 已由 input/click 实时同步进 CFG，随深拷贝带走
  c.rules = { active: CFG.rules.active, presets: CFG.rules.presets, user_global: "" };
  c.export.bilingual = $("cfg-bilingual").checked;
  c.export.toc = $("cfg-toc").checked;
  c.export.two_column = $("cfg-twocolumn").checked;
  c.translate.skip_references = $("cfg-skiprefs").checked;
  c.repair.spelling = $("cfg-repair").checked;
  c.export.output_dir = $("cfg-outdir").value;
  // 字体下拉的变更即时经 SetFontPreset 落盘并刷新 CFG，这里随深拷贝带上
  c.export.font_preset = CFG.export?.font_preset || "lxgw";
  c.export.user_font = CFG.export?.user_font || "";
  c.glossary = CFG.glossary.filter((g) => g.en.trim() && g.zh.trim());
  return c;
}

// 脏状态：全局保存管辖的字段一有改动就亮「有未保存的修改」；
// 规则卡/字体下拉等即时落盘的控件不计入。
let settingsDirty = false;
function markSettingsDirty(e) {
  if (e && e.target.closest("#rule-list,#btn-rule-add,#cfg-font,#font-import,#mineru-link")) return;
  if (settingsDirty) return;
  settingsDirty = true;
  $("dirty-pill").classList.add("show");
}
function clearSettingsDirty() {
  settingsDirty = false;
  $("dirty-pill").classList.remove("show");
}

function bindSave() {
  $("btn-save-config").addEventListener("click", async () => {
    try {
      await App.SaveConfig(collectSettings());
      CFG = await App.GetConfig();
      clearSettingsDirty();
      $("save-hint").textContent = "✓ 已保存 " + new Date().toLocaleTimeString();
      setTimeout(() => ($("save-hint").textContent = ""), 3000);
    } catch (e) {
      $("save-hint").textContent = "保存失败：" + e;
    }
  });
  $("g-add").addEventListener("click", () => {
    CFG.glossary = CFG.glossary || [];
    CFG.glossary.push({ en: "", zh: "" });
    renderGlossary();
  });
}

// ------------------------------------------------ 日志

function bindLog() {
  document.querySelectorAll(".log-filter").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll(".log-filter").forEach((b) => b.classList.remove("on"));
      btn.classList.add("on");
      logFilter = btn.dataset.lv;
      renderLog();
    });
  });
  $("btn-clear-log").addEventListener("click", async () => {
    await App.ClearLog();
    logLines = [];
    renderLog();
  });
}

function flushPending() {
  for (const l of logPending.splice(0)) {
    if (logFilter === "all" || l.level === logFilter) appendLogLine(l);
  }
}

let pendingFlushTimer = null;
function queueLog(line) {
  logLines.push(line);
  logPending.push(line);
  if (!pendingFlushTimer) {
    pendingFlushTimer = setTimeout(() => { pendingFlushTimer = null; flushPending(); }, 150);
  }
}

function appendLogLine(line) {
  const panel = $("log-panel");
  if (!panel) return;
  const div = document.createElement("div");
  div.dataset.lv = line.level;
  div.innerHTML = `<span class="t">${esc(line.time)}</span><span class="lv lv-${esc(line.level)}">${esc(line.level)}</span>${esc(line.msg)}`;
  panel.appendChild(div);
  panel.scrollTop = panel.scrollHeight;
  if (panel.children.length > 500) panel.removeChild(panel.firstChild);
}

function renderLog() {
  const panel = $("log-panel");
  panel.innerHTML = "";
  const arr = logLines.filter((l) => logFilter === "all" || l.level === logFilter);
  if (!arr.length) {
    panel.innerHTML = `<div class="log-empty">暂无日志</div>`;
    return;
  }
  for (const line of arr) appendLogLine(line);
}

// 覆盖 appendLog：高频日志节流合并
function appendLog(line) {
  queueLog(line);
}

// ------------------------------------------------ 启动

// 自绘模态框：替代原生 confirm/alert（原生弹窗会显示 wails.localhost 来源）。
// showModal({icon:'warn'|'bad', title, body, buttons:[{label, kind}]}) → Promise<按钮下标>；
// Esc = -1、Enter = 主按钮。样式走主题令牌，深浅自适应。
function showModal(opt) {
  return new Promise((resolve) => {
    const scrim = $("modal-scrim");
    const ico = $("modal-ico"), title = $("modal-title"), body = $("modal-body"), btns = $("modal-btns");
    ico.className = "m-ico " + (opt.icon === "bad" ? "bad" : "warn");
    ico.textContent = opt.icon === "bad" ? "\u2715" : "!";
    title.textContent = opt.title || "";
    body.innerHTML = opt.body || "";
    btns.innerHTML = "";
    (opt.buttons || [{ label: "知道了" }]).forEach((b, i) => {
      const el = document.createElement("button");
      el.className = "btn " + (b.kind === "danger" ? "danger" : b.kind === "ghost" ? "ghost" : "primary");
      el.textContent = b.label;
      el.addEventListener("click", () => close(i));
      btns.appendChild(el);
    });
    let done = false;
    const onKey = (e) => {
      if (e.key === "Escape") close(-1);
      if (e.key === "Enter") close(0);
    };
    function close(v) {
      if (done) return;
      done = true;
      document.removeEventListener("keydown", onKey);
      scrim.hidden = true;
      resolve(v);
    }
    scrim.hidden = false;
    document.addEventListener("keydown", onKey);
    scrim.addEventListener("pointerdown", (e) => { if (e.target === scrim) close(-1); }, { once: true });
    (btns.querySelector(".btn.primary,.btn.danger") || btns.querySelector(".btn"))?.focus();
  });
}

// 连接测试小图标：点击测试，四态（idle/spin/ok/bad），输入改动即重置
function bindConnTests() {
  document.querySelectorAll(".conn-dot").forEach((dot) => {
    const kind = dot.dataset.conn;
    const input = dot.parentElement.querySelector("input");
    const set = (cls, label) => {
      dot.className = "conn-dot " + cls;
      dot.textContent = cls === "spin" ? "◌" : cls === "ok" ? "✓" : cls === "bad" ? "✕" : "⇄";
      if (label) dot.title = label; else dot.title = "测试连接";
    };
    dot.addEventListener("click", async () => {
      if (dot.classList.contains("spin")) return;
      set("spin", "测试中…");
      try {
        if (kind === "llm") {
          const n = await App.TestLLM();
          set("ok", `连通 · ${n} 个模型`);
          localLog("info", `LLM 接口连通：${n} 个模型`);
        } else {
          const msg = await App.TestMinerU();
          set("ok", msg);
          localLog("info", "MinerU " + msg);
        }
      } catch (e) {
        set("bad", String(e));
        localLog("warn", "连接测试失败（" + (kind === "llm" ? "LLM" : "MinerU") + "）：" + e);
      }
    });
    input.addEventListener("input", () => set("idle"));
  });
}

// bindCheckUpdate 设置页「关于与更新」卡：手动检查 GitHub 最新版本，
// 状态机与连接测试同款（空闲→检查中→结果）；Release 页外链走系统浏览器。
function bindCheckUpdate() {
  const btn = $("btn-check-update");
  const out = $("update-result");
  btn.addEventListener("click", async () => {
    if (btn.disabled) return;
    btn.disabled = true;
    btn.textContent = "检查中…";
    try {
      const info = await App.CheckUpdate();
      out.hidden = false;
      out.innerHTML = info.has_update
        ? `🆕 发现新版本 <b>${esc(info.latest)}</b>（当前 ${esc(info.current)}）——前往 Release 页下载${info.notes ? `<br>${esc(info.notes)}` : ""}`
        : `✓ 已是最新（${esc(info.latest || info.current)}）`;
      localLog("info", info.has_update ? `发现新版本 ${info.latest}` : "已是最新版本");
    } catch (e) {
      out.hidden = false;
      out.textContent = "检查失败：" + e;
      localLog("warn", "检查更新失败：" + e);
    } finally {
      btn.disabled = false;
      btn.textContent = "检查更新";
    }
  });
  $("release-link").addEventListener("click", (e) => {
    e.preventDefault();
    rt.BrowserOpenURL(e.currentTarget.getAttribute("href"));
  });
}

// 拉取模型：从上游 /v1/models 获取列表，弹出带标题栏的可筛选选择框
function bindFetchModels() {
  const btn = $("btn-fetch-models");
  const input = $("cfg-llm-model");
  const pop = $("model-pop");
  let models = [];
  let open = false;
  let armAt = 0; // 面板打开后延迟武装“点外关闭”，防同一次点击的余波误关

  function renderPop(filter) {
    const f = (filter || "").toLowerCase();
    const hits = models.filter((m) => !f || m.toLowerCase().includes(f));
    const cur = input.value.trim();
    const list = hits.length
      ? hits.map((m) => `<div class="mp-item${m === cur ? " cur" : ""}" data-m="${escAttr(m)}" title="${escAttr(m)}">${esc(m)}</div>`).join("")
      : `<div class="mp-empty">没有匹配「${esc(filter)}」的模型</div>`;
    pop.innerHTML = `<div class="mp-head">
        <span>共 ${models.length} 个模型 · 点击选择</span>
        <button class="mp-close" aria-label="关闭">✕</button>
      </div>` + list;
    pop.querySelector(".mp-close").addEventListener("click", closePop);
    pop.querySelectorAll(".mp-item").forEach((it) => {
      it.addEventListener("click", () => {
        input.value = it.dataset.m;
        input.dispatchEvent(new Event("input", { bubbles: true }));
        closePop(); // 不再 focus 输入框：focus 会触发 combobox 重开逻辑，弹层关了又弹回
      });
    });
  }
  function openPop() {
    open = true;
    pop.hidden = false;
    renderPop(""); // 打开即全量展示；筛选只由用户的键入触发
    armAt = Date.now() + 300; // 300ms 内的 pointerdown 不视为“点外关闭”
  }
  function closePop() {
    open = false;
    pop.hidden = true;
  }

  btn.addEventListener("click", async () => {
    if (open) { closePop(); return; } // 再点一次 = 收起
    btn.disabled = true;
    const old = btn.textContent;
    btn.textContent = "拉取中…";
    try {
      models = await App.FetchModels();
      localLog("info", `已拉取 ${models.length} 个上游模型`);
      btn.textContent = `✓ ${models.length} 个`;
      openPop();
    } catch (e) {
      btn.textContent = "拉取失败";
      localLog("error", "拉取模型失败：" + e);
      setTimeout(() => (btn.textContent = old), 1600);
      btn.disabled = false;
      return;
    }
    setTimeout(() => { btn.textContent = old; btn.disabled = false; }, 1800);
  });
  input.addEventListener("input", () => { if (open) renderPop(input.value.trim()); });
  input.addEventListener("focus", () => { if (models.length && !open) openPop(); });
  document.addEventListener("pointerdown", (e) => {
    if (!open || Date.now() < armAt) return;
    if (e.target.closest("#model-pop") || e.target === input || e.target.closest("#btn-fetch-models")) return;
    closePop();
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && open) closePop(); });
}

// 主题：默认深色（dark），浅色存 localStorage；切换平滑过渡
function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  try { localStorage.setItem("literature-theme", t); } catch (e) { /* 忽略 */ }
}
function bindTheme() {
  applyTheme(localStorage.getItem("literature-theme") || "light");
  $("btn-theme").addEventListener("click", () => {
    applyTheme(document.documentElement.dataset.theme === "light" ? "dark" : "light");
  });
}

window.addEventListener("DOMContentLoaded", async () => {
  try {
    bindTheme();
    bindFetchModels();
    bindConnTests();
    bindCheckUpdate();
    bindTitlebar();
    bindNav();
    bindDrop();
    bindModeSwitch();
    bindQueueButtons();
    bindSave();
    bindLog();
    bindRulesStatic();
    // 侧栏底部 GitHub 入口：走系统默认浏览器（WebView 内不能开外链）
    $("repo-link").addEventListener("click", (e) => {
      e.preventDefault();
      rt.BrowserOpenURL(e.currentTarget.getAttribute("href"));
    });
    // MinerU API 管理直达
    $("mineru-link").addEventListener("click", (e) => {
      e.preventDefault();
      rt.BrowserOpenURL(e.currentTarget.getAttribute("href"));
    });
    // 队列开始/暂停开关
    $("btn-queue-toggle").addEventListener("click", async () => {
      try {
        if (QUEUE_STATE.running) await App.QueuePause();
        else await App.QueueStart();
        renderQueue();
      } catch (e) {
        localLog("error", "队列操作失败：" + e);
      }
    });
    rt.EventsOn("task:update", (t) => upsertTask(t));
    rt.EventsOn("queue:change", () => renderQueue());
    rt.EventsOn("log:line", (line) => appendLog(line));
    await loadSettings();
    // 脏监听延迟绑定：跳过 WebView 自动填充密码框时触发的 input 事件
    setTimeout(() => {
      $("view-settings").addEventListener("input", markSettingsDirty);
      $("view-settings").addEventListener("change", markSettingsDirty);
    }, 800);
    renderQueue();
    const tail = await App.GetLogTail();
    logLines = tail.slice();
    renderLog();
    positionModePill();
    // 版本号单一来源：Go 侧 internal/version 经绑定给出；壳缺失时退化为 ?
    let VER = "?";
    try { VER = await App.Version(); } catch (e) { /* 无绑定时显示 ? */ }
    $("about-version").textContent = "当前版本 " + VER;
    const fontsOk = CFG.mineru?.token && CFG.llm?.api_key;
    $("sidebar-status").innerHTML = fontsOk
      ? `${VER} <span class="dot">●</span> 就绪`
      : `${VER} <span style="color:var(--bad)">●</span> 待配置`;
  } catch (e) {
    // 初始化链断裂时状态栏会永远停在 index.html 的占位文案「引擎启动中…」，
    // 这里把真实错误亮出来，避免启动故障伪装成正常状态
    console.error("初始化失败", e);
    $("sidebar-status").innerHTML =
      `<span style="color:var(--bad)">●</span> 启动异常：` + esc(String(e)).slice(0, 120);
  }
});
