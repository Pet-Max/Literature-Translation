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
  var list = Array.prototype.slice.call(document.querySelectorAll(".ktx-disp"));
  if (!list.length) { return; }
  // 读写分离：先批量复位并读取初始度量，减少大文档交替读写引起的强制重排
  list.forEach(function (el) {
    if (!el.dataset.baseFs) {
      el.dataset.baseFs = parseFloat(getComputedStyle(el).fontSize) || 15;
    }
    el.style.fontSize = "";
  });
  list.forEach(function (el) {
    var base = parseFloat(el.dataset.baseFs);
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
// fitWideOverflow — 打印横向溢出收口：表格与代码块实测越出 .content 打印
// 盒者逐级适配，装得下的元素零改动（恒等）。触发条件是运行时实测盒缘
// 越界，无阈值、无文档特征。预览窗口同样生效——宽表在预览中原本也被
// 窗口边缘裁切且无滚动条，压缩即改进。
//   表格三级：层1 .tbl-fit（缩字号+紧凑 padding+长词可断）→ 层2
//   table-layout:fixed + 定宽（横向不可能再溢出，数据零丢失硬保底）。
//   代码块：不断词保语义，按比例缩字号至最长行适配，硬底 6px。
// 每轮先复位再实测，多轮调用不累积（与 fitWideFormulas 同模式）。
function fitWideOverflow() {
  var clip = document.querySelector(".content").getBoundingClientRect();
  if (!clip.width) { return; }
  document.querySelectorAll("table").forEach(function (t) {
    t.classList.remove("tbl-fit");
    t.style.tableLayout = "";
    t.style.width = "";
    var r = t.getBoundingClientRect();
    if (r.right > clip.right + 1 || r.left < clip.left - 1) {
      t.classList.add("tbl-fit");
      r = t.getBoundingClientRect();
      if (r.right > clip.right + 1 || r.left < clip.left - 1) {
        t.style.tableLayout = "fixed";
        t.style.width = "calc(100% - 4px)";
      }
    }
  });
  document.querySelectorAll("pre").forEach(function (p) {
    p.style.fontSize = "";
    for (var i = 0; i < 8 && p.scrollWidth > p.clientWidth + 2; i++) {
      var cur = parseFloat(getComputedStyle(p).fontSize) || 14;
      p.style.fontSize = Math.max(6, cur * p.clientWidth / p.scrollWidth) + "px";
    }
  });
}
// fitPrintFormulas — 打印期第二阶段公式适配（只在导出打印通道调用，
// 屏幕预览不调用、行为不变）：fitWideFormulas 有 0.55× 可读性下限，
// 极宽行间公式在下限处仍会横向溢出，屏幕滚动无妨、打印即裁。对仍
// 越界者继续缩放（无下限，硬底 6px）；行内 nowrap 公式越界者单条缩放。
// 已知边界：表格单元格内溢出单元格但未越打印盒的公式不处理；缩至
// 6px 硬底仍溢出者仍会被页边裁切。
function fitPrintFormulas() {
  var clip = document.querySelector(".content").getBoundingClientRect();
  if (!clip.width) { return; }
  var disps = [];
  document.querySelectorAll(".ktx-disp").forEach(function (el) {
    if (el.scrollWidth > el.clientWidth + 2) {
      disps.push({ el: el, fs: parseFloat(getComputedStyle(el).fontSize) || 15 });
    }
  });
  disps.forEach(function (item) {
    var cur = item.fs;
    for (var i = 0; i < 8 && item.el.scrollWidth > item.el.clientWidth + 2; i++) {
      cur = Math.max(6, cur * 0.85);
      item.el.style.fontSize = cur + "px";
    }
  });
  var inlines = [];
  document.querySelectorAll(".ktx").forEach(function (el) {
    var r = el.getBoundingClientRect();
    if (r.right > clip.right + 1) {
      inlines.push({ el: el, fs: parseFloat(getComputedStyle(el).fontSize) || 15 });
    }
  });
  inlines.forEach(function (item) {
    var cur = item.fs;
    for (var i = 0; i < 6; i++) {
      var r = item.el.getBoundingClientRect();
      if (r.right <= clip.right + 1) { return; }
      cur = Math.max(6, cur * 0.8);
      item.el.style.fontSize = cur + "px";
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
        throwOnError: true,
        macros: { "\\pmb": "\\boldsymbol", "\\bm": "\\boldsymbol" }
      });
      if (el.querySelector(".katex-error, .merror")) {
        window.__renderStats.errors.push("merror: " + el.textContent.slice(0, 40));
      } else {
        window.__renderStats.rendered++;
      }
    } catch (e) {
      window.__renderStats.errors.push(String(e).slice(0, 80));
      el.classList.add("formula-error");
      el.setAttribute("role", "alert");
      el.textContent = "公式渲染失败：" + tex;
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
      fitWideOverflow();
      setTimeout(function () { fitWideFormulas(); fitWideOverflow(); window.__ktxDone = true; }, 250);
    }, 30);
  });
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(function () { fitWideFormulas(); fitWideOverflow(); });
  }
  var fitTimer = null; // 预览场景窗口尺寸变化后重新适配（导出页无副作用）
  window.addEventListener("resize", function () {
    clearTimeout(fitTimer);
    fitTimer = setTimeout(function () { fitWideFormulas(); fitWideOverflow(); }, 150);
  });
  setTimeout(function () { // 兜底：字体接口异常也要出公式
    if (!window.__ktxDone) { renderAll(); fitWideFormulas(); fitWideOverflow(); window.__ktxDone = true; }
  }, 5000);
});
