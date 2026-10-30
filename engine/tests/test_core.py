"""引擎纯逻辑单测：python -m unittest discover -s engine/tests -v"""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import segmenter
import validator
from freezer import freeze, placeholders, unfreeze


class TestFreezer(unittest.TestCase):
    def test_roundtrip_inline_math(self):
        src = "不等式 $C_0 + \\omega^2 C_\\infty$ 给出下界。"
        f = freeze(src)
        self.assertEqual(placeholders(f.text), [1])
        self.assertIn("$C_0", f.mapping[1])
        self.assertEqual(unfreeze(f.text, f.mapping), src)

    def test_roundtrip_display_math(self):
        src = "正文。\n\n$$\nS_z(\\omega) \\le \\frac{1}{\\pi}\n$$\n\n后续。"
        f = freeze(src)
        self.assertEqual(len(f.mapping), 1)
        self.assertEqual(unfreeze(f.text, f.mapping), src)

    def test_display_before_inline(self):
        # $$ 必须先于 $ 冻结，否则行间公式会被拆成两个行内
        src = "看 $$a+b$$ 与 $c$ 的关系。"
        f = freeze(src)
        self.assertEqual(len(f.mapping), 2)
        self.assertEqual(f.mapping[1], "$$a+b$$")
        self.assertEqual(f.mapping[2], "$c$")
        self.assertEqual(unfreeze(f.text, f.mapping), src)

    def test_table_and_image(self):
        src = ('图示：![Fig 1](images/fig1.png)\n\n'
               '<table><tr><td>Value</td><td>1</td></tr></table>')
        f = freeze(src)
        self.assertEqual(len(f.mapping), 2)
        # 表格整体先冻结（长块优先），图片随后
        self.assertTrue(f.mapping[1].startswith("<table"))
        self.assertIn("fig1.png", f.mapping[2])
        self.assertEqual(unfreeze(f.text, f.mapping), src)

    def test_code_block(self):
        src = "如下：\n```python\nprint('$x$ 不是公式')\n```\n结束。"
        f = freeze(src)
        # 代码块整体冻结，内部的 $x$ 不得再单独占位
        self.assertEqual(len(f.mapping), 1)
        self.assertEqual(unfreeze(f.text, f.mapping), src)

    def test_escaped_dollar_not_frozen(self):
        src = r"价格是 \$5 而不是 5 美元。"
        f = freeze(src)
        self.assertEqual(len(f.mapping), 0)
        self.assertEqual(f.text, src)

    def test_multiline_inline_math_not_matched(self):
        # 行内公式不得跨行（跨行的 $$ 才合法）
        src = "a $x\ny$ b"
        f = freeze(src)
        self.assertEqual(len(f.mapping), 0)

    def test_equation_heavy_paragraph(self):
        # PSD 论文典型句式
        src = ("The bound Eq. (1) holds with $C_0$ and $C_\\infty$, "
               "see Fig. 2 and $S_z(\\omega)$ in Eq. (4).")
        f = freeze(src)
        self.assertEqual(len(placeholders(f.text)), 3)
        self.assertEqual(unfreeze(f.text, f.mapping), src)


