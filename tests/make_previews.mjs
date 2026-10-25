// make_previews.mjs — 产出 README 界面预览图：主界面（空队列演示态）× 深浅双主题。
// 运行：node tests/make_previews.mjs   （可选 CHROME_PATH 指定浏览器）
// 与 e2e_frontend.mjs 共用 overlay 服务器；浏览器探测逻辑同款。
import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { startOverlayServer, PREVIEW_PORT } from "./overlay_server.mjs";

const require = createRequire(import.meta.url);
const pw = require("playwright-core");
const ROOT = path.dirname(fileURLToPath(import.meta.url));
const IMAGES = path.resolve(ROOT, "..", "images");

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

const server = await startOverlayServer();
const browser = await pw.chromium.launch({ executablePath: findBrowser() || undefined, headless: true });
const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, deviceScaleFactor: 2 });
await page.goto(`http://127.0.0.1:${PREVIEW_PORT}/index.html?empty`, { waitUntil: "load" });
await page.waitForTimeout(1800); // 渲染 + 侧栏版本状态就绪

// 浅色为默认主题，先出浅色图
await page.screenshot({ path: path.join(IMAGES, "preview-tasks-light.png") });
await page.click("#btn-theme");
await page.waitForTimeout(600); // 主题过渡完成
await page.screenshot({ path: path.join(IMAGES, "preview-tasks-dark.png") });

await browser.close();
server.close();
console.log("previews written to", IMAGES);
