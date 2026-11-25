// GUI 预览模拟壳：在不启动 Wails 运行时的情况下渲染真实前端（视觉验收用）
window.__MOCK = true;
const _bs = String.fromCharCode(92);

// ?empty：空队列演示态（README 预览图用）
const EMPTY = new URLSearchParams(location.search).has("empty");
// ?fontnull：FontList 返回 null（Go nil 切片的 JSON 形态），回归 v1.0.1 修复
const FONTNULL = new URLSearchParams(location.search).has("fontnull");
// ?updatefail：CheckUpdate 抛错（reject），覆盖检查更新失败路径
const UPDATEFAIL = new URLSearchParams(location.search).has("updatefail");

const MOCK_TASKS = [
  {
    id: "T0", code: "K003", kind: "md",
    source_path: "D:" + _bs + "Demo" + _bs + "Library" + _bs + "K003_example.md",
    status: "等待", stage: "等待处理",
    done: 0, total: 0, failed: 0, refs_kept: 0,
    out_pdf: "", zh_md: "", work_dir: "", err: "",
  },
  {
    id: "T1", code: "L044", kind: "md",
    source_path: "D:" + _bs + "Demo" + _bs + "Library" + _bs + "L044_example.md",
    status: "翻译中", stage: "翻译中",
    done: 12, total: 34, failed: 0, refs_kept: 0,
    out_pdf: "", zh_md: "", work_dir: "", err: "",
  },
  {
    id: "T2", code: "L001", kind: "pdf",
    source_path: "D:" + _bs + "Demo" + _bs + "Library" + _bs + "L001_example.pdf",
    status: "完成", stage: "完成",
    done: 37, total: 37, failed: 0, refs_kept: 26,
    out_pdf: "D:" + _bs + "Demo" + _bs + "Library" + _bs + "L001" + _bs + "L001_zh.pdf",
    zh_md: "D:" + _bs + "Demo" + _bs + "Library" + _bs + "L001" + _bs + "L001_zh.md",
    work_dir: "D:" + _bs + "Demo" + _bs + "Library" + _bs + "L001", err: "",
  },
  {
    id: "T3", code: "L016", kind: "pdf",
    source_path: "D:" + _bs + "Demo" + _bs + "Library" + _bs + "L016_example.pdf",
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

// 出厂双预设文案与 internal/config 的 DefaultRules / GeneralRules 保持同源
// （前端「恢复默认」按预设名区分还原源：物理→MOCK_IRON，其余→MOCK_GENERAL）。
const MOCK_IRON = "你是一名物理学/学术文献翻译引擎。将用户消息中的 Markdown 学术文本完整、准确地翻译为流畅严谨的简体中文，严格遵守：\n\n1. 占位符与公式：⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码块、图片）必须原样保留，不得增删、改写或解读；独立成段的占位符保持原段落位置，行内占位符可随中文语序自然调整但须保持准确对应。不要擅自增删 $ / $$ 公式定界符。\n2. 忠实与单位：逐句完整翻译，不遗漏、不概括、不增补；按中文学术习惯合理调整长难句语序。首次出现的「全称（缩写）」保留英文缩写；数值后的国际单位与统计记号（如 nm、m/s、mg/kg、p < 0.05）保持原符号，不译成汉字单位。\n3. 引用与结构标签：Eq./Equation、Fig./FIG./Figure、Table/TABLE、Ref.、arXiv/DOI 及附录编号（如 Fig. S1、S IV）等交叉引用标记连同编号保留原样，不翻译标记词，也不在前面重复加「如图/见表」（写作「见 Fig. 1」而非「如图 Fig. 1」）；章节与习题标签统一中译（Chapter→第 N 章、Section/Sec.→第 N 节、Exercise→习题）。\n4. Markdown 与表格：保持标题层级（# 数量）、列表、引用块、链接、表格与原文一一对应；标题译文简洁忠实。链接仅译显示文字，URL 保持原样且 ] 与 ( 间不加空格；加粗/斜体紧贴中文引号或括号时将全角标点置于标记外侧。表格逐行保持竖线 |、列数、分隔行及单元格内 \\| 不变，单元格内不换行，不新增 <br> 等 HTML 标签；<sup>/<sub> 仅用于短角标或化学式，切勿将普通中文词句放入 <sup>/<sub> 内。\n5. 物理记号与代码：狄拉克记号（|00⟩、⟨01|）、希腊字母（Λ、Ω、ψ）、上下标变量、行内代码（反引号 `...`），以及无法识别的乱码字符均原样保留，不翻译、不删除、不猜测含义、不改写。\n6. 人名与专名：历史著名学者与定名概念统一采用学界通用中译（如 Schrödinger→薛定谔、von Neumann→冯·诺依曼、Bayes→贝叶斯）；正文引用的论文作者与作者栏保留原文姓氏（如 Smith et al.→Smith 等），英文姓名间保留半角逗号；无通用中译的专名保留原文。若遇参考文献条目，保留原文不译。\n7. 术语统一：若下方提供术语表，严格按表统一译名（含单复数与大小写变体）；未列出的专业术语采用该学科规范中文译名并保持全篇前后一致，不随意混用不同译法或中途夹杂英文原词。\n8. 只输出译文本身。不要任何解释、前言、结语、思考过程或外层代码围栏包裹。";

const MOCK_GENERAL = "你是一名学术文献翻译引擎。将用户消息中的 Markdown 学术文本完整、准确地翻译为流畅严谨的简体中文，严格遵守：\n\n1. 占位符与公式：⟦F1⟧、⟦F2⟧ 这类 ⟦F数字⟧ 占位符（代表公式、代码块、图片）必须原样保留，不得增删、改写或解读；独立成段的占位符保持原段落位置，行内占位符可随中文语序自然调整但须保持准确对应。不要擅自增删 $ / $$ 公式定界符。\n2. 忠实与单位：逐句完整翻译，不遗漏、不概括、不增补；按中文学术习惯合理调整长难句语序。首次出现的「全称（缩写）」保留英文缩写；数值后的国际单位与统计记号（如 nm、m/s、mg/kg、p < 0.05）保持原符号，不译成汉字单位。\n3. 引用与结构标签：Eq./Equation、Fig./FIG./Figure、Table/TABLE、Ref.、arXiv/DOI 及附录编号（如 Fig. S1、S IV）等交叉引用标记连同编号保留原样，不翻译标记词，也不在前面重复加「如图/见表」（写作「见 Fig. 1」而非「如图 Fig. 1」）；章节与习题标签统一中译（Chapter→第 N 章、Section/Sec.→第 N 节、Exercise→习题）。\n4. Markdown 与表格：保持标题层级（# 数量）、列表、引用块、链接、表格与原文一一对应；标题译文简洁忠实。链接仅译显示文字，URL 保持原样且 ] 与 ( 间不加空格；加粗/斜体紧贴中文引号或括号时将全角标点置于标记外侧。表格逐行保持竖线 |、列数、分隔行及单元格内 \\| 不变，单元格内不换行，不新增 <br> 等 HTML 标签；<sup>/<sub> 仅用于短角标或化学式，切勿将普通中文词句放入 <sup>/<sub> 内。\n5. 学科记号与代码：行内代码（反引号 `...`）、代码标识符与文件路径、数学/物理记号（|00⟩、⟨01|、希腊字母、上下标变量）、化学式（H<sub>2</sub>O）、生物拉丁学名与基因名斜体（*E. coli*、*BRCA1*），以及无法识别的乱码字符均原样保留，不翻译、不删除、不猜测含义、不改写。\n6. 人名与专名：历史著名学者与定名概念统一采用学界通用中译（如 Schrödinger→薛定谔、von Neumann→冯·诺依曼、Bayes→贝叶斯）；正文引用的论文作者与作者栏保留原文姓氏（如 Smith et al.→Smith 等），英文姓名间保留半角逗号；无通用中译的专名保留原文。若遇参考文献条目，保留原文不译。\n7. 术语统一：若下方提供术语表，严格按表统一译名（含单复数与大小写变体）；未列出的专业术语采用该学科规范中文译名并保持全篇前后一致，不随意混用不同译法或中途夹杂英文原词。\n8. 只输出译文本身。不要任何解释、前言、结语、思考过程或外层代码围栏包裹。";

const MOCK_CFG = {
  mineru: { token: "eyJ…demo", model_version: "vlm", language: "" },
  llm: { base_url: "https://open.bigmodel.cn/api/paas/v4", api_key: "sk-demo", model: "glm-4.6", concurrency: 3 },
  rules: {
    active: "物理",
    presets: [
      { name: "物理", text: MOCK_IRON },
      { name: "通用学术", text: MOCK_GENERAL },
    ],
    user_global: "",
  },
  glossary: [
    { en: "Schrödinger equation", zh: "薛定谔方程" },
  ],
  export: { bilingual: false, toc: false, two_column: false, user_font: "", output_dir: "" },
  translate: { skip_references: true },
  repair: { spelling: true },
  user_fonts: [],
};

// Persist a detached mock configuration across reloads, with injectable failures/delays.
const cloneMock = v => JSON.parse(JSON.stringify(v));
const savedMock = sessionStorage.getItem("literature-settings-mock");
if (savedMock) Object.assign(MOCK_CFG, JSON.parse(savedMock));
window.__settingsMock = {failSaves:false, delay:0, calls:[], closed:false, ready:false};
const mockState = window.__settingsMock;
 mockState.corrupt = new URLSearchParams(location.search).has("corrupt");
 mockState.hasBackup = true;
const mergeMock = (a,b) => { const out = {...a}; for (const [k,v] of Object.entries(b)) out[k] = v && typeof v === "object" && !Array.isArray(v) ? mergeMock(a[k] || {},v) : v; return out; };
const waitMock = () => new Promise(r => setTimeout(r,mockState.delay));
const cleanMock = s => String(s || "").trim().replace(/^"|"$/g, "").replace(/^bearer\s+/i, "").trim();
const mockApp = {
  QueueList: async () => TASKS,
  QueueAddPDF: async () => [],
  QueueAddMD: async (paths, mode) => {
    for (const p of paths || []) {
      const zh = /中文|_zh/.test(p) || mode === "export";
      TASKS.push({id:"T"+Date.now()+Math.random(),code:"X001",kind:mode === "export" || zh ? "export":"md",mode:mode || "auto",source_path:p,status:"等待",stage:"待开始",done:0,total:0,failed:0,refs_kept:0,out_pdf:"",zh_md:"",work_dir:"",err:""});
    }
    return [];
  },
  QueueRetry: async () => null,
  QueueRemove: async (id) => {
    const idx = TASKS.findIndex((t) => t.id === id);
    if (idx >= 0) TASKS.splice(idx, 1);
  },
  QueueClearFinished: async () => [],
  GetConfig: async () => cloneMock(MOCK_CFG),
  GetConfigLoadState: async () => ({ready:!mockState.corrupt,message:mockState.corrupt ? "配置读取失败，原文件已保留" : "",has_backup:!!mockState.hasBackup}),
  RecoverConfig: async action => {
    mockState.calls.push({method:"recover",action});
    if (mockState.recoveryFails || (action==="reload" && mockState.corrupt)) throw new Error("模拟恢复失败");
    if(action==="restore_backup" && !mockState.hasBackup) throw new Error("没有有效备份");
    if (mockState.recoveryConfig) Object.assign(MOCK_CFG,cloneMock(mockState.recoveryConfig));
    mockState.corrupt=false;
    sessionStorage.setItem("literature-settings-mock",JSON.stringify(MOCK_CFG));
    return cloneMock(MOCK_CFG);
  },
  UpdateSettings: async patch => {
    const captured = cloneMock(patch); mockState.calls.push({method:"save",patch:captured});
    await waitMock();
    if (mockState.failSaves || mockState.corrupt) throw new Error("模拟磁盘写入失败");
    const next = mergeMock(MOCK_CFG,captured);
    if (next.glossary_rows) next.glossary = next.glossary_rows.filter(g=>g.en.trim() && g.zh.trim()).map(g=>({en:g.en.trim(),zh:g.zh.trim()}));
    next.mineru.token = cleanMock(next.mineru.token); next.llm.api_key = cleanMock(next.llm.api_key);
    next.llm.base_url = next.llm.base_url.trim().replace(/\/+$/, "");
    Object.assign(MOCK_CFG,next);
    sessionStorage.setItem("literature-settings-mock",JSON.stringify(MOCK_CFG));
    return cloneMock(MOCK_CFG);
  },
  TestLLMWithConfig: async (url,key) => {
    mockState.calls.push({method:"llm",url,key}); await waitMock();
    return {status:"ok",message:"模型列表接口连通 · 15 个模型",model_count:15};
  },
  TestMinerUWithConfig: async token => {
    const result=cloneMock(mockState.mineruResult || {status:"ok",message:"Token 有效，服务连通 · 解析功能待验证"});
    mockState.calls.push({method:"mineru",token}); await waitMock();
    return result;
  },
  DefaultGlossary: async () => cloneMock([{en:"Schrödinger equation",zh:"薛定谔方程"}]),
  ResetGlossaryToDefault: async () => mockApp.UpdateSettings({glossary_rows:[{en:"Schrödinger equation",zh:"薛定谔方程"}]}),
  FetchModelsWithConfig: async (url,key) => {
    const models=cloneMock(mockState.models || ["glm-4.6","glm-5.3-flash","glm-5.3","deepseek-v3"]);
    mockState.calls.push({method:"models",url,key}); await waitMock(); return models;
  },
  Version: async () => "v1.4.3",
  CheckUpdate: async () => UPDATEFAIL
    ? Promise.reject("无法连接 GitHub（检查网络后重试）: Get \"https://api.github.com/repos/Pet-Max/Literature-Translation/releases/latest\": dial tcp 203.0.113.9:443: i/o timeout")
    : {has_update:false,current:"v1.4.3",latest:"v1.4.3",url:""},
  IronRules: async () => MOCK_IRON,
  GeneralRules: async () => MOCK_GENERAL,
  PickFiles: async () => [],
  QueueState: async () => ({running:false,paused:true,pending:EMPTY ? 0:2}),
  QueueStart: async () => { mockState.calls.push({method:"start",config:cloneMock(MOCK_CFG)}); },
  QueuePause: async () => true,
  QueuePauseNow: async () => { mockState.calls.push({method:"pauseNow"}); },
  QueueStartOne: async () => true,
  FontList: async () => FONTNULL ? null : cloneMock(MOCK_CFG.user_fonts),
  FontAdd: async () => {
    mockState.calls.push({method:"font-import"}); await waitMock();
    if (mockState.failSaves) throw new Error("模拟字体写入失败");
    const uf={internal_name:"MockFont",display_name:"Mock Font",file_name:"mock.ttf"};
    MOCK_CFG.user_fonts.push(uf);
    sessionStorage.setItem("literature-settings-mock",JSON.stringify(MOCK_CFG));
    return cloneMock(uf);
  },
  SetFontPreset: async (preset,uf) => { if (mockState.failSaves) throw new Error("模拟字体保存失败"); await mockApp.UpdateSettings({export:{font_preset:preset,user_font:uf}}); },
  FontRemove: async (internalName) => {
    mockState.calls.push({method:"font-remove",internalName}); await waitMock();
    if (mockState.failSaves) throw new Error("模拟删除字体失败");
    MOCK_CFG.user_fonts = (MOCK_CFG.user_fonts || []).filter(f => f.internal_name !== internalName);
    if (MOCK_CFG.export.user_font === internalName) {
      MOCK_CFG.export.user_font = "";
      MOCK_CFG.export.font_preset = "lxgw";
    }
    sessionStorage.setItem("literature-settings-mock",JSON.stringify(MOCK_CFG));
    return cloneMock(MOCK_CFG);
  },
  PickFontFile: async () => "mock.ttf",
  PickDirectory: async () => mockState.pickedDir ?? "D:" + _bs + "Demo" + _bs + "CustomLibrary",
  FrontendReady: async () => {mockState.ready=true;},
  FinishSettingsClose: async () => {mockState.closed=true;},
  GetLogTail: async () => MOCK_LOGS,
  ClearLog: async () => {MOCK_LOGS.splice(0);},
  OpenPath: async () => true,
};
window.go = {app:{App:mockApp}};
const rtState={maximised:false};
window.runtime={
  EventsOn:(name,cb)=>{(window.__evt=window.__evt || {})[name]=cb; if(name === "task:update" && !EMPTY) setTimeout(()=>{MOCK_TASKS[0].done=15;cb(MOCK_TASKS[0]);},1200);},
  OnFileDrop:()=>{},BrowserOpenURL:()=>{},WindowMinimise:()=>{},
  WindowToggleMaximise:()=>{rtState.maximised=!rtState.maximised;},WindowIsMaximised:async()=>rtState.maximised,
  Quit:()=>{ window.__evt?.["settings:before-close"]?.(); },
};