class TestSegmenter(unittest.TestCase):
    def test_split_by_heading(self):
        md = "# A\npara1\n\npara2\n\n# B\npara3"
        chunks = segmenter.split(md)
        self.assertEqual(len(chunks), 2)
        self.assertIn("# A", chunks[0])
        self.assertIn("# B", chunks[1])

    def test_max_chars_respected(self):
        para = "字" * 100
        md = "\n\n".join(para for _ in range(100))  # 10000+ 字符
        chunks = segmenter.split(md, max_chars=1000)
        self.assertGreater(len(chunks), 1)
        for c in chunks:
            self.assertLessEqual(len(c), 1010)

    def test_heading_goes_with_following_text(self):
        md = "# 标题\n\n内容段落。"
        chunks = segmenter.split(md)
        self.assertEqual(len(chunks), 1)
        self.assertTrue(chunks[0].startswith("#"))

    def test_empty(self):
        self.assertEqual(segmenter.split("   \n  "), [])

    def test_join_preserves_content(self):
        md = "# A\n\n第一段。\n\n第二段。\n\n## B\n\n第三段。"
        chunks = segmenter.split(md)
        rejoined = "\n\n".join(chunks)
        for piece in ("# A", "第一段。", "第二段。", "## B", "第三段。"):
            self.assertIn(piece, rejoined)

    def test_hard_split_keeps_display_math_whole(self):
        # 超长块里含单个超限 $$ 公式：宁可整段超长也不从公式中间切断，
        # 否则 $$ 不配对、freeze 匹配不到，裸 LaTeX 被模型改写
        giant = "$$\n" + ("x+y=z " * 800) + "\n$$"
        md = "前导段落文字。\n\n" + giant + "\n\n后缀段落文字。"
        chunks = segmenter.split(md, max_chars=300)
        formula_chunk = None
        for c in chunks:
            f = freeze(c)
            # 冻结后不得残留 $$（公式必须整体成占位符，而非被切断裸奔）
            self.assertNotIn("$$", f.text)
            if len(f.mapping) == 1:
                formula_chunk = c
        # 超限公式所在段超出 max_chars 但保持完整
        self.assertIsNotNone(formula_chunk)
        self.assertIn(giant, formula_chunk)

    def test_hard_split_cut_avoids_math(self):
        # 多个公式交错在超长块里：每个切出的段都不得含有未配对的 $
        body = "。".join(f"第 {i} 步满足 $x_{i}^2 > {i}$ 以及 $y_{i} < {i}$。" * 3
                        for i in range(200))
        chunks = segmenter.split(body, max_chars=500)
        self.assertGreater(len(chunks), 1)
        for c in chunks:
            f = freeze(c)
            # 段内所有 $ 公式成对冻结成功（无裸 $ 残留）
            self.assertNotIn("$", f.text)


class TestValidator(unittest.TestCase):
    def test_pass(self):
        src = "把 ⟦F1⟧ 代入 ⟦F2⟧ 得到结论。"
        dst = "将 ⟦F1⟧ 代入 ⟦F2⟧ 可得结论。"
        self.assertEqual(validator.validate(src, dst), [])

    def test_missing_placeholder(self):
        src = "把 ⟦F1⟧ 代入 ⟦F2⟧。"
        dst = "将 ⟦F1⟧ 代入公式。"
        reasons = validator.validate(src, dst)
        self.assertTrue(any("缺失" in r and "F2" in r for r in reasons))

    def test_extra_placeholder(self):
        src = "把 ⟦F1⟧ 代入。"
        dst = "将 ⟦F1⟧ 代入 ⟦F9⟧ 中。"
        reasons = validator.validate(src, dst)
        self.assertTrue(any("多出" in r for r in reasons))

    def test_heading_count(self):
        src = "# 标题\n\n正文 ⟦F1⟧。"
        dst = "# 译文标题\n\n正文 ⟦F1⟧。\n\n## 多出来的标题"
        reasons = validator.validate(src, dst)
        self.assertTrue(any("标题数" in r for r in reasons))

    def test_too_short(self):
        src = "长原文。" * 60 + " ⟦F1⟧"
        dst = "太短"
        reasons = validator.validate(src, dst)
        self.assertTrue(any("异常简短" in r for r in reasons))

    def test_final_dollar_odd(self):
        md = "正文 $x+y 未闭合"
        self.assertTrue(any("奇数" in r for r in validator.validate_final(md)))

    def test_final_begin_end(self):
        md = r"$$\begin{aligned} a &= b \\ \end{align}$$"
        reasons = validator.validate_final(md)
        self.assertTrue(any("begin/\\\\end" in r or "配对" in r for r in reasons))

    def test_final_leftover_placeholder(self):
        md = "正文 ⟦F3⟧ 残留"
        self.assertTrue(any("残留" in r for r in validator.validate_final(md)))


if __name__ == "__main__":
    unittest.main()


