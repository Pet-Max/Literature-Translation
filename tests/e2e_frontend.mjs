// 前端组件 E2E：用 Playwright 驱动真实前端（frontend/dist）+ gui_shim mock 壳。
// 服务器见 overlay_server.mjs（overlay 方式直供 dist 并注入 shim）。
// 运行：node tests/e2e_frontend.mjs   （可选 CHROME_PATH 指定浏览器可执行文件）
import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";
import { startOverlayServer, PREVIEW_PORT } from "./overlay_server.mjs";

const require = createRequire(import.meta.url);
const pw = require("playwright-core");

// ------------------------------------------------ 浏览器可执行文件探测
function findBrowser() {
  const cands = [
    process.env.CHROME_PATH,
    "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",
    "C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
    path.join(process.env.LOCALAPPDATA || "", "Google\\Chrome\\Application\\chrome.exe"),
    "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe",
    "C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe",
  ].filter(Boolean);
  for (const c of cands) { try { if (fs.existsSync(c)) return c; } catch { /* 忽略 */ } }
  return null;
}

// ------------------------------------------------ 断言助手
let passed = 0, failed = 0;
function ok(name, cond, detail) {
  if (cond) { passed++; console.log(`  ✓ ${name}`); }
  else { failed++; console.log(`  ✗ ${name}${detail ? " — " + detail : ""}`); }
}
async function until(fn, timeout = 5000, step = 100) {
  const t0 = Date.now();
  for (;;) {
    let v;
    try { v = await fn(); } catch { v = null; }
    if (v) return v;
    if (Date.now() - t0 > timeout) return null;
    await new Promise((r) => setTimeout(r, step));
  }
}

// ------------------------------------------------ 主流程
const server = await startOverlayServer(PREVIEW_PORT);
const browser = await pw.chromium.launch({
  executablePath: findBrowser() || undefined,
  headless: true,
});
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
const pageErrors = [];
page.on("pageerror", (e) => pageErrors.push(String(e)));
// 每次导航前清空主题存档，保证首屏是默认深色
await page.addInitScript(() => { try { localStorage.clear(); } catch {} });

const PAGE_URL = `http://127.0.0.1:${PREVIEW_PORT}/index.html`;
await page.goto(PAGE_URL, { waitUntil: "load" });

// ============ [1] 初始渲染 ============
console.log("\n[1] 初始渲染");
{
  ok("页面标题", (await page.title()) === "Literature Translation", await page.title());
  ok("品牌名 Literature Translation", (await page.locator(".brand-name").textContent()) === "Literature Translation");
  ok("品牌图标存在", await page.locator(".brand-ico").count() === 1);
  ok("拖放区可见", await page.locator("#dropzone").isVisible());
  ok("拖放区提示文案", (await page.locator("#dropzone .dz-hint").textContent()).includes("PDF 或 MD"));
  await until(() => page.locator("#queue-list .task").count().then((n) => n === 4));
  ok("队列渲染 4 张任务卡", (await page.locator("#queue-list .task").count()) === 4);
  const mockPaths = await page.evaluate(() => MOCK_TASKS.flatMap(task =>
    ["source_path", "out_pdf", "zh_md", "work_dir"].map(key => task[key]).filter(Boolean)));
  ok("模拟路径使用中性 Windows 目录", mockPaths.every(value => value.startsWith("D:\\Demo\\Library\\")));
  ok("模拟路径不含控制字符", mockPaths.every(value => !/[\u0000-\u001f]/.test(value)));
  ok("任务计数文案", (await page.locator("#queue-count").textContent()) === "4 个任务");
  ok("默认模式=自动", await page.locator('.mode-btn[data-mode="auto"]').evaluate((el) => el.classList.contains("active")));
  await until(() => page.locator("#sidebar-status").textContent().then((t) => t.includes("v1.0.5")));
  ok("版本状态显示已配置", (await page.locator("#sidebar-status").textContent()).includes("已配置"));
}

// ============ [2] 双主题 ============
console.log("\n[2] 双主题");
{
  const lightBg = await page.locator("body").evaluate((el) => getComputedStyle(el).backgroundColor);
  ok("浅色背景 rgb(245,245,247)（默认）", lightBg === "rgb(245, 245, 247)", lightBg);
  ok("data-theme=light", (await page.locator("html").getAttribute("data-theme")) === "light");
  await page.click("#btn-theme");
  await until(() => page.locator("body").evaluate((el) => getComputedStyle(el).backgroundColor).then((c) => c === "rgb(12, 14, 19)"));
  const darkBg = await page.locator("body").evaluate((el) => getComputedStyle(el).backgroundColor);
  ok("深色背景 rgb(12,14,19)", darkBg === "rgb(12, 14, 19)", darkBg);
  ok("主题持久化", (await page.evaluate(() => localStorage.getItem("literature-theme"))) === "dark");
  const sideBg = await page.locator(".sidebar").evaluate((el) => getComputedStyle(el).backgroundColor);
  ok("侧栏与主区背景区分", sideBg !== darkBg, `sidebar=${sideBg} body=${darkBg}`);
  await page.click("#btn-theme"); // 切回浅色，后续断言基线一致
  await until(() => page.locator("html").getAttribute("data-theme").then((t) => t === "light"));
}

// ============ [3] 设置视图 ============
console.log("\n[3] 设置视图");
{
  await page.click('.nav-item[data-view="settings"]');
  await until(() => page.locator("#view-settings").evaluate((el) => el.classList.contains("active")));
  ok("设置视图激活", await page.locator("#view-settings.active").count() === 1);
  ok("全局保存栏已移除", await page.locator("#btn-save-config,.save-bar").count() === 0);
  await page.locator("#cfg-bilingual").check({force:true});
  await until(() => page.locator("#save-layout").textContent().then(t=>t.includes("已保存")));
  ok("开关自动保存", await page.evaluate(()=>MOCK_CFG.export.bilingual) === true);

  // 开关圆角：.switch i 有 5 个（5 个 toggle-row），取第一个断言（strict 模式下必须 first()）
  const sw = await page.locator(".switch i").first().evaluate((el) => getComputedStyle(el).borderRadius);
  ok("开关胶囊圆角 999px", sw === "999px", sw);

  // 连接测试小图标：点 → mock 立即返回 → ✓
  await page.click('.conn-dot[data-conn="llm"]');
  await until(() => page.locator('.conn-dot[data-conn="llm"]').getAttribute("class").then((c) => c.includes("ok")));
  ok("LLM 连接测试 ✓", (await page.locator('.conn-dot[data-conn="llm"]').textContent()) === "✓");
  ok("连通提示含模型数", (await page.locator('.conn-dot[data-conn="llm"]').getAttribute("title")).includes("15"));
  // 输入改动 → 状态重置空闲
  await page.fill("#cfg-llm-url", "https://example.com/v1");
  await until(() => page.locator('.conn-dot[data-conn="llm"]').getAttribute("class").then((c) => c.includes("idle")));
  ok("输入改动重置连接状态", (await page.locator('.conn-dot[data-conn="llm"]').textContent()) === "⇄");

  // 拉取模型弹层
  await page.click("#btn-fetch-models");
  await until(() => page.locator("#model-pop").evaluate((el) => !el.hidden));
  ok("模型弹层打开", await page.locator("#model-pop").isVisible());
  ok("弹层标题计数", (await page.locator("#model-pop .mp-head").textContent()).includes("共 4 个模型"));
  ok("模型条目 4 条", (await page.locator("#model-pop .mp-item").count()) === 4);
  await page.locator("#model-pop .mp-item").first().click();
  await until(() => page.locator("#model-pop").evaluate((el) => el.hidden));
  ok("选中后弹层收起", await page.locator("#model-pop").isHidden());
  ok("模型名回填", (await page.locator("#cfg-llm-model").inputValue()) === "glm-4.6");
  // 再次打开 → Esc 收起
  await page.click("#btn-fetch-models");
  await until(() => page.locator("#model-pop").evaluate((el) => !el.hidden));
  await page.keyboard.press("Escape");
  await until(() => page.locator("#model-pop").evaluate((el) => el.hidden));
  ok("Esc 收起弹层", await page.locator("#model-pop").isHidden());

  // 关于与更新卡
  ok("关于与更新卡按钮", await page.locator("#btn-check-update").isVisible());
  await page.click("#btn-check-update");
  await until(() => page.locator("#about-status").evaluate((el) => !el.hidden));
  ok("检查更新行内状态=已是最新", (await page.locator("#about-status").textContent())=== "✓ 已是最新");
  ok("检查成功时无独立结果行", await page.locator("#update-result").isHidden());
  ok("当前版本行填充", (await page.locator("#about-version").textContent()).includes("v1.0.5"));
}

// ============ [4] 日志视图 ============
console.log("\n[4] 日志视图");
{
  await page.click('.nav-item[data-view="logs"]');
  await until(() => page.locator("#view-logs").evaluate((el) => el.classList.contains("active")));
  ok("日志视图激活", await page.locator("#view-logs.active").count() === 1);
  const logCount0 = await until(() => page.locator("#log-panel > div[data-lv]").count().then((n) => (n >= 6 ? n : null)));
  ok("初始日志 ≥6 条（mock 6 条 + 设置页操作产生的本地日志）", (logCount0 || 0) >= 6, String(logCount0));
  await page.click('.log-filter[data-lv="warn"]');
  await until(() => page.locator("#log-panel > div[data-lv]").count().then((n) => n === 1));
  const warnLv = await page.locator("#log-panel > div[data-lv]").first().getAttribute("data-lv");
  ok("警告筛选只剩 1 条", warnLv === "warn", `data-lv=${warnLv}`);
  await page.click('.log-filter[data-lv="all"]');
  await page.click("#btn-clear-log");
  await until(() => page.locator("#log-panel .log-empty").count().then((n) => n === 1));
  ok("清空后显示暂无日志", (await page.locator("#log-panel .log-empty").textContent()).includes("暂无日志"));
}

