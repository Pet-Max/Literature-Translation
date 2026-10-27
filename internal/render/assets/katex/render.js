// render.js — 导出页的 KaTeX 挂载脚本（由 render.Build 以外链引用）。
// 抽成独立资源而非内联 <script>：导出页的 CSP 要求 script-src 'self'，
// 源 Markdown 里的原始 HTML 已被 goldmark 默认转义，双保险防脚本注入。
// 渲染完成（或失败）后置 __ktxDone，导出模块轮询它。
// 逐公式渲染：每个公式独立调用 katex.render，单个失败不影响其他公式
// （auto-render 的整文档遍历存在静默部分失败的问题）。
// 字体先预加载再渲染，消除"先渲染后测量"的宽度竞态。
window.__renderStats = { rendered: 0, errors: [] };
function fitWideFormulas() {
  document.querySelectorAll(".ktx-disp").forEach(function (el) {
    var box = el.getBoundingClientRect();
    var maxRight = 0;
    el.querySelectorAll("*").forEach(function (n) {
      var r = n.getBoundingClientRect();
      if (r.width > 0 && r.right > maxRight) { maxRight = r.right; }
    });
    var overflow = maxRight - box.right;
    if (overflow > 2) {
      var base = parseFloat(getComputedStyle(el).fontSize) || 15;
      var fs = Math.max(6, base * (box.width / (box.width + overflow)));
      if (fs < base) {
        el.style.fontSize = fs + "px";
        fitWideFormulas(); // 缩放后再测一轮，直至收敛
      }
    }
  });
}
function renderAll() {
  document.querySelectorAll(".ktx, .ktx-disp").forEach(function (el) {
    var tex = el.getAttribute("data-tex");
    if (!tex || el.dataset.done) { return; }
    el.dataset.done = "1";
    try {
      katex.render(tex, el, {
        displayMode: el.classList.contains("ktx-disp"),
        throwOnError: false,
        macros: { "\\pmb": "\\boldsymbol", "\\bm": "\\boldsymbol" }
      });
      if (el.querySelector(".merror")) {
        window.__renderStats.errors.push("merror: " + el.textContent.slice(0, 40));
      } else {
        window.__renderStats.rendered++;
      }
    } catch (e) {
      window.__renderStats.errors.push(String(e).slice(0, 80));
    }
  });
}
document.addEventListener("DOMContentLoaded", function () {
  var loads = [];
  if (document.fonts && document.fonts.load) {
    ["1em KaTeX_Main", "italic 1em KaTeX_Math", "bold italic 1em KaTeX_Math",
     "bold 1em KaTeX_Main", "1em KaTeX_Size1", "1em KaTeX_Size2",
     "1em KaTeX_Size3", "1em KaTeX_Size4", "1em KaTeX_AMS",
     "1em KaTeX_Caligraphic"].forEach(function (f) {
      loads.push(document.fonts.load(f));
    });
    loads.push(document.fonts.load('1em "LXGW WenKai"', document.body.textContent.slice(0, 60000)));
  }
  Promise.all(loads).catch(function () {}).then(function () {
    setTimeout(function () {
      renderAll();
      fitWideFormulas();
      setTimeout(function () { fitWideFormulas(); window.__ktxDone = true; }, 250);
    }, 30);
  });
  setTimeout(function () { // 兜底：字体接口异常也要出公式
    if (!window.__ktxDone) { renderAll(); fitWideFormulas(); window.__ktxDone = true; }
  }, 5000);
});
