// render.js — 导出页的 KaTeX 挂载脚本（由 render.Build 以外链引用）。
// 抽成独立资源而非内联 <script>：导出页的 CSP 要求 script-src 'self'，
// 源 Markdown 里的原始 HTML 已被 goldmark 默认转义，双保险防脚本注入。
// 渲染完成（或失败）后置 __ktxDone，导出模块轮询它。
// 逐公式渲染：每个公式独立调用 katex.render，单个失败不影响其他公式
// （auto-render 的整文档遍历存在静默部分失败的问题）。
// 字体先预加载再渲染，消除"先渲染后测量"的宽度竞态。
window.__renderStats = { rendered: 0, errors: [] };
// fitWideFormulas — 行间公式宽度自适应：超宽者缩字号，其余不动。
// 溢出只看 el.scrollWidth − clientWidth：KaTeX 的隐藏 MathML 副本
// （.katex-mathml）自身 1px 且 overflow:hidden，其溢出不进入滚动区域，
// 天然不参与测量。旧版逐节点量 getBoundingClientRect 会把这份隐形副本
// 一起量进去，而 Chrome 对矩阵/数组的 MathML 排版明显偏宽，导致肉眼
// 很短的公式也被误判超宽而缩小。
// 每轮先把字号复位到自然值再测：任何一轮的误判（字体晚到、窗口瞬时
// 宽度、滚动条出现）都会在下一轮复原，缩小不再"粘住"，多次调用不累积。
function fitWideFormulas() {
  document.querySelectorAll(".ktx-disp").forEach(function (el) {
    if (!el.dataset.baseFs) {
      el.dataset.baseFs = parseFloat(getComputedStyle(el).fontSize) || 15;
    }
    var base = parseFloat(el.dataset.baseFs);
    el.style.fontSize = "";
    // 居中的 nowrap 内容向两侧等量溢出，滚动区只计入右侧一半，
    // 故按 (clientWidth + 2*overflow) 估算公式实际宽度；迭代至多
    // 8 轮消除 scrollWidth 取整误差，每轮都从当前字号重新收敛。
    for (var i = 0; i < 8; i++) {
      var over = el.scrollWidth - el.clientWidth;
      if (over <= 2) { return; }
      var cur = parseFloat(el.style.fontSize) || base;
      var fs = cur * el.clientWidth / (el.clientWidth + 2 * over);
      if (fs < base * 0.55 || fs <= 6) {
        el.style.fontSize = ""; // 低于下限宁可横向滚动，不出不可读小字
        return;
      }
      el.style.fontSize = fs + "px";
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
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(function () { fitWideFormulas(); });
  }
  var fitTimer = null; // 预览场景窗口尺寸变化后重新适配（导出页无副作用）
  window.addEventListener("resize", function () {
    clearTimeout(fitTimer);
    fitTimer = setTimeout(fitWideFormulas, 150);
  });
  setTimeout(function () { // 兜底：字体接口异常也要出公式
    if (!window.__ktxDone) { renderAll(); fitWideFormulas(); window.__ktxDone = true; }
  }, 5000);
});
