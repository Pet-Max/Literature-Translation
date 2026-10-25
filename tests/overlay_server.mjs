// overlay_server.mjs — e2e 与截图脚本共用的静态服务器：以 frontend/dist 为主，
// 注入 gui_shim mock 壳（定义 window.go），/assets 直通渲染器嵌入资源。
// gui_preview 不存任何前端拷贝，测试与截图永远打真实前端。
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const PREVIEW_DIR = path.join(ROOT, "gui_preview"); // 只放 gui_shim.js
const DIST_DIR = path.resolve(ROOT, "..", "frontend", "dist");
const RENDER_ASSETS = path.resolve(ROOT, "..", "internal", "render", "assets");
export const PREVIEW_PORT = 8890;

const MIME = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8", ".json": "application/json",
  ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon",
  ".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
};

export function startOverlayServer(port = PREVIEW_PORT) {
  const send = (res, file, type) => fs.readFile(file, (err, data) => {
    if (err) { res.writeHead(404); return res.end("not found"); }
    res.writeHead(200, { "Content-Type": type || MIME[path.extname(file).toLowerCase()] || "application/octet-stream" });
    res.end(data);
  });
  const guard = (root, file) => path.normalize(file).startsWith(path.normalize(root));
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const p = decodeURIComponent(new URL(req.url, "http://x").pathname);
      if (p === "/favicon.ico") { res.writeHead(204); return res.end(); }
      if (p === "/" || p === "/index.html") {
        // dist 原文件 + 注入 mock 壳（gui_shim 定义 window.go，必须在 main.js 之前）
        return fs.readFile(path.join(DIST_DIR, "index.html"), "utf8", (err, html) => {
          if (err) { res.writeHead(404); return res.end("not found"); }
          html = html.replace("<body>", '<body>\n<script src="/gui_shim.js"></script>');
          res.writeHead(200, { "Content-Type": MIME[".html"] });
          res.end(html);
        });
      }
      if (p === "/gui_shim.js") return send(res, path.join(PREVIEW_DIR, "gui_shim.js"));
      if (p.startsWith("/assets/")) {
        const file = path.join(RENDER_ASSETS, p.slice("/assets".length));
        if (!guard(RENDER_ASSETS, file)) { res.writeHead(403); return res.end(); }
        return send(res, file);
      }
      const file = path.join(DIST_DIR, p);
      if (!guard(DIST_DIR, file)) { res.writeHead(403); return res.end(); }
      send(res, file);
    });
    srv.listen(port, "127.0.0.1", () => resolve(srv));
  });
}
