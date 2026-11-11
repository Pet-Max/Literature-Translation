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
const escAttr = (s) => esc(s).replace(/"/g, "&quot;").replace(/'/g, "&#39;"); // 用于属性位（含单引号）
const fileCode = (path) => {
  const base = path.split(/[\\/]/).pop().replace(/\.(md|pdf)$/i, "");
  const m = base.match(/^([A-Za-z]+\d+)/);
  return m ? m[1] : base.slice(0, 12);
};

// ------------------------------------------------ 视图切换

function bindNav() {
  document.querySelectorAll(".nav-item").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!settingsRecoveryBlocked && !(await requireSettingsSaved())) return;
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
  $("win-close").addEventListener("click", closeWithSettings);
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

// 三种任务形态的步进器阶段表（按 kind 取用）。pdf 的"校准"= 拼写校准，
// 配置关闭或扫描件会跳过——该步会随后续阶段开始被标成 ✓（可接受的近似）。
const STAGES_BY_KIND = {
  pdf: ["解析", "校准", "翻译", "排版", "完成"],
  md: ["翻译", "排版", "完成"],
  export: ["排版", "完成"],
};
const STAGE_IDX_BY_KIND = {
  pdf: { "解析中": 0, "拼写校准": 1, "翻译中": 2, "排版中": 3, "完成": 4 },
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
  // 定位优先用 stage（「拼写校准」这类子步骤只改 stage 不改 status）；
  // 「解析中（排队中）」这类形态映射不到时落到 status。
  const total = stagesOf(t).length;
  if (t.status === "等待") return 1;
  if (t.status === "完成") return total;
  const idx = stageIdxOf(t);
  const cur = idx[t.stage] ?? idx[t.status];
  if (t.status === "失败") return (cur === undefined ? 0 : cur) + 1;
  return cur === undefined ? 1 : Math.min(cur + 1, total);
}

function taskCard(t) {
  const code = fileCode(t.source_path);
  const fileName = t.source_path.split(/[\\/]/).pop();
  // chip 文案：拼写校准是解析的子步骤（Status 仍是枚举「解析中」），
  // 徽章按 stage 显示更精确一步
  const chipText = t.status === "解析中" && t.stage === "拼写校准" ? "拼写校准" : t.status;
  const chipClass = { "解析中": "run", "拼写校准": "run", "翻译中": "run", "排版中": "run", "完成": "ok", "失败": "bad" }[chipText] || "";

  // 步进器：只渲染已到达的阶段，成功一步亮一步
  // cur 与 visibleStages 同口径用 stage 优先定位（拼写校准期间「解析」标 ✓）
  const vis = visibleStages(t);
  const idx = stageIdxOf(t);
  const cur = idx[t.stage] ?? idx[t.status];
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
    // 分桶渲染（段数无关）：≤60 段一段一格——绿=已完成、红=失败、灰=待翻；
    // 更多段时聚合为 60 桶，桶内含失败→红、全完成→绿，其余灰。
    // done_idx/failed_idx 由引擎进度事件携带；旧版引擎没有时退化为比例条。
    const CAP = 60;
    const cells = Math.min(t.total, CAP);
    const hasIdx = Array.isArray(t.done_idx) || Array.isArray(t.failed_idx);
    const doneSet = new Set(t.done_idx || []);
    const failSet = new Set(t.failed_idx || []);
    let bars = "";
    if (hasIdx && t.total <= CAP) {
      for (let i = 0; i < t.total; i++) {
        bars += `<i class="${failSet.has(i) ? "bad" : doneSet.has(i) ? "f" : ""}"></i>`;
      }
    } else if (hasIdx) {
      const per = t.total / cells;
      for (let c = 0; c < cells; c++) {
        const lo = Math.floor(c * per);
        const hi = Math.min(t.total, Math.floor((c + 1) * per));
        let bad = false, allDone = hi > lo;
        for (let i = lo; i < hi; i++) {
          if (failSet.has(i)) bad = true;
          else if (!doneSet.has(i)) allDone = false;
        }
        bars += `<i class="${bad ? "bad" : allDone ? "f" : ""}"></i>`;
      }
    } else {
      const filled = Math.round((t.done / t.total) * cells);
      for (let i = 0; i < cells; i++) {
        bars += `<i class="${i < filled ? "f" : ""}"></i>`;
      }
    }
    progressHtml = `
      <div class="segbar" aria-label="翻译进度">${bars}</div>
      <div class="seg-cap"><span>第 ${t.done} / ${t.total} 段</span><span class="eta">失败 ${t.failed}${t.sweeping ? " · 补翻中" : ""}</span></div>`;
  }

  // 状态相关的 meta 文案（信息，不做装饰）；kind 标注实际走的路径
  const kindLabel = {
    pdf: "PDF 原文 · 完整流程",
    md: "英文 MD · 翻译+排版",
    export: "中文 MD · 仅排版",
  }[t.kind] || "英文 MD · 翻译+排版";
  let meta;
  if (t.status === "完成") {
    const failPart = t.failed > 0 && t.kind !== "export" ? ` · ${t.failed} 段失败保留英文` : "";
    meta = t.kind === "export" ? "排版完成" : `${t.done} 段译文${failPart}` + (t.refs_kept > 0 ? ` · 参考文献 ${t.refs_kept} 条保留原文` : "");
  } else if (t.status === "失败") {
    // 引擎未就绪/建归档失败/语言预检被拒发生在解析之前——别误标"卡在「解析」"。
    // 「已开始」= 流程推进到过任一实际阶段（解析中/拼写校准/翻译中/排版中），
    // 或 MinerU 轮询已上报过状态（解析中（…）形态）。
    const stageKnown = stageIdxOf(t)[t.stage] !== undefined || /^解析中（/.test(t.stage || "");
    const started = t.done > 0 || t.total > 0 || stageKnown;
    meta = started ? `${kindLabel} · 卡在「${stagesOf(t)[vis - 1]}」这一步`
                   : `${kindLabel} · 未能开始（详见错误信息）`;
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
    ? `<div class="fail-note">${esc(t.err.slice(0, 240))}</div>` : "";
  // 完成但有失败段：橙色提醒（区别于硬错误的红色），告知补翻路径
  const sweepNote = t.status === "完成" && t.failed > 0 && t.kind !== "export"
    ? `<div class="warn-note">有 ${t.failed} 段经多轮重试仍未通过校验，成品对应位置保留英文原文。点「重跑」补翻：已翻好的段走断点复用，只重翻失败段。</div>` : "";
  const enterCls = cardSeen[t.id] ? "" : " enter";
  cardSeen[t.id] = true;
  return `
  <div class="task${enterCls}" id="task-${t.id}">
    <div class="codetile${t.kind !== "pdf" ? " md" : ""}" title="${escAttr(t.source_path)}">
      <div class="codetile-code">${esc(code)}</div>
      <div class="codetile-kind">${t.kind === "pdf" ? "文献原文" : t.kind === "export" ? "中文稿" : "已解析稿"}</div>
    </div>
    <div class="task-body">
      <div class="task-name" title="${escAttr(t.source_path)}">${esc(fileName)}</div>
      <div class="task-meta">${esc(meta)}</div>
      <div class="stepper">${stages}</div>
      ${progressHtml}
      ${failNote}
      ${sweepNote}
    </div>
    <div class="task-side">
      <span class="status-chip ${chipClass}">${esc(chipText)}</span>
      ${sideActions}
    </div>
  </div>`;
}

let QUEUE_STATE = { running: false, paused: true, pending: 0 };
// 刷新令牌：task:update 与 queue:change 几乎同时到达时会并发发起两次
// 刷新，先发出的慢响应后落地会把 UI 拉回旧状态——比对令牌丢弃过期响应
let queueReqSeq = 0;

function renderQueue() {
  const my = ++queueReqSeq;
  Promise.all([App.QueueList(), App.QueueState()]).then(([tasks, st]) => {
    if (my !== queueReqSeq) return; // 已有更新的刷新，本次结果作废
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
      toggle.textContent = st.paused ? "暂停中" : "暂停";
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

let rqTimer = null;
function upsertTask(t) {
  // 进度事件高频：250ms 尾随防抖重绘（缓存即时更新，仅延后 DOM 重建）
  if (rqTimer) clearTimeout(rqTimer);
  rqTimer = setTimeout(() => { rqTimer = null; renderQueue(); }, 250);
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
    b.addEventListener("click", async () => {
      try {
        await App.QueueRetry(b.dataset.retry);
        // 重跑从第一阶段重来：清掉残留的高阶段数，新阶段才有入场动画
        delete lastStageCount[b.dataset.retry];
        renderQueue();
      } catch (e) {
        // 运行中的任务后端会拒绝重排队——弹窗说明，不再静默无反应
        await showModal({ icon: "warn", title: "无法重新排队", body: esc(String(e)) });
      }
    });
  });
  document.querySelectorAll("[data-remove]").forEach((b) => {
    if (b.dataset.bound) return;
    b.dataset.bound = "1";
    b.addEventListener("click", () => App.QueueRemove(b.dataset.remove).then(() => {
      // 卡片已移除：两条渲染缓存同步清，防缓慢泄漏
      delete lastStageCount[b.dataset.remove];
      delete cardSeen[b.dataset.remove];
      renderQueue();
    }).catch((e) => showModal({ icon: "warn", title: "无法移除", body: esc(String(e)) })));
  });
  document.querySelectorAll("[data-startone]").forEach((b) => {
    if (b.dataset.bound) return;
    b.dataset.bound = "1";
    b.addEventListener("click", async () => {
      try {
        if (!(await requireSettingsSaved())) return;
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
      try {
        const kept = await App.QueueClearFinished();
        // 只清掉被移除任务的渲染缓存，留下的卡片不重播入场动画
        const ids = new Set((kept || []).map((t) => t.id));
        for (const id of Object.keys(lastStageCount)) {
          if (!ids.has(id)) delete lastStageCount[id];
        }
        for (const id of Object.keys(cardSeen)) {
          if (!ids.has(id)) delete cardSeen[id];
        }
        renderQueue();
      }
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

async function loadSettings(config = null) {
  CFG = config || await App.GetConfig();
  CFG.glossary = copySettings(CFG.glossary_rows ?? CFG.glossary ?? []);
  $("cfg-mineru-token").value = CFG.mineru?.token || "";
  $("cfg-mineru-mode").value = CFG.mineru?.model_version || "vlm";
  $("cfg-llm-url").value = CFG.llm?.base_url || "";
  $("cfg-llm-key").value = CFG.llm?.api_key || "";
  $("cfg-llm-model").value = CFG.llm?.model || "";
  const concSel = $("cfg-llm-conc");
  const concurrency = String(CFG.llm?.concurrency || 3);
 concSel.value = concurrency;
  // 手改配置文件设了选项外的并发值（2/4 等）时补一个临时选项，避免下拉显示空白
  if (concSel.selectedIndex < 0) {
    const o = document.createElement("option");
    o.value = concurrency;
    o.textContent = concurrency;
    concSel.appendChild(o);
    concSel.value = o.value;
  }
  $("cfg-bilingual").checked = !!CFG.export?.bilingual;
  $("cfg-toc").checked = !!CFG.export?.toc;
  $("cfg-twocolumn").checked = !!CFG.export?.two_column;
  $("cfg-skiprefs").checked = CFG.translate?.skip_references !== false;
  $("cfg-repair").checked = CFG.repair?.spelling !== false;
  $("cfg-repair-symbols").checked = CFG.repair?.symbols !== false;
  $("cfg-outdir").value = CFG.export?.output_dir || "";
  DEFAULT_RULES_TEXT = await App.IronRules();
  GENERAL_RULES_TEXT = await App.GeneralRules();
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
  // 恢复默认按钮：与出厂示例完全一致时置灰，防「点了没变化」的错觉
  App.DefaultGlossary().then((def) => {
    const cur = CFG.glossary || [];
    const same = cur.length === def.length && def.every((d, i) => cur[i] && cur[i].en === d.en && cur[i].zh === d.zh);
    const btn = $("g-reset");
    if (btn) { btn.disabled = same; btn.title = same ? "当前已是默认术语表" : "清除全部术语，还原为出厂示例"; }
  }).catch(() => {});
}

function bindGlossary() {
  document.querySelectorAll("#glossary-box [data-gi]").forEach((inp) => {
    const update = (delay) => {
      if (settingsComposing.has(inp)) return;
      const i = +inp.dataset.gi, k = inp.dataset.gk;
      if (CFG.glossary[i]) {
        CFG.glossary[i][k] = inp.value;
        saveGlossary(delay);
      }
    };
    inp.addEventListener("compositionstart", () => settingsComposing.add(inp));
    inp.addEventListener("compositionend", () => { settingsComposing.delete(inp); update(600); });
    inp.addEventListener("input", () => update(600));
    inp.addEventListener("blur", () => update(0));
  });
  document.querySelectorAll("[data-gdel]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const i = +btn.dataset.gdel, g = CFG.glossary[i];
      if (!g) return;
      if ((await showModal({icon:"bad", title:"删除这条术语？", body:`<b>${esc(g.en)} → ${esc(g.zh)}</b> 将从术语表移除。`, buttons:[{label:"取消",kind:"ghost"},{label:"删除",kind:"danger"}]})) !== 1) return;
      const removed = copySettings(CFG.glossary.splice(i, 1)[0]);
      renderGlossary(); saveGlossary();
      showSettingsUndo("glossary", () => { CFG.glossary.splice(Math.min(i, CFG.glossary.length), 0, removed); renderGlossary(); saveGlossary(); });
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

let DEFAULT_RULES_TEXT = ""; // 「物理」出厂文案（App.IronRules）
let GENERAL_RULES_TEXT = ""; // 「通用学术」出厂文案：非物理预设恢复默认的还原源、新建预设的初始底稿
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
      <textarea class="in rules-edit" data-rtext rows="12" spellcheck="false">${esc(p.draft_text ?? p.text)}</textarea>
      <div class="rule-actions">
        <button class="btn primary" data-rsave="${i}">应用到新任务</button>
        <button class="btn ghost-sm" data-rcancel>完成编辑</button>
        <button class="btn ghost-sm" data-rreset="${i}" title="${restoreSourceFor(p.name).tip}">恢复默认</button>
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
        ${live ? `<span class="r-live">生效中</span>` : ""}${p.draft_text !== undefined ? '<span class="r-draft">有未应用修改</span>' : ''}
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
    if (e.target.matches("[data-rtext]") && !e.isComposing) captureRuleDraft(e.target);
  });
  list.addEventListener("compositionend", (e) => { if (e.target.matches("[data-rtext]")) captureRuleDraft(e.target);
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

function captureRuleDraft(ta) {
  if (ruleOpen < 0) return;
  const p = presets()[ruleOpen];
  if (ta.value === p.text) delete p.draft_text; else p.draft_text = ta.value;
  updateRuleCount(ta); saveRulesDraft(600);
  const row = ta.closest(".rule-card").querySelector(".rule-row");
  row.querySelector(".r-draft")?.remove();
  if (p.draft_text !== undefined) { const b = document.createElement("span"); b.className = "r-draft"; b.textContent = "有未应用修改"; row.appendChild(b); }
}
function toggleRuleCard(i) {
  if (ruleOpen === i) {
    ruleEditing = false;
    ruleOpen = -1;
    renderRules();
    return;
  }
  ruleEditing = false;
  ruleOpen = i;
  renderRules();
}

async function persistRules() {
  saveRulesDraft();
  await drainSettings();
  return !settingsJobs.has("rules");
}

function flashSaved(i) {
  const row = document.querySelector(`.rule-card[data-ri="${i}"] .rule-row`);
  if (!row) return;
  const s = document.createElement("span");
  s.className = "r-saved";
  s.textContent = "✓ 已应用";
  row.appendChild(s);
  setTimeout(() => s.remove(), 1800);
}

async function saveRuleText(i) {
  const preset = presets()[i];
  const ta = document.querySelector("#rule-list [data-rtext]");
  const text = ta ? ta.value : presets()[i].draft_text ?? presets()[i].text;
  if (!text.trim() || [...text].length > RULES_MAX) {
    await showModal({icon:"warn", title:"规则尚不能应用", body:`正文应为 1–${RULES_MAX} 个字符；草稿会保留，请修改后再应用。`}); return;
  }
  presets()[i].text = text; delete presets()[i].draft_text;
  CFG.rules.active = presets()[i].name;
  if (!(await persistRules())) return;
  if (presets()[i] !== preset || ruleOpen !== i || preset.draft_text !== undefined) return;
  ruleEditing = false; renderRules(); flashSaved(i);
}

// restoreSourceFor 返回预设「恢复默认」的还原源与按钮提示：「物理」
// 回物理出厂文案，其余（通用学术/新建/改名后的预设）统一回通用学术。
function restoreSourceFor(name) {
  const physics = name === "物理";
  return {
    text: physics ? DEFAULT_RULES_TEXT : GENERAL_RULES_TEXT,
    tip: physics ? "还原为物理出厂规则" : "还原为通用学术出厂规则",
  };
}

function resetRuleText() {
  const ta = document.querySelector("#rule-list [data-rtext]");
  if (!ta) return;
  ta.value = restoreSourceFor(presets()[ruleOpen]?.name).text;
  captureRuleDraft(ta);
  ta.focus();
}

function setRuleLive(i) {
  CFG.rules.active = presets()[i].name;
  renderRules(); saveRulesDraft();
}

async function deleteRulePreset(i) {
  const ps = presets();
  if (ps.length <= 1) return;
  const removed = copySettings(ps[i]); const oldActive = CFG.rules.active;
  if ((await showModal({
    icon: "bad",
    title: `删除预设「${ps[i].name}」？`,
    body: "删除后 10 秒内可撤销。",
    buttons: [{ label: "取消", kind: "ghost" }, { label: "删除", kind: "danger" }],
  })) !== 1) return;
  ps.splice(i, 1);
  if (!ps.some((p) => p.name === CFG.rules.active)) CFG.rules.active = ps[0].name;
  ruleEditing = false;
  if (ruleOpen === i) ruleOpen = -1;
  else if (ruleOpen > i) ruleOpen -= 1;
  renderRules(); saveRulesDraft();
  showSettingsUndo("rules", () => {
    if (presets().some(p => p.name === removed.name)) { showModal({icon:"warn",title:"无法撤销",body:"已有同名预设，请先改名后再撤销。"}); return false; }
    presets().splice(Math.min(i, presets().length), 0, removed);
    if (oldActive === removed.name) CFG.rules.active = removed.name;
    renderRules(); saveRulesDraft();
  });
}

function addRulePreset() {
  const ps = presets();
  const names = new Set(ps.map((p) => p.name));
  let n = ps.length + 1;
  while (names.has(`预设${n}`)) n++;
  // 新建以「通用学术」为底稿：普适基线，与恢复默认的兜底源一致
  ps.push({ name: `预设${n}`, text: GENERAL_RULES_TEXT });
  ruleEditing = true;
  ruleOpen = ps.length - 1;
  renderRules(); saveRulesDraft();
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
function startRuleRename(i) {
  if (ruleEditing) {
    // The editor's input events already retained the draft.
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
    if (e.isComposing) return;
    if (e.key === "Enter") input.blur();
    if (e.key === "Escape") { input.dataset.cancel = "1"; input.blur(); }
  });
  input.addEventListener("blur", () => {
    const name = input.dataset.cancel ? "" : input.value.trim().slice(0, 20);
    if (!name || name === p.name) { renderRules(); return; }
    if (presets().some((q, j) => q.name === name && j !== i)) {
      localLog("warn", `已存在名为「${name}」的预设，换一个名字吧`);
      renderRules();
      return;
    }
    if (CFG.rules.active === p.name) CFG.rules.active = name;
    p.name = name;
    renderRules(); saveRulesDraft();
  });
}

async function loadFontSection() {
  // || []：绑定层万一返回 null（Go nil 切片序列化形态）时不崩，空列表处理
  const list = (await App.FontList()) || [];
 CFG.user_fonts = copySettings(list);
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
    // Keep the import action queued until it runs; a font selection must not replace it.
    imp.disabled = sel.disabled = true;
    try {
      let uf = null;
      queueSettingsAction("output-font", async () => {
        try {
          if (uf && !((await App.FontList()) || []).some(f => f.internal_name === uf.internal_name && f.file_name === uf.file_name)) uf = null;
          uf ||= await App.FontAdd(f);
          await App.SetFontPreset("custom", uf.internal_name);
          if (!settingsJobs.has("output-font")) { CFG.export.font_preset = "custom"; CFG.export.user_font = uf.internal_name; }
          await loadFontSection();
          localLog("info", `已导入字体：${uf.display_name}`);
          return {export:{font_preset:"custom", user_font:uf.internal_name}};
        } finally { imp.disabled = sel.disabled = false; }
      });
    } catch (e) {
      imp.disabled = sel.disabled = false;
      await showModal({ icon: "warn", title: "导入失败", body: esc(String(e)) });
    }
  });
  sel.addEventListener("change", async () => {
    const v = sel.value;
    try {
      const preset = FONT_PRESETS[v] ? v : "custom", user = FONT_PRESETS[v] ? "" : v;
      CFG.export.font_preset = preset; CFG.export.user_font = user;
      updatePreview();
      queueSettingsAction("output-font", () => App.SetFontPreset(preset, user), {export:{font_preset:preset, user_font:user}});
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

let previewVersion = 0;
const previewFaces = new Map();
async function updatePreview() {
  const mine = ++previewVersion;
  const v = $("cfg-font").value, hint = $("font-preview-status");
  const fallback = FONT_PRESETS.lxgw.stack;
  hint.textContent = "";
  if (FONT_PRESETS[v]) {
    $("font-preview").style.fontFamily = FONT_PRESETS[v].stack;
  } else {
    $("font-preview").style.fontFamily = fallback;
    const uf = (CFG.user_fonts || []).find(u => u.internal_name === v);
    try {
      if (!uf || !uf.file_name) throw new Error("字体未注册");
      let load = previewFaces.get(uf.file_name);
      if (!load) {
        const family = "ImportedPreview" + (previewFaces.size + 1);
        load = (async () => {
          const face = await new FontFace(family, `url("/userfonts/${encodeURIComponent(uf.file_name)}")`).load();
          document.fonts.add(face);
          await document.fonts.load(`16px "${family}"`, $("font-preview").textContent);
          return family;
        })();
        previewFaces.set(uf.file_name, load);
        load.catch(() => previewFaces.delete(uf.file_name));
      }
      const family = await load;
      if (mine !== previewVersion) return;
      $("font-preview").style.fontFamily = `"${family}",${fallback}`;
    } catch {
      if (mine !== previewVersion) return;
      hint.textContent = "字体加载失败，已使用默认字体预览";
    }
  }
  if (mine === previewVersion) renderPreviewKatex();
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
  let copyResetTimer = 0;
  const ICON_COPY = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>';
  $("btn-copy-log").innerHTML = ICON_COPY; // 图标单源：HTML 静态副本已移除，此处注入
  const ICON_CHECK = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5"/></svg>';
  const ICON_FAIL = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>';
  $("btn-copy-log").addEventListener("click", async () => {
    const text = logLines
      .filter((l) => logFilter === "all" || l.level === logFilter)
      .map((l) => `${l.time} ${l.level.toUpperCase().padEnd(5)} ${l.msg}`)
      .join("\n");
    if (!text) return;
    let ok = false;
    try {
      await navigator.clipboard.writeText(text);
      ok = true;
    } catch {
      // WebView2 剪贴板权限兜底：隐藏选区 + execCommand
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      ok = document.execCommand("copy");
      ta.remove();
    }
    const btn = $("btn-copy-log");
    btn.innerHTML = ok ? ICON_CHECK : ICON_FAIL;
    btn.classList.toggle("log-copy-done", ok);
    btn.classList.toggle("log-copy-fail", !ok);
    clearTimeout(copyResetTimer);
    copyResetTimer = setTimeout(() => {
      btn.innerHTML = ICON_COPY;
      btn.classList.remove("log-copy-done", "log-copy-fail");
    }, 1500);
  });
  $("btn-clear-log").addEventListener("click", async () => {
    try {
      await App.ClearLog();
      logLines = [];
      renderLog();
    } catch (e) {
      await showModal({ icon: "warn", title: "操作失败", body: esc(String(e)) });
    }
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
  if (logLines.length > 2000) logLines.splice(0, logLines.length - 2000); // 硬上限
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
  // 尾随空格：CSS margin 不进复制文本，选中复制时需要真实分隔
  div.innerHTML = `<span class="t">${esc(line.time)} </span><span class="lv lv-${esc(line.level)}">${esc(line.level)} </span>${esc(line.msg)}`;
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
    if (!scrim.hidden) {
      // 防重入：已有模态时不再叠加（旧实例的键盘监听会把两个 promise
      // 一起 resolve 并提前隐藏遮罩）。-1 = Esc 语义，调用方按取消处理
      resolve(-1);
      return;
    }
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

// 空闲态图标：心电图（存活检测隐喻）；结果态沿用 ◌/✓/ⓘ/!/✕ 字形与配色
const ECG_ICON = '<svg class="ecg" viewBox="0 0 20 20" aria-hidden="true"><path d="M1.5 10H5.9L7.5 6.4L10.1 13.9L11.7 10H18.5"/></svg>';
// Tests always use current inputs and ignore responses for superseded inputs.
function bindConnTests() {
  document.querySelectorAll(".conn-dot").forEach((dot) => {
    const kind = dot.dataset.conn;
    const ids = kind === "llm" ? ["cfg-llm-url", "cfg-llm-key", "cfg-llm-model"] : ["cfg-mineru-token"];
    let version = 0;
    const message = document.createElement("div"); message.className = "conn-message";
    message.setAttribute("role", "status"); message.setAttribute("aria-live", "polite");
    const wrap = dot.parentElement, host = document.createElement("div");
    wrap.replaceWith(host); host.append(wrap, message);
    const set = (cls, label = "测试连接") => {
      dot.className = "conn-dot " + cls;
      dot.innerHTML = cls === "idle" ? ECG_ICON : cls === "spin" ? "◌" : cls === "ok" ? "✓" : cls === "info" ? "ⓘ" : cls === "bad" ? "✕" : "!";
      dot.title = label; dot.setAttribute("aria-label", label);
      message.textContent = cls === "idle" ? "" : label;
    };
    dot.addEventListener("click", async () => {
      if (dot.classList.contains("spin")) return;
      const mine = ++version;
      const values = ids.map(id => $(id).value);
      set("spin", "测试中…");
      try {
        const r = kind === "llm" ? await App.TestLLMWithConfig(values[0], values[1]) : await App.TestMinerUWithConfig(values[0]);
        if (mine !== version || values.some((v,i) => v !== $(ids[i]).value)) return;
        set(r.status === "ok" ? "ok" : r.status === "info" ? "info" : r.status === "warning" ? "warning" : "bad", r.message);
        localLog(r.status === "ok" || r.status === "info" ? "info" : "warn", (kind === "llm" ? "LLM：" : "MinerU：") + r.message);
      } catch {
        if (mine === version) set("bad", "连接检测未完成，请重试");
      }
    });
    ids.forEach(id => $(id).addEventListener("input", () => { ++version; set("idle"); }));
  });
}

// bindCheckUpdate 设置页「关于与更新」卡：手动检查 GitHub 最新版本，
// 状态机与连接测试同款（空闲→检查中→结果）；Release 页外链走系统浏览器。
function bindCheckUpdate() {
  const btn = $("btn-check-update");
  const out = $("update-result");
  const status = $("about-status");
  let releaseUrl = "";
  // 短状态行内展示（紧跟版本号）；「有新版本」可点击直达 Release 页
  status.addEventListener("click", () => {
    if (status.classList.contains("new") && releaseUrl) rt.BrowserOpenURL(releaseUrl);
  });
  btn.addEventListener("click", async () => {
    if (btn.disabled) return;
    btn.disabled = true;
    btn.textContent = "检查中…";
    status.hidden = true;
    out.hidden = true; // 清掉上一轮可能残留的失败提示（失败→成功切换场景）
    try {
      const info = await App.CheckUpdate();
      releaseUrl = info.url || "";
      if (info.has_update) {
        // 有新版本：仅行内短状态（可点击直达 Release 页），无独立结果行
        status.className = "about-status new";
        status.hidden = false;
        status.textContent = `🆕 有新版本 ${info.latest}`;
        localLog("info", `发现新版本 ${info.latest}`);
      } else {
        // 已是最新：一行短状态收尾
        status.className = "about-status ok";
        status.hidden = false;
        status.textContent = "✓ 已是最新";
        localLog("info", "已是最新版本");
      }
    } catch (e) {
      const raw = String(e);
      // 界面只显示用户可读的前缀（如「无法连接 GitHub（检查网络后重试）」）；
      // 约定：带细节的报错以「）: 」分隔可读部分与原始信息，完整细节进日志便于排查
      const sep = raw.indexOf("）: ");
      status.hidden = true;
      out.hidden = false;
      out.textContent = "检查失败：" + (sep >= 0 ? raw.slice(0, sep + 1) : raw);
      localLog("warn", "检查更新失败：" + raw);
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
  let modelVersion = 0;
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
      </div><div class="mp-list">${list}</div>`;
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
    const requestVersion = modelVersion;
    try {
      const mine = requestVersion;
      const url = $("cfg-llm-url").value, key = $("cfg-llm-key").value;
      const fetched = await App.FetchModelsWithConfig(url, key);
      if (mine !== modelVersion || url !== $("cfg-llm-url").value || key !== $("cfg-llm-key").value) { btn.disabled = false; btn.textContent = old; return; }
      models = fetched;
      localLog("info", `已拉取 ${models.length} 个上游模型`);
      btn.textContent = `✓ ${models.length} 个`;
      openPop();
    } catch (e) {
      if (requestVersion !== modelVersion) { btn.textContent = old; btn.disabled = false; return; }
      btn.textContent = "拉取失败";
      localLog("error", "拉取模型失败：" + e);
      setTimeout(() => (btn.textContent = old), 1600);
      btn.disabled = false;
      return;
    }
    setTimeout(() => { btn.textContent = old; btn.disabled = false; }, 1800);
  });
  ["cfg-llm-url", "cfg-llm-key"].forEach(id => $(id).addEventListener("input", () => {
    modelVersion++; models = []; closePop();
  }));
  input.addEventListener("input", () => { if (open) renderPop(input.value.trim()); });
  input.addEventListener("focus", () => { if (models.length && !open) openPop(); });
  document.addEventListener("pointerdown", (e) => {
    if (!open || Date.now() < armAt) return;
    if (e.target.closest("#model-pop") || e.target === input || e.target.closest("#btn-fetch-models")) return;
    closePop();
  });
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && open) closePop(); });
}

// 主题：默认浅色（light），选择存 localStorage；切换平滑过渡
function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  const tip = t === "light" ? "切换为深色" : "切换为浅色";
  $("btn-theme").title = tip;
  $("btn-theme").setAttribute("aria-label", tip);
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
    bindAutosave();
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
        else { if (!(await requireSettingsSaved())) return; await App.QueueStart(); }
        renderQueue();
      } catch (e) {
        localLog("error", "队列操作失败：" + e);
      }
    });
    rt.EventsOn("task:update", (t) => upsertTask(t));
    rt.EventsOn("queue:change", () => renderQueue());
    rt.EventsOn("log:line", (line) => appendLog(line));
    await loadSettings();
    initSettingsBaseline();
    await checkConfigRecovery();
    settingsReady = !settingsRecoveryBlocked;
    if (App.FrontendReady) await App.FrontendReady();
    renderQueue();
    const tail = await App.GetLogTail();
    logLines = tail.slice();
    renderLog();
    positionModePill();
    // 版本号单一来源：Go 侧 internal/version 经绑定给出；壳缺失时退化为 ?
    let VER = "?";
    try { VER = await App.Version(); } catch (e) { /* 无绑定时显示 ? */ }
    $("about-version").textContent = VER;
    sidebarVersion = VER;
    renderSidebarStatus();
  } catch (e) {
    // 初始化链断裂时状态栏会永远停在 index.html 的占位文案「引擎启动中…」，
    // 这里把真实错误亮出来，避免启动故障伪装成正常状态
    console.error("初始化失败", e);
    $("sidebar-status").innerHTML =
      `<span style="color:var(--bad)">●</span> 启动异常：` + esc(String(e)).slice(0, 120);
  }
});
