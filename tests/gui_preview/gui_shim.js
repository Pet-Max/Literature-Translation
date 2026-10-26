// GUI 预览模拟壳：在不启动 Wails 运行时的情况下渲染真实前端（视觉验收用）
window.__MOCK = true;
const _bs = String.fromCharCode(92);

// ?empty：空队列演示态（README 预览图用）
const EMPTY = new URLSearchParams(location.search).has("empty");
// ?fontnull：FontList 返回 null（Go nil 切片的 JSON 形态），回归 v1.0.1 修复
const FONTNULL = new URLSearchParams(location.search).has("fontnull");

const MOCK_TASKS = [
  {
    id: "T0", code: "K003", kind: "md",
    source_path: "D:\文献全文\04_候选待核验\K003_Using the Environment to Understand non-Markovian Open Quantum Systems.md",
    status: "等待", stage: "等待处理",
    done: 0, total: 0, failed: 0, refs_kept: 0,
    out_pdf: "", zh_md: "", work_dir: "", err: "",
  },
  {
    id: "T1", code: "L044", kind: "md",
    source_path: "D:" + _bs + "文献全文" + _bs + "03_方法与边界" + _bs + "L044_Coherent heat transfer leads to genuine quantum enhancement_2404.05799v2.md",
    status: "翻译中", stage: "翻译中",
    done: 12, total: 34, failed: 0, refs_kept: 0,
    out_pdf: "", zh_md: "", work_dir: "", err: "",
  },
  {
    id: "T2", code: "L001", kind: "pdf",
    source_path: "D:" + _bs + "文献全文" + _bs + "01_直接竞争" + _bs + "L001_Full counting statistics_2212.11307v1.pdf",
    status: "完成", stage: "完成",
    done: 37, total: 37, failed: 0, refs_kept: 26,
    out_pdf: "D:" + _bs + "翻译验证" + _bs + "L001_zh.pdf",
    zh_md: "D:" + _bs + "翻译验证" + _bs + "L001_zh.md",
    work_dir: "D:" + _bs + "翻译验证", err: "",
  },
  {
    id: "T3", code: "L016", kind: "pdf",
    source_path: "D:" + _bs + "文献全文" + _bs + "02_核心必读" + _bs + "L016_Example paper_2023v1.pdf",
    status: "失败", stage: "解析中",
    done: 0, total: 0, failed: 0, refs_kept: 0,
    out_pdf: "", zh_md: "", work_dir: "",
    err: "MinerU Token 无效（请到 mineru.net「API 管理」页重新生成并核对）",
  },
];

const TASKS = EMPTY ? [] : MOCK_TASKS;

const MOCK_LOGS = [
  { time: "10:41:02", level: "info", msg: "MinerU 精准解析（vlm 模式）…" },
  { time: "10:42:37", level: "info", msg: "解析完成，用时 88 秒" },
  { time: "10:44:12", level: "warn", msg: "第 7 段 $ 配对数 8 ≠ 10，已将错误反馈模型（第 1 次）" },
  { time: "10:44:19", level: "info", msg: "第 7 段重试通过 ✓" },
  { time: "10:46:03", level: "error", msg: "第 21 段连续 3 次校验失败，已标记待人工处理" },
  { time: "10:47:55", level: "info", msg: "译文 33/34 段完成，等待失败段处理后统一排版" },
];

const MOCK_IRON = "你是一名物理学/学术文献翻译引擎。把用户消息中的 Markdown 学术文本翻译成简体中文，严格遵守：\n\n1. 只翻译自然语言文字；⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码、图片、表格）必须原样保留：一个不能少、一个不能改、位置不能移动，也不要试图翻译或解读占位符内容。\n2. Eq. (5)、Fig. 3、Table 1、Ref. [12]、arXiv:2306.00417、附录编号 S IV 等引用编号保留原样，不要翻译 Eq./Fig. 等标记。\n3. Markdown 结构保持不变：标题层级（# 数量）、列表、引用块、加粗斜体、链接与原文一一对应；标题译文务必简短。\n4. 专有名词、人名、变量名（如 PSD、Langevin、Sz(ω) 中的记号）按学界惯例处理：有通用中译的用中译，没有的保留原文。\n5. 术语严格按术语表统一译名。\n6. 只输出译文本身。不要任何解释、前言、结语、代码围栏包裹。";