// ============ [5] 自绘弹窗（删除术语确认 / Esc 关闭） ============
console.log("\n[5] 自绘弹窗");
{
  await page.click('.nav-item[data-view="settings"]');
  await until(() => page.locator("#view-settings").evaluate((el) => el.classList.contains("active")));
  const rows = () => page.locator("#glossary-box .g-row:not(.head)").count();
  ok("术语表 1 行示例", (await rows()) === 1);

  await page.locator(".g-del").first().click();
  await until(() => page.locator("#modal-scrim").evaluate((el) => !el.hidden));
  ok("弹窗出现", await page.locator("#modal-scrim").isVisible());
  ok("弹窗标题", (await page.locator("#modal-title").textContent()) === "删除这条术语？");
  ok("弹窗正文含术语", (await page.locator("#modal-body").textContent()).includes("Langevin equation"));
  ok("按钮为 取消/删除", (await page.locator("#modal-btns .btn").allTextContents()).join("/") === "取消/删除");

  await page.keyboard.press("Escape");
  await until(() => page.locator("#modal-scrim").evaluate((el) => el.hidden));
  ok("Esc 关闭弹窗", await page.locator("#modal-scrim").isHidden());
  ok("取消后行数不变", (await rows()) === 1);

  await page.locator(".g-del").first().click();
  await until(() => page.locator("#modal-scrim").evaluate((el) => !el.hidden));
  await page.locator('#modal-btns .btn.danger').click();
  await until(() => rows().then((n) => n === 0));
  ok("确认删除后行数 0", (await rows()) === 0);
  ok("删除后弹窗关闭", await page.locator("#modal-scrim").isHidden());
}

// ============ [6] 公式预览（KaTeX 幂等） ============
console.log("\n[6] 公式预览");
{
  const texCount = await page.locator("#font-preview .tex").count();
  ok("预览公式 3 个", texCount === 3, String(texCount));
  await until(() => page.locator("#font-preview .tex .katex").count().then((n) => n === 3));
  ok("KaTeX 全部渲染", (await page.locator("#font-preview .tex .katex").count()) === 3);
  const doneFlags = await page.locator("#font-preview .tex").evaluateAll((els) => els.map((e) => e.dataset.done));
  ok("幂等守卫标记", doneFlags.every((f) => f === "1"), doneFlags.join(","));

  // 切字体：预览字体栈变化，KaTeX 不重渲染（不增殖）
  const fontBefore = await page.locator("#font-preview").evaluate((el) => getComputedStyle(el).fontFamily);
  await page.selectOption("#cfg-font", "yahei");
  await page.waitForTimeout(200);
  const fontAfter = await page.locator("#font-preview").evaluate((el) => getComputedStyle(el).fontFamily);
  ok("切字体后字体栈变化", fontBefore !== fontAfter);
  ok("KaTeX 不增殖（仍 3 个）", (await page.locator("#font-preview .tex .katex").count()) === 3);
  await page.selectOption("#cfg-font", "lxgw"); // 还原默认
}

// ============ [7] 回任务视图 + 控制台错误 ============
console.log("\n[7] 回任务视图");
{
  await page.click('.nav-item[data-view="tasks"]');
  await until(() => page.locator("#view-tasks").evaluate((el) => el.classList.contains("active")));
  ok("任务视图激活", await page.locator("#view-tasks.active").count() === 1);
  ok("拖放区仍可见", await page.locator("#dropzone").isVisible());
  // 自绘标题栏：最大化按钮随窗口状态切换图标（Windows 惯例）
  ok("初始为最大化图标 □", (await page.locator("#win-max.restore").count()) === 0);
  await page.click("#win-max");
  const r1 = await until(() => page.locator("#win-max.restore").count().then((n) => (n === 1 ? n : null)));
  ok("最大化后切还原图标 ❐", r1 === 1);
  await page.click("#win-max");
  const r2 = await until(() => page.locator("#win-max.restore").count().then((n) => (n === 0 ? "cleared" : null)));
  ok("还原后切回 □", r2 === "cleared");
  ok("无 JS 运行时错误", pageErrors.length === 0, pageErrors.join(" | "));
}

// ============ [8] FontList→null 回归（Go nil 切片曾炸断整条初始化链） ============
console.log("\n[8] FontList→null 回归");
{
  const page2 = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errors2 = [];
  page2.on("pageerror", (e) => errors2.push(String(e)));
  await page2.goto(PAGE_URL + "?fontnull", { waitUntil: "load" });
  // 修复后 null 按空列表处理：应用照常走完初始化链，状态栏到达「已配置」
  await until(() => page2.locator("#sidebar-status").textContent().then((t) => t.includes("v1.0.5")));
  const st2 = await page2.locator("#sidebar-status").textContent();
  ok("null 字体列表下状态正常", st2.includes("已配置"), st2);
  ok("状态栏无「启动中/异常」残留", !st2.includes("启动中") && !st2.includes("异常"), st2);
  ok("规则卡仍渲染", (await page2.locator("#rule-list .rule-card").count()) === 2);
  ok("术语表仍渲染", (await page2.locator("#glossary-box .g-row:not(.head)").count()) === 1);
  ok("null 页无 JS 运行时错误", errors2.length === 0, errors2.join(" | "));
  await page2.close();
}

// ============ [9] 检查更新失败路径（?updatefail：CheckUpdate reject） ============
console.log("\n[9] 检查更新失败路径");
{
  const page3 = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  await page3.addInitScript(() => { try { localStorage.clear(); } catch {} });
  await page3.goto(PAGE_URL + "?updatefail", { waitUntil: "load" });
  await until(() => page3.locator("#sidebar-status").textContent().then((t) => t.includes("v1.0.5")));
  await page3.click('.nav-item[data-view="settings"]');
  await until(() => page3.locator("#view-settings").evaluate((el) => el.classList.contains("active")));
  await page3.click("#btn-check-update");
  await until(() => page3.locator("#update-result").evaluate((el) => !el.hidden));
  ok("失败时行内状态不出现", await page3.locator("#about-status").isHidden());
  const errText = await page3.locator("#update-result").textContent();
  ok("失败提示只含可读前缀（无 IP/URL 细节）", errText.includes("无法连接 GitHub") && !errText.includes("dial tcp") && !errText.includes("https://"), errText.trim());
  // 失败→成功切换：换到正常壳页面点击成功后，上一轮残留的错误行必须消失
  const page4 = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  await page4.addInitScript(() => { try { localStorage.clear(); } catch {} });
  await page4.goto(PAGE_URL, { waitUntil: "load" });
  await until(() => page4.locator("#sidebar-status").textContent().then((t) => t.includes("v1.0.5")));
  await page4.click('.nav-item[data-view="settings"]');
  await until(() => page4.locator("#view-settings").evaluate((el) => el.classList.contains("active")));
  // 先人为暴露一个"上轮失败残留"，再点成功，验证 stale 清理
  await page4.evaluate(() => {
    const out = document.querySelector("#update-result");
    out.hidden = false;
    out.textContent = "检查失败：无法连接 GitHub（检查网络后重试）";
  });
  await page4.click("#btn-check-update");
  await until(() => page4.locator("#about-status").evaluate((el) => !el.hidden));
  ok("失败→成功后错误行被清除", await page4.locator("#update-result").isHidden());
  ok("成功后行内状态=已是最新", (await page4.locator("#about-status").textContent())=== "✓ 已是最新");
  await page3.close();
  await page4.close();
}

