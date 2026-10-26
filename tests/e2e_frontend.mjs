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
  ok("任务计数文案", (await page.locator("#queue-count").textContent()) === "4 个任务");
  ok("默认模式=自动", await page.locator('.mode-btn[data-mode="auto"]').evaluate((el) => el.classList.contains("active")));
  await until(() => page.locator("#sidebar-status").textContent().then((t) => t.includes("v1.0.1")));
  ok("版本状态就绪", (await page.locator("#sidebar-status").textContent()).includes("就绪"));
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
  ok("保存按钮可见", await page.locator("#btn-save-config").isVisible());
  ok("脏标记初始隐藏", await page.locator("#dirty-pill").evaluate((el) => !el.classList.contains("show")));

  // 脏监听在 DOMContentLoaded 后 800ms 才绑定，先等绑定窗口过去
  await page.waitForTimeout(1000);
  await page.locator("#cfg-bilingual").check({ force: true });
  await until(() => page.locator("#dirty-pill").evaluate((el) => el.classList.contains("show")));
  ok("改动后脏标记亮起", await page.locator("#dirty-pill.show").count() === 1);

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
  ok("无 JS 运行时错误", pageErrors.length === 0, pageErrors.join(" | "));
}

// ============ [8] FontList→null 回归（Go nil 切片曾炸断整条初始化链） ============
console.log("\n[8] FontList→null 回归");
{
  const page2 = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errors2 = [];
  page2.on("pageerror", (e) => errors2.push(String(e)));
  await page2.goto(PAGE_URL + "?fontnull", { waitUntil: "load" });
  // 修复后 null 按空列表处理：应用照常走完初始化链，状态栏到达「就绪」
  await until(() => page2.locator("#sidebar-status").textContent().then((t) => t.includes("v1.0.1")));
  const st2 = await page2.locator("#sidebar-status").textContent();
  ok("null 字体列表下状态就绪", st2.includes("就绪"), st2);
  ok("状态栏无「启动中/异常」残留", !st2.includes("启动中") && !st2.includes("异常"), st2);
  ok("规则卡仍渲染", (await page2.locator("#rule-list .rule-card").count()) === 2);
  ok("术语表仍渲染", (await page2.locator("#glossary-box .g-row:not(.head)").count()) === 1);
  ok("null 页无 JS 运行时错误", errors2.length === 0, errors2.join(" | "));
  await page2.close();
}

// ------------------------------------------------ 收尾
await browser.close();
server.close();
console.log(`\n════════ 通过 ${passed} · 失败 ${failed} ════════`);
process.exit(failed ? 1 : 0);