const MOCK_CFG = {
  mineru: { token: "eyJ…demo", model_version: "vlm" },
  llm: { base_url: "https://open.bigmodel.cn/api/paas/v4", api_key: "sk-demo", model: "glm-4.6", concurrency: 3 },
  rules: {
    active: "默认",
    presets: [
      { name: "默认", text: MOCK_IRON },
      { name: "综述专用", text: MOCK_IRON + "\n\n这是综述文献：方法名保留英文；每节末尾用一句话概括作者观点。" },
    ],
    user_global: "",
  },
  glossary: [
    { en: "Langevin equation", zh: "朗之万方程" },
  ],
  export: { bilingual: false, toc: false, two_column: false, user_font: "", output_dir: "" },
  translate: { skip_references: true },
  repair: { spelling: true },
  user_fonts: [],
};

window.go = { app: { App: {
  QueueList: async () => TASKS,
  QueueAddPDF: async (paths, mode) => [],
  QueueAddMD: async (paths, mode) => {
    for (const p of paths || []) {
      const zh = /中文|_zh/.test(p) || mode === "export";
      TASKS.push({
        id: "T" + Date.now() + Math.random().toString(36).slice(2, 5),
        code: "X001", kind: mode === "export" || zh ? "export" : "md",
        mode: mode || "auto",
        source_path: p, status: "等待", stage: "待开始",
        done: 0, total: 0, failed: 0, refs_kept: 0,
        out_pdf: "", zh_md: "", work_dir: "", err: "",
      });
    }
    return [];
  },
  QueueRetry: async () => null,
  QueueClearFinished: async () => [],
  TestLLM: async () => 15,
  TestMinerU: async () => "Token 有效",
  GetConfig: async () => MOCK_CFG,
  Version: async () => "v1.0.2",
  CheckUpdate: async () => ({ has_update: false, current: "v1.0.2", latest: "v1.0.2", url: "", notes: "" }),
  SaveConfig: async () => true,
  IronRules: async () => MOCK_IRON,
  PickFiles: async () => [],
  FetchModels: async () => ["glm-4.6","glm-5.3-flash","glm-5.3","deepseek-v3"],
  QueueState: async () => ({ running: false, paused: true, pending: EMPTY ? 0 : 2 }),
  QueueStart: async () => true,
  QueuePause: async () => true,
  QueueStartOne: async (id) => true,
  SaveRules: async (active, presets) => { MOCK_CFG.rules = { active, presets, user_global: "" }; return true; },
  FontList: async () => (FONTNULL ? null : []),
  FontAdd: async () => ({}),
  SetFontPreset: async (preset, uf) => { MOCK_CFG.export.font_preset = preset; MOCK_CFG.export.user_font = uf; return true; },
  PickFontFile: async () => "",
  GetLogTail: async () => MOCK_LOGS,
  ClearLog: async () => { MOCK_LOGS.splice(0); },
  OpenPath: async () => true,
}}};

const rtState = { maximised: false };

window.runtime = {
  EventsOn: (name, cb) => {
    (window.__evt = window.__evt || {})[name] = cb;
    if (name === "task:update" && !EMPTY) {
      // 模拟一次进度推进，展示动态卡片
      setTimeout(() => { MOCK_TASKS[0].done = 15; cb(MOCK_TASKS[0]); }, 1200);
    }
  },
  OnFileDrop: () => {},
  BrowserOpenURL: () => {},
  WindowMinimise: () => {},
  WindowToggleMaximise: () => { rtState.maximised = !rtState.maximised; },
  WindowIsMaximised: async () => rtState.maximised,
  Quit: () => {},
};