// ============ [10] 自动保存、草稿、时序与退出回归 ============
console.log("\n[10] 设置持久化与时序回归");
{
  const p = await browser.newPage({viewport:{width:1280,height:900}});
  const errors = []; p.on("pageerror",e=>errors.push(String(e)));
  await p.goto(PAGE_URL,{waitUntil:"load"});
  await until(()=>p.evaluate(()=>window.__settingsMock.ready));
  await p.click('.nav-item[data-view="settings"]');
  await p.fill("#cfg-mineru-token","CURRENT-TOKEN");
  await p.click('.conn-dot[data-conn="mineru"]');
  await until(()=>p.locator('.conn-dot[data-conn="mineru"]').getAttribute("class").then(t=>t.includes("info")));
  ok("MinerU 使用当前输入且准确说明验证范围", await p.evaluate(()=>window.__settingsMock.calls.some(c=>c.method==="mineru" && c.token==="CURRENT-TOKEN")) && (await p.locator(".conn-message").first().textContent())==="服务已响应，解析功能待验证");
  await p.evaluate(()=>window.__settingsMock.delay=180);
  await p.fill("#cfg-llm-key","KEY-BEFORE");
  await p.click('.conn-dot[data-conn="llm"]');
  await p.fill("#cfg-llm-key","KEY-AFTER");
  await p.waitForTimeout(300);
  ok("旧连接结果不覆盖新 Key", (await p.locator('.conn-dot[data-conn="llm"]').getAttribute("class")).includes("idle"));
  await p.click("#btn-fetch-models");
  await p.fill("#cfg-llm-url","https://changed.invalid/v1");
  await p.waitForTimeout(300);
  ok("旧模型列表不回填", await p.locator("#model-pop").isHidden());
  await p.evaluate(async()=>{window.__settingsMock.delay=0;await flushSettings();});
  await p.evaluate(()=>{
    const el=document.querySelector("#cfg-mineru-token");el.dispatchEvent(new CompositionEvent("compositionstart",{bubbles:true}));
    el.value="IME-DRAFT";el.dispatchEvent(new InputEvent("input",{bubbles:true,isComposing:true}));
  });
  await p.waitForTimeout(700);
  ok("组合输入期间不落盘", await p.evaluate(()=>MOCK_CFG.mineru.token === "CURRENT-TOKEN"));
  await p.evaluate(()=>document.querySelector("#cfg-mineru-token").dispatchEvent(new CompositionEvent("compositionend",{bubbles:true})));
  await until(()=>p.evaluate(()=>MOCK_CFG.mineru.token === "IME-DRAFT"));
  ok("组合输入完成后自动保存", await p.evaluate(()=>MOCK_CFG.mineru.token === "IME-DRAFT"));

  await p.evaluate(()=>window.__settingsMock.delay=160);
  await p.fill("#cfg-llm-model","WRITE-IN-FLIGHT");
  await p.locator("#cfg-llm-model").blur();
  await p.fill("#cfg-llm-model","EDIT-WHILE-WRITING");
  await p.check("#cfg-toc",{force:true});
  await p.evaluate(()=>flushSettings());
  ok("保存期间的新输入和其他区域均写入",await p.evaluate(()=>MOCK_CFG.llm.model==="EDIT-WHILE-WRITING" && MOCK_CFG.export.toc));
  await p.evaluate(()=>window.__settingsMock.delay=0);

  const applied=await p.evaluate(()=>MOCK_CFG.rules.presets[0].text);
  await p.click('[data-rrow="0"]');await p.click('[data-redit="0"]');
  await p.fill("[data-rtext]","DRAFT-ONLY");
  await p.evaluate(()=>flushSettings());
  ok("草稿不覆盖应用版本", await p.evaluate(old=>MOCK_CFG.rules.presets[0].text===old && MOCK_CFG.rules.presets[0].draft_text==="DRAFT-ONLY",applied));
  await p.reload({waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
  await p.click('.nav-item[data-view="settings"]');await p.click('[data-rrow="0"]');await p.click('[data-redit="0"]');
  ok("重载恢复草稿", await p.locator("[data-rtext]").inputValue()==="DRAFT-ONLY");
  await p.fill("[data-rtext]","x".repeat(4001));await p.click('[data-rsave="0"]');
  await until(()=>p.locator("#modal-scrim").isVisible());
  ok("超限规则禁止应用",(await p.locator("#modal-title").textContent())==="规则尚不能应用");
  await p.keyboard.press("Escape");await p.fill("[data-rtext]","APPLIED-NEW");await p.click('[data-rsave="0"]');
  await until(()=>p.evaluate(()=>MOCK_CFG.rules.presets[0].text==="APPLIED-NEW"));
  ok("显式应用提交规则",await p.evaluate(()=>MOCK_CFG.rules.presets[0].text==="APPLIED-NEW" && MOCK_CFG.rules.presets[0].draft_text===undefined));
  await p.click("#btn-rule-add");await p.evaluate(()=>flushSettings());
  await p.fill("[data-rtext]","SECOND-DRAFT");await p.evaluate(()=>flushSettings());
  await p.click("[data-rcancel]");await p.click('[data-rdel="2"]');await p.click("#modal-btns .danger");
  await until(()=>p.locator("#undo-rules button").isVisible());await p.click("#undo-rules button");await p.evaluate(()=>flushSettings());
  ok("删除规则可撤销且草稿保留",await p.evaluate(()=>MOCK_CFG.rules.presets.length===3 && MOCK_CFG.rules.presets[2].draft_text==="SECOND-DRAFT"));
  await p.evaluate(()=>window.__settingsMock.failSaves=true);
  await p.click('[data-rrow="2"]');await p.click('[data-rdel="2"]');await p.click("#modal-btns .danger");
  await until(()=>p.locator("#save-rules").textContent().then(t=>t.includes("保存失败")));
  ok("规则删除保存失败后编辑区仍与本地状态一致",(await p.locator("#rule-list .rule-card").count())===2 && (await p.locator("#undo-rules button").isVisible()));
  await p.evaluate(()=>window.__settingsMock.failSaves=false);
  await p.click("#undo-rules button");await p.evaluate(()=>flushSettings());
  ok("规则删除失败可撤销并恢复保存",await p.evaluate(()=>MOCK_CFG.rules.presets.length===3 && MOCK_CFG.rules.presets[2].draft_text==="SECOND-DRAFT"));
  await p.click('[data-rrow="2"]');await p.click('[data-rlive="2"]');
  await p.evaluate(()=>flushSettings());
  ok("设为生效不提交草稿",await p.evaluate(()=>MOCK_CFG.rules.active===MOCK_CFG.rules.presets[2].name && MOCK_CFG.rules.presets[2].text!=="SECOND-DRAFT" && MOCK_CFG.rules.presets[2].draft_text==="SECOND-DRAFT"));
  await p.click('[data-rrow="0"]');await p.click('[data-redit="0"]');await p.click('[data-rreset]');
  await p.evaluate(()=>flushSettings());
  ok("恢复默认只改草稿",await p.evaluate(()=>MOCK_CFG.rules.presets[0].text==="APPLIED-NEW" && MOCK_CFG.rules.presets[0].draft_text===MOCK_IRON));
  await p.click('[data-rname="0"]');await p.fill(".prename","改名后的预设");await p.locator(".prename").blur();
  await p.evaluate(()=>flushSettings());
  ok("编辑时改名保留草稿并自动保存",await p.evaluate(()=>MOCK_CFG.rules.presets[0].name==="改名后的预设" && MOCK_CFG.rules.presets[0].draft_text===MOCK_IRON));

  await p.click("#g-add");await p.fill('[data-gi="1"][data-gk="en"]',"UNFINISHED");
  await p.selectOption("#cfg-font","songti");await p.evaluate(()=>flushSettings());
  ok("切字体不丢未完成术语",await p.evaluate(()=>MOCK_CFG.glossary_rows.length===2 && MOCK_CFG.glossary_rows[1].en==="UNFINISHED" && MOCK_CFG.glossary.length===1));
  ok("未完成行不显示额外提示",!(await p.locator("#glossary-box").textContent()).includes("待补全"));
  await p.fill('[data-gi="1"][data-gk="zh"]',"完整译名");await p.evaluate(()=>flushSettings());
  ok("完整术语参与翻译",await p.evaluate(()=>MOCK_CFG.glossary.length===2));
  await p.locator(".g-del").last().click();await p.click("#modal-btns .danger");
  await until(()=>p.locator("#undo-glossary button").isVisible());await p.click("#undo-glossary button");await p.evaluate(()=>flushSettings());
  ok("术语删除可撤销",await p.evaluate(()=>MOCK_CFG.glossary.length===2));
  await p.fill('[data-gi="1"][data-gk="zh"]',"字体导入前的新译名");await p.click("#font-import");await p.evaluate(()=>flushSettings());
  ok("导入字体保留术语编辑",await p.evaluate(()=>MOCK_CFG.glossary[1].zh==="字体导入前的新译名" && MOCK_CFG.export.user_font==="MockFont"));

  await p.evaluate(()=>window.__settingsMock.failSaves=true);
  await p.check("#cfg-bilingual",{force:true});
  await until(()=>p.locator("#save-layout").textContent().then(t=>t.includes("保存失败")));
  ok("写入失败保留输入",await p.locator("#cfg-bilingual").isChecked() && !(await p.evaluate(()=>MOCK_CFG.export.bilingual)));
  await p.evaluate(()=>window.__settingsMock.failSaves=false);await p.click("#save-layout button");
  await until(()=>p.evaluate(()=>MOCK_CFG.export.bilingual));
  ok("失败重试成功",await p.evaluate(()=>MOCK_CFG.export.bilingual));
  await p.fill("#cfg-llm-model","LATEST-MODEL");await p.click('.nav-item[data-view="tasks"]');
  await p.click("#btn-queue-toggle");
  ok("启动前提交最后一次输入",await p.evaluate(()=>window.__settingsMock.calls.some(c=>c.method==="start" && c.config.llm.model==="LATEST-MODEL")));
  await p.click('.nav-item[data-view="settings"]');
  await p.evaluate(()=>window.__settingsMock.failSaves=true);await p.fill("#cfg-llm-model","BEFORE-CLOSE");
  await p.evaluate(()=>window.runtime.Quit());
  await until(()=>p.locator("#modal-title").textContent().then(t=>t==="退出前保存失败"));
  ok("系统关闭先等待保存",(await p.locator("#modal-title").textContent())==="退出前保存失败" && !(await p.evaluate(()=>window.__settingsMock.closed)));
  await p.locator("#modal-btns button").nth(1).click();
  ok("继续编辑不退出",!(await p.evaluate(()=>window.__settingsMock.closed)));
  await p.evaluate(()=>window.__settingsMock.failSaves=false);await p.click("#win-close");
  await until(()=>p.locator("#modal-title").textContent().then(t=>t==="退出前保存失败"));
  await p.locator("#modal-btns button").first().click();
  await until(()=>p.evaluate(()=>window.__settingsMock.closed));
  ok("重试保存后退出",await p.evaluate(()=>window.__settingsMock.closed && MOCK_CFG.llm.model==="BEFORE-CLOSE"));
  ok("设置回归无运行时错误",errors.length===0,errors.join(" | "));
  fs.mkdirSync("output/playwright",{recursive:true});
  await p.evaluate(()=>{applyTheme("light");document.querySelector(".main").scrollTop=0;});
  await p.waitForTimeout(250);
  await p.screenshot({path:"output/playwright/settings-autosave-light.png"});
  await p.evaluate(()=>applyTheme("dark"));await p.waitForTimeout(250);
  await p.screenshot({path:"output/playwright/settings-autosave-dark.png"});
  await p.close();

  const q=await browser.newPage({viewport:{width:1040,height:680}});
  await q.goto(PAGE_URL,{waitUntil:"load"});await until(()=>q.evaluate(()=>window.__settingsMock.ready));
  await q.click('.nav-item[data-view="settings"]');
  const previous=await q.evaluate(()=>MOCK_CFG.llm.model);
  await q.evaluate(()=>window.__settingsMock.failSaves=true);
  await q.fill("#cfg-llm-model","DISCARD-THIS-EDIT");await q.click("#win-close");
  await until(()=>q.locator("#modal-title").textContent().then(t=>t==="退出前保存失败"));
  await q.locator("#modal-btns button").nth(2).click();await until(()=>q.evaluate(()=>window.__settingsMock.closed));
  ok("放弃未保存修改并退出保留已存版本",await q.evaluate(old=>window.__settingsMock.closed && MOCK_CFG.llm.model===old,previous));
  await q.close();
}

// ============ [11] 仅实际变更保存与反馈 ============
console.log("\n[11] 保存变更判断与反馈时序");
{
  const p=await browser.newPage({viewport:{width:1040,height:800}});
  const errors=[];p.on("pageerror",e=>errors.push(String(e)));
  await p.goto(PAGE_URL,{waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
  await p.click('.nav-item[data-view="settings"]');
  const writes=()=>p.evaluate(()=>window.__settingsMock.calls.filter(c=>c.method==="save").length);
  const status=()=>p.locator("#save-service").textContent();
  ok("初始设置不显示保存成功",(await p.locator(".autosave-state").allTextContents()).every(t=>!t.trim()));
  await p.evaluate(()=>{
    for(const id of Object.keys(settingsFields)){
      const el=document.getElementById(id);el.focus();el.blur();
      if(el.tagName==="SELECT" || el.type==="checkbox")el.dispatchEvent(new Event("change",{bubbles:true}));
    }
    for(const el of document.querySelectorAll("#glossary-box input")){el.focus();el.blur();}
    document.getElementById("cfg-font").dispatchEvent(new Event("change",{bubbles:true}));
  });
  await p.click('[data-rrow="0"]');await p.click('[data-redit="0"]');
  await p.locator("[data-rtext]").dispatchEvent("input");
  await p.click('.nav-item[data-view="tasks"]');await p.click('.nav-item[data-view="settings"]');
  await p.evaluate(()=>flushSettings());
  ok("无改动的聚焦失焦选择和切页不写盘",await writes()===0);
  ok("无改动操作不产生已保存提示",(await p.locator(".autosave-state").allTextContents()).every(t=>!t.trim()));

  await p.evaluate(()=>window.__settingsMock.delay=120);
  await p.fill("#cfg-llm-model","REAL-CHANGE");await p.locator("#cfg-llm-model").blur();
  ok("实际写入未完成时不显示已保存",!(await status()).includes("已保存"));
  await until(()=>status().then(t=>t.includes("已保存")));
  ok("真实修改成功后才显示已保存",await p.evaluate(()=>MOCK_CFG.llm.model==="REAL-CHANGE") && (await status()).includes("已保存"));
  await p.evaluate(()=>window.__settingsMock.delay=0);
  const afterSave=await writes();
  await p.waitForTimeout(600);await p.locator("#cfg-llm-model").focus();await p.locator("#cfg-llm-model").blur();
  ok("已保存字段再次失焦不重复提交",await writes()===afterSave);
  await p.waitForTimeout(1100);
  ok("成功提示显示一秒后半秒淡出且无改动点击不延长",!(await status()).trim());

  const beforeCancel=await writes();
  await p.fill("#cfg-llm-model","CANCEL-BEFORE-SEND");await p.fill("#cfg-llm-model","REAL-CHANGE");
  await p.waitForTimeout(700);await p.evaluate(()=>flushSettings());
  ok("请求发出前恢复原值不保存",await writes()===beforeCancel);
  ok("撤回未发送修改不显示成功提示",!(await status()).trim());

  await p.evaluate(()=>window.__settingsMock.delay=250);
  const beforeRevert=await writes();
  await p.fill("#cfg-llm-model","IN-FLIGHT");await p.locator("#cfg-llm-model").blur();
  await p.fill("#cfg-llm-model","REAL-CHANGE");
  await until(()=>p.evaluate(()=>MOCK_CFG.llm.model==="IN-FLIGHT"));
  ok("旧响应不把尚未保存的新值标为成功",(await status()).includes("保存中") && !(await status()).includes("已保存"));
  await p.evaluate(()=>flushSettings());
  ok("请求发出后恢复原值仍正确保存",await writes()===beforeRevert+2 && await p.evaluate(()=>MOCK_CFG.llm.model==="REAL-CHANGE"));

  const beforeCoalesce=await writes();
  await p.fill("#cfg-llm-model","TARGET");await p.locator("#cfg-llm-model").blur();
  await p.fill("#cfg-llm-model","TRANSIENT");await p.fill("#cfg-llm-model","TARGET");await p.evaluate(()=>flushSettings());
  ok("改回正在提交的值不重复保存",await writes()===beforeCoalesce+1 && await p.evaluate(()=>MOCK_CFG.llm.model==="TARGET"));
  await p.evaluate(()=>{window.__settingsMock.delay=0;window.__settingsMock.failSaves=true;});
  await p.fill("#cfg-llm-model","SAVE-FAILURE");await p.locator("#cfg-llm-model").blur();
  await until(()=>status().then(t=>t.includes("保存失败")));
  const afterFailure=await writes();
  await p.waitForTimeout(1600);
  ok("失败提示不被旧成功提示的定时器清除",(await status()).includes("保存失败"));
  await p.locator("#cfg-llm-model").focus();await p.locator("#cfg-llm-model").blur();await p.waitForTimeout(150);
  ok("失败后未修改的失焦不自动重试",await writes()===afterFailure && (await status()).includes("保存失败"));
  await p.fill("#cfg-llm-model","TARGET");await p.evaluate(()=>flushSettings());
  ok("恢复已保存内容取消失败修改",await writes()===afterFailure && !(await status()).trim() && await p.evaluate(()=>MOCK_CFG.llm.model==="TARGET"));
  await p.evaluate(()=>window.__settingsMock.delay=200);
  const beforeFailedRevert=await writes();
  await p.fill("#cfg-llm-model","FAIL-IN-FLIGHT");await p.locator("#cfg-llm-model").blur();
  await p.fill("#cfg-llm-model","TARGET");await p.evaluate(()=>flushSettings());
  ok("在途请求失败但已恢复原值不误报成功或失败",await writes()===beforeFailedRevert+1 && !(await status()).trim() && await p.evaluate(()=>MOCK_CFG.llm.model==="TARGET"));
  await p.evaluate(()=>{window.__settingsMock.failSaves=false;window.__settingsMock.delay=0;});

  const beforePartial=await writes();
  await p.evaluate(()=>{
    const set=(id,value)=>{const el=document.getElementById(id);el.value=value;el.dispatchEvent(new Event("input",{bubbles:true}));};
    set("cfg-llm-model","PARTIAL-REVERT");set("cfg-llm-url","https://latest.invalid/v1");set("cfg-llm-model","TARGET");
  });
  await p.evaluate(()=>flushSettings());
  ok("撤回部分字段保留其他实际修改",await writes()===beforePartial+1 && await p.evaluate(()=>MOCK_CFG.llm.model==="TARGET" && MOCK_CFG.llm.base_url==="https://latest.invalid/v1"));

  await p.fill('[data-gi="0"][data-gk="en"]',"changed term");await p.evaluate(()=>flushSettings());
  const afterTerm=await writes();
  await p.locator('[data-gi="0"][data-gk="en"]').focus();await p.locator('[data-gi="0"][data-gk="en"]').blur();
  await p.evaluate(()=>flushSettings());
  ok("已保存术语再次失焦不重复保存",await writes()===afterTerm && await p.evaluate(()=>MOCK_CFG.glossary[0].en==="changed term"));
  await p.selectOption("#cfg-font","yahei");await p.evaluate(()=>flushSettings());
  const afterFont=await writes();
  await p.locator("#cfg-font").dispatchEvent("change");await p.evaluate(()=>flushSettings());
  ok("已保存字体的相同选择不重复保存",await writes()===afterFont && await p.evaluate(()=>MOCK_CFG.export.font_preset==="yahei"));
  await p.click("#font-import");await p.evaluate(()=>flushSettings());
  const afterImport=await writes();
  await p.locator("#cfg-font").dispatchEvent("change");await p.evaluate(()=>flushSettings());
  ok("导入字体后同一选择不重复保存",await writes()===afterImport && await p.evaluate(()=>MOCK_CFG.export.user_font==="MockFont"));
  const beforeIME=await writes();
  await p.evaluate(()=>{
    const el=document.getElementById("cfg-llm-model");el.focus();
    el.dispatchEvent(new CompositionEvent("compositionstart",{bubbles:true}));
    el.value="IME-PENDING";el.dispatchEvent(new InputEvent("input",{bubbles:true,isComposing:true}));
  });
  await p.waitForTimeout(700);
  ok("组合输入清除旧成功提示且不保存半成品",await writes()===beforeIME && !(await status()).includes("已保存") && await p.evaluate(()=>MOCK_CFG.llm.model==="TARGET"));
  await p.locator("#cfg-llm-model").dispatchEvent("compositionend");await p.evaluate(()=>flushSettings());
  ok("组合输入结束保存后才确认成功",await p.evaluate(()=>MOCK_CFG.llm.model==="IME-PENDING") && (await status()).includes("已保存"));
  await p.evaluate(()=>window.__settingsMock.delay=200);
  await p.fill("#cfg-llm-model","BEFORE-IME");await p.locator("#cfg-llm-model").blur();
  await p.evaluate(()=>{
    const el=document.getElementById("cfg-llm-model");el.focus();
    el.dispatchEvent(new CompositionEvent("compositionstart",{bubbles:true}));
    el.value="IME-LATEST";el.dispatchEvent(new InputEvent("input",{bubbles:true,isComposing:true}));
  });
  await until(()=>p.evaluate(()=>MOCK_CFG.llm.model==="BEFORE-IME"));
  ok("旧写入完成不把当前组合输入标为已保存",!(await status()).includes("已保存"));
  await p.locator("#cfg-llm-model").dispatchEvent("compositionend");await p.evaluate(()=>flushSettings());
  ok("在途保存与组合输入交错后保留最新内容",await p.evaluate(()=>MOCK_CFG.llm.model==="IME-LATEST") && (await status()).includes("已保存"));
  ok("保存状态回归无运行时错误",errors.length===0,errors.join(" | "));
  await p.close();
}

// ============ [12] 输出卡片统一保存提示 ============
console.log("\n[12] 输出卡片右上角保存反馈");
{
  const p=await browser.newPage({viewport:{width:1040,height:800}});
  const errors=[];p.on("pageerror",e=>errors.push(String(e)));
  await p.goto(PAGE_URL,{waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
  await p.click('.nav-item[data-view="settings"]');
  const card=p.locator('.set-card[data-section="output"]');
  const status=()=>p.locator("#save-output").textContent();
  const layout=()=>card.evaluate(el=>{
    const top=el.getBoundingClientRect().top;
    return [el.querySelector("#cfg-font").getBoundingClientRect().top-top,el.querySelector("#cfg-outdir").getBoundingClientRect().top-top];
  });
  const before=await layout();
  await p.selectOption("#cfg-font","songti");await p.evaluate(()=>flushSettings());
  ok("字体保存提示位于输出标题右侧",await p.locator('.set-card[data-section="output"] h3 #save-output.saved').count()===1);
  ok("字体控件下方不再有保存提示",await p.locator("#cfg-font").evaluate(el=>!el.parentElement.querySelector(".autosave-state")) && await card.locator(".autosave-state").count()===1);
  const after=await layout();
  ok("保存提示不改变字体与目录行的位置",before.every((v,i)=>Math.abs(v-after[i])<1));
  await p.evaluate(()=>applyTheme("light"));await p.waitForTimeout(200);
  await card.screenshot({path:"output/playwright/settings-output-status-v4.png"});

  await p.evaluate(()=>window.__settingsMock.delay=600);
  await p.selectOption("#cfg-font","yahei");await p.fill("#cfg-outdir","D:\\Demo\\Library");await p.locator("#cfg-outdir").blur();
  await until(()=>p.evaluate(()=>MOCK_CFG.export.font_preset==="yahei"));
  ok("字体先保存完成时仍提示目录保存中",(await status()).includes("保存中") && !(await status()).includes("已保存"));
  await p.evaluate(()=>flushSettings());
  ok("字体与目录完成后只显示一条成功提示",(await status())==="✓ 已保存" && await card.locator(".autosave-state.saved").count()===1 && await p.evaluate(()=>MOCK_CFG.export.output_dir==="D:\\Demo\\Library"));
  await p.waitForTimeout(1100);
  ok("较早完成的保存定时器不隐藏较晚成功提示",(await status()).includes("已保存"));
  await p.waitForTimeout(650);
  ok("统一成功提示在最后一次保存后一秒加半秒淡出后隐藏",!(await status()).trim());
  await p.evaluate(()=>{window.__settingsMock.delay=0;window.__settingsMock.failSaves=true;});
  await p.fill("#cfg-outdir","D:\\Demo\\Blocked");await p.locator("#cfg-outdir").blur();
  await until(()=>status().then(t=>t.includes("保存失败")));
  await p.evaluate(()=>window.__settingsMock.failSaves=false);await p.selectOption("#cfg-font","songti");
  await until(()=>p.evaluate(()=>MOCK_CFG.export.font_preset==="songti"));
  ok("字体成功不覆盖目录失败",(await status()).includes("保存失败") && (await status()).includes("归档目录："));
  await p.locator("#save-output button").click();await until(()=>status().then(t=>t.includes("已保存")));
  ok("共享位置可重试目录保存",await p.evaluate(()=>MOCK_CFG.export.output_dir==="D:\\Demo\\Blocked"));

  await p.evaluate(()=>window.__settingsMock.failSaves=true);await p.selectOption("#cfg-font","yahei");
  await until(()=>status().then(t=>t.includes("保存失败")));
  await p.evaluate(()=>window.__settingsMock.failSaves=false);
  await p.fill("#cfg-outdir","D:\\Demo\\Other");await p.locator("#cfg-outdir").blur();
  await until(()=>p.evaluate(()=>MOCK_CFG.export.output_dir==="D:\\Demo\\Other"));
  ok("目录成功不覆盖字体失败",(await status()).includes("保存失败") && (await status()).includes("字体："));
  await p.locator("#save-output button").click();await until(()=>status().then(t=>t.includes("已保存")));
  ok("共享位置可重试字体保存",await p.evaluate(()=>MOCK_CFG.export.font_preset==="yahei"));

  await p.evaluate(()=>window.__settingsMock.failSaves=true);await p.selectOption("#cfg-font","songti");
  await p.fill("#cfg-outdir","D:\\Demo\\Both");await p.locator("#cfg-outdir").blur();
  await until(()=>p.locator("#save-output button").count().then(n=>n===2));
  ok("两项失败保留各自的重试入口",(await p.locator("#save-output button").allTextContents()).join("/").includes("重试字体") && (await status()).includes("归档目录："));
  await p.evaluate(()=>window.__settingsMock.failSaves=false);
  await p.getByRole("button",{name:"重试字体",exact:true}).click();
  await until(()=>p.evaluate(()=>MOCK_CFG.export.font_preset==="songti"));
  ok("重试其中一项不清除另一项错误",(await status()).includes("归档目录：") && !(await status()).includes("字体："));
  await p.locator("#save-output button").click();await until(()=>status().then(t=>t.includes("已保存")));
  ok("两项重试后正确保存最终配置",await p.evaluate(()=>MOCK_CFG.export.font_preset==="songti" && MOCK_CFG.export.output_dir==="D:\\Demo\\Both"));
  ok("输出卡片回归无运行时错误",errors.length===0,errors.join(" | "));
  await p.close();
}

// ============ [13] MinerU 探测文案与信息状态 ============
console.log("\n[13] MinerU 检测结果表达");
{
  const p=await browser.newPage({viewport:{width:1040,height:800}});
  const errors=[];p.on("pageerror",e=>errors.push(String(e)));
  await p.goto(PAGE_URL,{waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
  await p.click('.nav-item[data-view="settings"]');
  const dot=p.locator('.conn-dot[data-conn="mineru"]');
  const message=p.locator('.conn-message').first();
  await dot.click();await until(()=>message.textContent().then(t=>t==="服务已响应，解析功能待验证"));
  ok("MinerU 已响应显示信息图标",(await dot.getAttribute("class")).includes("info") && (await dot.textContent())==="ⓘ");
  const blue=await until(()=>dot.evaluate(el=>getComputedStyle(el).color===getComputedStyle(document.querySelector(".nav-item.active")).backgroundColor));
  ok("MinerU 信息图标使用蓝色强调色",blue===true);
  ok("探测结果不显示验证通过的绿色勾",!(await dot.getAttribute("class")).includes("ok") && (await dot.textContent())!=="✓");
  ok("辅助技术能读取新的验证范围提示",await dot.getAttribute("aria-label")==="服务已响应，解析功能待验证" && await message.getAttribute("role")==="status");
  ok("已响应结果记录为信息而非警告",await p.evaluate(()=>logLines.some(l=>l.level==="info" && l.msg==="MinerU：服务已响应，解析功能待验证") && !logLines.some(l=>l.level==="warn" && l.msg==="MinerU：服务已响应，解析功能待验证")));
  await p.evaluate(()=>applyTheme("light"));await p.waitForTimeout(200);
  await p.locator('.set-card[data-section="service"]').screenshot({path:"output/playwright/settings-mineru-status-v5.png"});
  for(const result of [
    {status:"error",message:"Token 认证失败，请检查凭据或账号权限",icon:"✕",cls:"bad"},
    {status:"error",message:"Token 已过期，请更新",icon:"✕",cls:"bad"},
    {status:"error",message:"暂时无法连接，请检查网络后重试",icon:"✕",cls:"bad"},
    {status:"warning",message:"已收到响应，暂时无法确认服务状态",icon:"!",cls:"warning"},
  ]){
    await p.evaluate(r=>window.__settingsMock.mineruResult={status:r.status,message:r.message},result);
    await dot.click();await until(()=>message.textContent().then(t=>t===result.message));
    ok("分类提示："+result.message,(await dot.getAttribute("class")).includes(result.cls) && (await dot.textContent())===result.icon && await message.textContent()===result.message);
  }
  await p.evaluate(()=>{window.__settingsMock.mineruResult=null;window.__settingsMock.delay=180;});
  await dot.click();await p.fill("#cfg-mineru-token","CHANGED-AFTER-REQUEST");await p.waitForTimeout(300);
  ok("旧信息响应不能覆盖已改变的 Token",(await dot.getAttribute("class")).includes("idle") && !(await message.textContent()).trim());
  ok("MinerU 文案回归无运行时错误",errors.length===0,errors.join(" | "));
  await p.close();
}

// ============ [14] 侧栏服务配置状态实时同步 ============
console.log("\n[14] 侧栏服务配置状态");
{
  const p=await browser.newPage({viewport:{width:1040,height:800}});
  const errors=[];p.on("pageerror",e=>errors.push(String(e)));
  await p.addInitScript(()=>{
    if (!sessionStorage.getItem("literature-settings-mock")) sessionStorage.setItem("literature-settings-mock",JSON.stringify({
      mineru:{token:"",model_version:"vlm"},llm:{base_url:"",api_key:"",model:"",concurrency:3},
    }));
  });
  await p.goto(PAGE_URL,{waitUntil:"load"});
  const sidebar=p.locator("#sidebar-status");
  const status=()=>sidebar.textContent();
  const edit=async values=>p.evaluate(values=>{
    for(const [id,value] of Object.entries(values)){
      const el=document.getElementById(id);el.value=value;el.dispatchEvent(new Event("input",{bubbles:true}));
    }
  },values);
  await until(()=>status().then(t=>t.includes("待配置")));
  ok("启动缺少配置时显示待配置",(await status()).includes("待配置"));
  const missing=await sidebar.getAttribute("title");
  ok("悬停说明缺少的四项必要配置",["MinerU Token","模型 API Key","模型名","模型 API 地址"].every(s=>missing.includes(s)));
  await p.click('.nav-item[data-view="settings"]');
  await p.evaluate(()=>window.__settingsMock.delay=180);
  await edit({"cfg-mineru-token":"fake-sidebar-token","cfg-llm-key":"fake-sidebar-key","cfg-llm-url":"https://sidebar.invalid/v1","cfg-llm-model":"sidebar-demo"});
  ok("填完整但尚未保存时显示保存中",(await status()).includes("保存中") && await p.evaluate(()=>MOCK_CFG.llm.api_key===""));
  await p.evaluate(()=>flushSettings());
  ok("保存成功后无需重启即显示已配置",(await status()).includes("已配置") && !(await status()).includes("就绪"));
  ok("已配置明确说明仍需验证连接与任务",(await sidebar.getAttribute("title")).includes("仍需验证"));
  ok("状态保留版本并支持辅助技术读取",(await status()).includes("v1.0.5") && await sidebar.getAttribute("role")==="status" && await sidebar.getAttribute("aria-live")==="polite" && (await sidebar.getAttribute("aria-label")).includes("已配置"));
  await p.evaluate(()=>window.__settingsMock.delay=0);

  await edit({"cfg-mineru-token":'""'});await p.evaluate(()=>flushSettings());
  ok("后端规范化为空的凭据不被判为已配置",(await status()).includes("待配置") && (await sidebar.getAttribute("title")).includes("MinerU Token") && await p.evaluate(()=>MOCK_CFG.mineru.token===""));
  ok("规范化响应不覆盖当前表单输入",await p.locator("#cfg-mineru-token").inputValue()==='""');
  await edit({"cfg-mineru-token":"Bearer fake-sidebar-token"});await p.evaluate(()=>flushSettings());
  ok("规范化后有效凭据恢复已配置",(await status()).includes("已配置") && await p.evaluate(()=>MOCK_CFG.mineru.token==="fake-sidebar-token"));
  for(const [id,value,hint] of [
    ["cfg-llm-key","   ","模型 API Key"],
    ["cfg-llm-model","   ","模型名"],
    ["cfg-llm-url","","模型 API 地址"],
    ["cfg-llm-url","ftp://sidebar.invalid/v1","HTTP 或 HTTPS"],
    ["cfg-llm-url","https:sidebar.invalid/v1","HTTP 或 HTTPS"],
  ]){
    await edit({[id]:value});await p.evaluate(()=>flushSettings());
    ok("缺项或格式无效时待配置："+id+" / "+JSON.stringify(value),(await status()).includes("待配置") && (await sidebar.getAttribute("title")).includes(hint));
    await edit({"cfg-llm-key":"fake-sidebar-key","cfg-llm-model":"sidebar-demo","cfg-llm-url":"https://sidebar.invalid/v1"});await p.evaluate(()=>flushSettings());
  }
  await p.evaluate(()=>window.__settingsMock.failSaves=true);
  await edit({"cfg-llm-model":"FAILED-SIDEBAR-EDIT"});await p.evaluate(()=>flushSettings().catch(()=>{}));
  ok("服务保存失败显示配置未保存",(await status()).includes("配置未保存") && (await sidebar.getAttribute("title")).includes("重试"));
  ok("保存失败保留编辑与原有效配置",await p.locator("#cfg-llm-model").inputValue()==="FAILED-SIDEBAR-EDIT" && await p.evaluate(()=>MOCK_CFG.llm.model==="sidebar-demo"));
  await p.evaluate(()=>window.__settingsMock.failSaves=false);
  await p.locator("#cfg-bilingual").check({force:true});await p.evaluate(()=>flushSettings().catch(()=>{}));
  ok("其他卡片保存成功不覆盖服务配置失败",(await status()).includes("配置未保存") && await p.evaluate(()=>MOCK_CFG.export.bilingual));
  await p.waitForTimeout(1650);
  ok("配置未保存不会随成功提示定时消失",(await status()).includes("配置未保存"));
  await p.locator("#save-service button").click();await until(()=>status().then(t=>t.includes("已配置")));
  ok("服务配置重试成功后实时恢复已配置",(await status()).includes("已配置") && await p.evaluate(()=>MOCK_CFG.llm.model==="FAILED-SIDEBAR-EDIT"));

  await p.evaluate(()=>window.__settingsMock.delay=220);
  await edit({"cfg-llm-model":"EARLIER-VALID-MODEL"});await p.locator("#cfg-llm-model").dispatchEvent("blur");
  await edit({"cfg-llm-model":""});
  await until(()=>p.evaluate(()=>MOCK_CFG.llm.model==="EARLIER-VALID-MODEL"));
  ok("旧成功响应不提前确认仍待保存的新输入",(await status()).includes("保存中"));
  await p.evaluate(()=>flushSettings());
  ok("较晚清空字段成功后实时变为待配置",(await status()).includes("待配置") && await p.evaluate(()=>MOCK_CFG.llm.model===""));
  await p.evaluate(()=>window.__settingsMock.delay=0);
  await edit({"cfg-llm-model":"sidebar-demo"});await p.evaluate(()=>flushSettings());
  await p.waitForTimeout(1650);
  ok("卡片成功提示隐藏后侧栏仍显示已配置",!(await p.locator("#save-service").textContent()).trim() && (await status()).includes("已配置"));
  const descriptions=(await sidebar.getAttribute("title"))+(await sidebar.getAttribute("aria-label"));
  ok("状态说明不包含凭据",!descriptions.includes("fake-sidebar-token") && !descriptions.includes("fake-sidebar-key"));
  ok("状态同步不自动调用远程连接检测",await p.evaluate(()=>!window.__settingsMock.calls.some(c=>["mineru","llm","models"].includes(c.method))));
  await p.evaluate(()=>applyTheme("light"));await p.waitForTimeout(200);
  await p.screenshot({path:"output/playwright/settings-sidebar-status-v7.png"});
  await p.reload({waitUntil:"load"});await until(()=>status().then(t=>t.includes("已配置")));
  ok("重载后按持久化配置正确显示状态",(await status()).includes("已配置") && (await status()).includes("v1.0.5"));
  ok("侧栏状态回归无运行时错误",errors.length===0,errors.join(" | "));
  await p.close();
}

// ============ [15] 模型弹层固定标题与列表滚动 ============
console.log("\n[15] 模型弹层标题贴齐与独立滚动");
{
  const p=await browser.newPage({viewport:{width:1040,height:800}});
  const errors=[];p.on("pageerror",e=>errors.push(String(e)));
  await p.goto(PAGE_URL,{waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
  await p.click('.nav-item[data-view="settings"]');
  const pop=p.locator("#model-pop");
  const head=pop.locator(".mp-head");
  const list=pop.locator(".mp-list");
  ok("模型弹层初始保持隐藏",await pop.isHidden());
  await p.evaluate(()=>window.__settingsMock.models=Array.from({length:19},(_,i)=>"demo-model-"+String(i+1).padStart(2,"0")));
  await p.click("#btn-fetch-models");await until(()=>pop.isVisible());await p.waitForTimeout(250);
  ok("长列表显示19个模型及对应标题",await pop.locator(".mp-item").count()===19 && (await head.textContent()).includes("共 19 个模型"));
  const bounds=await pop.boundingBox();
  const header=await head.boundingBox();
  const rows=await list.boundingBox();
  ok("标题背景贴齐弹层顶部及左右内边缘",Math.abs(header.y-bounds.y-1)<1 && Math.abs(header.x-bounds.x-1)<1 && Math.abs(header.width-bounds.width+2)<1);
  ok("列表从标题分隔线下方开始",Math.abs(rows.y-header.y-header.height)<1);
  ok("长列表限制弹层总高度",bounds.height<=265);
  ok("标题文字和关闭按钮保留内部留白",await head.evaluate(el=>{
    const b=el.getBoundingClientRect(),label=el.querySelector("span").getBoundingClientRect(),close=el.querySelector("button").getBoundingClientRect();
    return label.left-b.left>=12 && label.top-b.top>=6 && b.right-close.right>=12;
  }));
  await list.hover();await p.mouse.wheel(0,420);
  await until(()=>list.evaluate(el=>el.scrollTop>0));
  ok("鼠标滚轮只滚动模型列表",await list.evaluate(el=>el.scrollTop>0) && await pop.evaluate(el=>el.scrollTop===0));
  const scrolledHeader=await head.boundingBox();
  ok("列表滚动时标题位置保持不变",Math.abs(scrolledHeader.y-header.y)<1 && Math.abs(scrolledHeader.height-header.height)<1);
  ok("滚动条所在列表区域始终位于标题下方",(await list.boundingBox()).y>=scrolledHeader.y+scrolledHeader.height-1);
  ok("滚动条目不会遮住标题内容",await head.evaluate(el=>{
    const b=el.getBoundingClientRect();return document.elementFromPoint(b.left+24,b.top+b.height/2)?.closest(".mp-head")===el;
  }));
  await p.evaluate(()=>applyTheme("light"));await p.waitForTimeout(200);
  await pop.screenshot({path:"output/playwright/model-picker-light-v8.png"});
  await p.evaluate(()=>applyTheme("dark"));await p.waitForTimeout(200);
  await pop.screenshot({path:"output/playwright/model-picker-dark-v8.png"});
  await head.locator(".mp-close").click();
  ok("列表滚动后关闭按钮仍可操作",await pop.isHidden());
  await p.locator("#cfg-llm-model").focus();await until(()=>pop.isVisible());
  await pop.locator('[data-m="demo-model-19"]').click();await p.evaluate(()=>flushSettings());
  ok("滚动选择末尾模型后关闭并保存",await pop.isHidden() && await p.locator("#cfg-llm-model").inputValue()==="demo-model-19" && await p.evaluate(()=>MOCK_CFG.llm.model==="demo-model-19"));
  await p.locator("#cfg-llm-model").focus();await until(()=>pop.isVisible());
  await p.fill("#cfg-llm-model","demo-model-03");
  ok("新列表结构仍可筛选模型",await pop.locator(".mp-item").count()===1 && await pop.locator(".mp-item").getAttribute("data-m")==="demo-model-03");
  ok("少量结果按内容收缩且无需滚动",(await pop.boundingBox()).height<150 && await list.evaluate(el=>el.scrollHeight===el.clientHeight));
  await p.fill("#cfg-llm-model","NO-MATCH");
  ok("无匹配时保留标题与空结果提示",await head.isVisible() && await list.locator(".mp-empty").count()===1 && await pop.locator(".mp-item").count()===0);
  await p.keyboard.press("Escape");
  ok("空结果时Esc仍可关闭",await pop.isHidden());
  ok("模型弹层布局回归无运行时错误",errors.length===0,errors.join(" | "));
  await p.close();
}

// ============ [16] 保存成功提示淡出 ============
console.log("\n[16] 显示一秒再半秒淡出");
{
  const p=await browser.newPage({viewport:{width:1040,height:800}});
  const errors=[];p.on("pageerror",e=>errors.push(String(e)));
  await p.goto(PAGE_URL,{waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
  await p.click('.nav-item[data-view="settings"]');
  const status=p.locator("#save-service");
  const visual=el=>({text:el.textContent,opacity:Number(getComputedStyle(el).opacity),phase:el.className,height:el.getBoundingClientRect().height});
  const duringFade=el=>{
    const opacity=Number(getComputedStyle(el).opacity);
    return el.classList.contains("fading") && opacity>0 && opacity<1 ? {opacity,text:el.textContent,height:el.getBoundingClientRect().height} : null;
  };
  await p.fill("#cfg-llm-model","FADE-DEMO");await p.evaluate(()=>flushSettings());
  const initial=await status.evaluate(visual);
  ok("保存成功立即清晰显示",initial.opacity===1 && initial.text==="✓ 已保存");
  await p.evaluate(()=>{
    window.__fadeTextChanges=0;
    new MutationObserver(()=>window.__fadeTextChanges++).observe(document.getElementById("save-service"),{childList:true,subtree:true,characterData:true});
  });
  await p.waitForTimeout(850);
  ok("前一秒保持完整可见",await status.evaluate(el=>el.classList.contains("saved") && Number(getComputedStyle(el).opacity)===1));
  const faded=await until(()=>status.evaluate(duringFade),1200,20);
  ok("成功提示经过中间透明度逐渐淡出",!!faded && faded.text==="✓ 已保存");
  ok("淡出期间不重复改写提示文字",await p.evaluate(()=>window.__fadeTextChanges===0));
  ok("淡出不改变提示行高度",!!faded && Math.abs(faded.height-initial.height)<1);
  await until(()=>status.textContent().then(t=>!t.trim()));
  ok("半秒淡出结束后移除文字",!(await status.textContent()).trim());

  await p.fill("#cfg-llm-model","INTERRUPT-FADE");await p.evaluate(()=>flushSettings());
  await until(()=>status.evaluate(duringFade),1800,20);
  await p.evaluate(()=>{window.__settingsMock.delay=180;window.__settingsMock.failSaves=true;});
  await p.fill("#cfg-llm-model","FAIL-DURING-FADE");
  ok("新修改立即取消淡出并清晰显示保存中",await status.evaluate(el=>el.classList.contains("saving") && Number(getComputedStyle(el).opacity)===1));
  await p.evaluate(()=>flushSettings().catch(()=>{}));
  ok("淡出中发生保存失败时错误和重试清晰可见",await status.evaluate(el=>el.classList.contains("error") && Number(getComputedStyle(el).opacity)===1 && !!el.querySelector("button")));
  await p.waitForTimeout(1600);
  ok("旧淡出定时器不会清除失败提示",(await status.textContent()).includes("保存失败") && await status.evaluate(el=>Number(getComputedStyle(el).opacity)===1));
  await p.evaluate(()=>{window.__settingsMock.delay=0;window.__settingsMock.failSaves=false;});
  await status.locator("button").click();await p.evaluate(()=>flushSettings());
  ok("重试成功重新完整显示提示",await status.evaluate(el=>el.classList.contains("saved") && Number(getComputedStyle(el).opacity)===1));
  await until(()=>status.evaluate(duringFade),1800,20);
  await p.evaluate(()=>{
    const el=document.getElementById("cfg-llm-model");el.focus();
    el.dispatchEvent(new CompositionEvent("compositionstart",{bubbles:true}));
    el.value="IME-AFTER-FADE";el.dispatchEvent(new InputEvent("input",{bubbles:true,isComposing:true}));
  });
  ok("淡出期间开始组合输入会清除旧成功提示",!(await status.textContent()).trim() && await p.evaluate(()=>MOCK_CFG.llm.model==="FAIL-DURING-FADE"));
  await p.locator("#cfg-llm-model").dispatchEvent("compositionend");await p.evaluate(()=>flushSettings());
  ok("组合输入完成后正常保存并完整显示",await p.evaluate(()=>MOCK_CFG.llm.model==="IME-AFTER-FADE") && await status.evaluate(el=>el.classList.contains("saved") && Number(getComputedStyle(el).opacity)===1));

  await p.evaluate(()=>window.__settingsMock.delay=300);
  await p.selectOption("#cfg-font","songti");await p.fill("#cfg-outdir","D:\\Demo\\Fade");await p.locator("#cfg-outdir").blur();
  await p.evaluate(()=>flushSettings());
  const output=p.locator("#save-output");
  await p.waitForTimeout(700);
  ok("较早的字体淡出不会淡化较晚的目录成功提示",await output.evaluate(el=>el.classList.contains("saved") && Number(getComputedStyle(el).opacity)===1));
  const outputFade=await until(()=>output.evaluate(duringFade),1200,20);
  await p.waitForTimeout(275);
  const later=await output.evaluate(visual);
  ok("较早提示清理时共用位置仍连续淡出",!!outputFade && later.phase.includes("fading") && later.opacity>0 && later.opacity<outputFade.opacity && later.text==="✓ 已保存");
  await until(()=>output.textContent().then(t=>!t.trim()));

  await p.emulateMedia({reducedMotion:"reduce"});
  await p.evaluate(()=>window.__settingsMock.delay=0);
  await p.fill("#cfg-llm-model","REDUCED-MOTION");await p.evaluate(()=>flushSettings());
  await until(()=>status.evaluate(el=>el.classList.contains("fading")),1800,20);
  ok("减少动态效果偏好下保持清晰且不执行淡出动画",await status.evaluate(el=>el.classList.contains("fading") && Number(getComputedStyle(el).opacity)===1 && el.getAnimations().length===0));
  await until(()=>status.textContent().then(t=>!t.trim()));
  ok("减少动态效果时仍按时清理提示",!(await status.textContent()).trim());
  ok("淡出回归无运行时错误",errors.length===0,errors.join(" | "));
  await p.close();
}


console.log("\n[20] 配置恢复、字体真实加载与合法并发回显");
{
 const p=await browser.newPage({viewport:{width:1040,height:900}});
 const errors=[];p.on("pageerror",e=>errors.push(String(e)));
 await p.goto(PAGE_URL,{waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
 await p.click('.nav-item[data-view="settings"]');
 for(let n=1;n<=8;n++) {
  await p.evaluate(async n=>{MOCK_CFG.llm.concurrency=n;await loadSettings();initSettingsBaseline();},n);
  ok(`并发 ${n} 正确回显`,await p.inputValue("#cfg-llm-conc")===String(n));
 }
 const fixturePath = "/assets/lxgw/files/lxgwwenkai-regular-subset-119.woff2";
 const redirect = await p.request.get(new URL("/userfonts/mock.ttf", PAGE_URL).href, {maxRedirects:0});
 ok("模拟字体重定向到仓库字体", redirect.status() === 302 && redirect.headers().location === fixturePath);
 const fixture = await p.request.get(new URL(fixturePath, PAGE_URL).href);
 const fontBytes = await fixture.body();
 ok("仓库测试字体响应有效", fixture.status() === 200 && fixture.headers()["content-type"] === "font/woff2" && fontBytes.subarray(0,4).toString("ascii") === "wOF2");
 await p.click("#font-import");await p.evaluate(()=>flushSettings());
 await until(()=>p.evaluate(()=>document.getElementById("font-preview").style.fontFamily.includes("ImportedPreview")));
 ok("导入字体已真实加载到浏览器",await p.evaluate(()=>[...document.fonts].some(f=>f.family.startsWith("ImportedPreview")&&f.status==="loaded")));
 ok("预览实际选择已加载的字体",await p.locator("#font-preview").evaluate(el=>el.style.fontFamily.includes("ImportedPreview")));
 await p.evaluate(async()=>{
  MOCK_CFG.user_fonts.push({internal_name:"MissingFont",display_name:"missing",file_name:"missing.ttf"});
  CFG.export.user_font="MissingFont";await loadFontSection();
 });
 await until(()=>p.locator("#font-preview-status").textContent().then(s=>s.includes("加载失败")));
 ok("字体加载失败明确提示并回退",(await p.locator("#font-preview-status").textContent()).includes("加载失败") && !(await p.locator("#font-preview").evaluate(el=>el.style.fontFamily.includes("ImportedPreview"))));
 ok("新交互无运行时错误",errors.length===0,errors.join(" | "));
 await p.close();
 const r=await browser.newPage({viewport:{width:1040,height:900}});
 await r.goto(PAGE_URL+"?corrupt",{waitUntil:"load"});await until(()=>r.evaluate(()=>window.__settingsMock.ready));
 await r.click('.nav-item[data-view="settings"]');
 ok("配置损坏时恢复入口可达",await r.locator("#config-recovery").isVisible());
 ok("恢复前禁止编辑与自动保存",await r.locator("#cfg-llm-model").isDisabled() && await r.evaluate(()=>!settingsReady && window.__settingsMock.calls.filter(c=>c.method==="save").length===0));
 ok("恢复前任务入口被阻止",await r.evaluate(async()=>{const promise=requireSettingsSaved();setTimeout(()=>document.querySelector("#modal-btns button")?.click(),30);return !(await promise)}));
 await r.click('[data-recover="reload"]');
 await until(()=>r.locator("#config-recovery-message").textContent().then(s=>s.includes("恢复失败")));
 ok("重新读取失败保留恢复入口",await r.locator("#config-recovery").isVisible() && await r.locator("#cfg-llm-model").isDisabled());
 await r.click('[data-recover="reset"]');
 ok("重建需要明确选择",await r.locator("#modal-scrim").isVisible() && await r.evaluate(()=>!window.__settingsMock.calls.some(c=>c.method==="recover"&&c.action==="reset")));
 await r.locator("#modal-btns button").nth(1).click();
 await r.click('[data-recover="restore_backup"]');
 await until(()=>r.locator("#config-recovery").isHidden());
 ok("恢复有效备份后重新启用编辑",await r.locator("#cfg-llm-model").isEnabled() && await r.evaluate(()=>settingsReady));
 await r.fill("#cfg-llm-model","RECOVERED-MODEL");await r.evaluate(()=>flushSettings());
 ok("恢复后正常持久化",await r.evaluate(()=>MOCK_CFG.llm.model==="RECOVERED-MODEL"));
 await r.close();
}

console.log("\n[21] 损坏恢复保留待保存输入与退出保护");
{
 const p=await browser.newPage({viewport:{width:1040,height:1000}});
 const errors=[];p.on("pageerror",e=>errors.push(String(e)));
 await p.goto(PAGE_URL,{waitUntil:"load"});await until(()=>p.evaluate(()=>window.__settingsMock.ready));
 await p.click('.nav-item[data-view="settings"]');
 await p.evaluate(()=>{
  ruleOpen=0;ruleEditing=true;renderRules();
  window.__settingsMock.recoveryConfig=JSON.parse(JSON.stringify(MOCK_CFG));
  window.__settingsMock.recoveryConfig.llm.model="BACKUP-MODEL";
  document.getElementById("cfg-llm-model").value="PENDING-MODEL";
  document.getElementById("cfg-llm-model").dispatchEvent(new Event("input",{bubbles:true}));
  CFG.rules.presets[0].draft_text="pending draft";saveRulesDraft(600);
  CFG.glossary.push({en:"pending term",zh:""});renderGlossary();saveGlossary(600);
  window.__settingsMock.corrupt=true;
 });
 await p.evaluate(()=>flushSettings().catch(()=>{}));
 ok("损坏后暂停保存但保留三个区域的补丁",await p.evaluate(()=>settingsRecoveryBlocked && settingsJobs.size===3));
 ok("损坏后保留输入并禁用正文编辑",await p.inputValue("#cfg-llm-model")==="PENDING-MODEL" && await p.locator("[data-rtext]").isDisabled());
 await p.evaluate(()=>{window.__evt["settings:before-close"]();});
 await until(()=>p.locator("#modal-scrim").isVisible());
 ok("损坏后仍有退出保护和明确放弃选项",await p.locator("#modal-btns button").count()===3);
 await p.locator("#modal-btns button").nth(1).click();
 await p.evaluate(()=>window.__settingsMock.recoveryFails=true);
 await p.click('[data-recover="restore_backup"]');
 await until(()=>p.locator("#config-recovery-message").textContent().then(s=>s.includes("恢复失败")));
 ok("恢复失败不丢失待保存输入",await p.evaluate(()=>settingsJobs.size===3) && await p.inputValue("#cfg-llm-model")==="PENDING-MODEL");
 await p.evaluate(()=>window.__settingsMock.recoveryFails=false);
 await p.click('[data-recover="restore_backup"]');
 await until(()=>p.locator("#config-recovery").isHidden());
 ok("恢复后编辑区覆盖回待保存字段",await p.inputValue("#cfg-llm-model")==="PENDING-MODEL" && await p.evaluate(()=>CFG.rules.presets[0].draft_text==="pending draft" && CFG.glossary.some(x=>x.en==="pending term" && !x.zh)));
 await p.waitForTimeout(750);
 ok("恢复后未自动覆盖备份，等待用户重试",await p.evaluate(()=>MOCK_CFG.llm.model==="BACKUP-MODEL" && settingsJobs.size===3 && [...settingsJobs.values()].every(j=>!!j.error && !j.ready)));
 await p.evaluate(()=>flushSettings(true));
 ok("显式重试后全部编辑持久化",await p.evaluate(()=>!settingsJobs.size && MOCK_CFG.llm.model==="PENDING-MODEL" && MOCK_CFG.rules.presets[0].draft_text==="pending draft" && MOCK_CFG.glossary_rows.some(x=>x.en==="pending term" && !x.zh)));
 await p.fill("#cfg-llm-model","DISCARD-MODEL");
 await p.evaluate(()=>window.__settingsMock.corrupt=true);
 await p.evaluate(()=>flushSettings().catch(()=>{}));
 await p.evaluate(()=>{window.__evt["settings:before-close"]();});
 await until(()=>p.locator("#modal-scrim").isVisible());
 await p.locator("#modal-btns button").nth(2).click();
 ok("只有明确放弃才清除待保存队列",await p.evaluate(()=>!settingsJobs.size && MOCK_CFG.llm.model==="PENDING-MODEL"));
 ok("损坏恢复无前端运行时错误",errors.length===0,errors.join(" | "));
 await p.close();
}

// ------------------------------------------------ 收尾
await browser.close();
server.close();
console.log(`\n════════ 通过 ${passed} · 失败 ${failed} ════════`);
process.exit(failed ? 1 : 0);