class TestReferenceDetector(unittest.TestCase):
    """参考文献甄别器：段首 [N] + 编号递增 + 特征复核"""

    def make(self, enabled=True):
        import engine
        return engine.make_reference_detector(enabled)

    REFS = [
        "[2] R. D. Yates and D. J. Goodman, Probability and stochastic processes "
        "(John Wiley & Sons, 2014).",
        "[3] N. Wiener, Generalized harmonic analysis, Acta Math. 55, 117 (1930).",
        "[4] A. Dechant and E. Lutz, Aging Wiener-Khinchin theorem, "
        "Phys. Rev. Lett. 115, 080603 (2015).",
    ]

    def test_accepts_ref_sequence(self):
        d = self.make()
        for r in self.REFS:
            self.assertTrue(d(r))

    def test_rejects_body_without_bracket_head(self):
        d = self.make()
        body = ("The bound Eq. (1) also holds for out-of-equilibrium systems, "
                "as shown in [17, 18] and (2023) studies with A. B. initials.")
        self.assertFalse(d(body))

    def test_rejects_break_in_sequence(self):
        d = self.make()
        self.assertTrue(d(self.REFS[0]))          # [2] 起始
        self.assertTrue(d(self.REFS[2]))          # [4]：间隔2，容忍（合并条目场景）
        self.assertFalse(d(self.REFS[1]))         # [3] 小于上一个编号 → 拒绝
        self.assertFalse(d("[9] N. X. Author, J. Abbrev. 1, 2 (2020)."))  # 跳距过大 → 拒绝

    def test_rejects_range_bracket_head(self):
        d = self.make()
        self.assertTrue(d(self.REFS[0]))          # [2]
        # 段首是 [17, 18]（行内引用形式）：正则要求 ] 后有空格且捕获纯数字
        self.assertFalse(d("[17, 18] entropy production estimates follow."))
        # 补上 [3] 后序列继续
        self.assertTrue(d(self.REFS[1]))

    def test_rejects_featureless_entry(self):
        d = self.make()
        self.assertTrue(d(self.REFS[0]))          # [2]
        # 无作者缩写、无年份 → 不满足特征复核
        self.assertFalse(d("[3] some text without features"))
        self.assertTrue(d(self.REFS[1]))          # 状态未污染，[3] 仍接受

    def test_disabled(self):
        d = self.make(False)
        for r in self.REFS:
            self.assertFalse(d(r))

    def test_too_long_rejected(self):
        d = self.make()
        long_entry = "[2] R. D. Yates (2014). " + "x" * 900
        self.assertFalse(d(long_entry))


class TestProgressReload(unittest.TestCase):
    """回归：先失败后成功的段，重启后不得再被 failed 标记卡住"""

    def test_failed_then_success_reload(self):
        import os
        import tempfile

        from checkpoints import Progress

        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "progress.jsonl")
            pr = Progress(p)
            pr.record_failed(0, "占位符缺失")
            pr.record(0, "chunk-source", "译文内容")
            # 模拟重启：重新从 JSONL 加载
            pr2 = Progress(p)
            self.assertNotIn(0, pr2.failed, "成功行应清除失败标记")
            self.assertTrue(pr2.reusable(0, "chunk-source"))
            # 源段内容变化（校准后）→ 不得复用
            self.assertFalse(pr2.reusable(0, "chunk-source-changed"))


class TestTableStructureValidation(unittest.TestCase):
    def test_table_intact_passes(self):
        # 冻结态：单元格公式已是占位符，两侧竖线结构一致
        src = "表：\n\n| Model | ⟦F1⟧ |\n| --- | --- |\n| A | 1 |\n"
        dst = "表：\n\n| 模型 | ⟦F1⟧ |\n| --- | --- |\n| A | 1 |\n"
        self.assertEqual(validator.validate(src, dst), [])

    def test_broken_column_fails(self):
        src = "| Model | ⟦F1⟧ |\n| --- | --- |\n| A | 1 |"
        dst = "| 模型 | ⟦F1⟧ |\n| --- | --- |\n| A | 1 缺竖线"
        reasons = validator.validate(src, dst)
        self.assertTrue(any("表格结构" in r for r in reasons))

    def test_row_count_change_fails(self):
        src = "| A | 1 |\n| B | 2 |"
        dst = "| A | 1 |"
        reasons = validator.validate(src, dst)
        self.assertTrue(any("表格结构" in r for r in reasons))

    def test_leading_ket_prose_not_table(self):
        # 行首狄拉克记号不是表格行（本库实测 57 行 |p⟩ = … 形态）；
        # 模型重排换行让 |00⟩ 落到行首也不得误报
        src = "态 |00⟩ 与 |01⟩ 构成基。"
        dst = "|00⟩ 与 |01⟩ 构成基。"
        self.assertEqual(validator.validate(src, dst), [])

    def test_table_with_ket_cells_still_checked(self):
        # 表格单元格里含狄拉克记号时仍须正常校验
        src = "| 态 | 值 |\n| --- | --- |\n| |00⟩ | 1 |"
        dst = "| 态 | 值 |\n| --- | --- |\n| |00⟩ |"
        reasons = validator.validate(src, dst)
        self.assertTrue(any("表格结构" in r for r in reasons))
