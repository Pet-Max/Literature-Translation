// 结构还原：HTML 表格 → Markdown 管道表格，<eq> → $…$。
package repair

import (
	"fmt"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------- 结构还原

// 表格与 <eq> 标签：MinerU 的 HTML 表格在 goldmark（unsafe 关闭）下被
// 静默丢弃，导出 PDF 时整张表消失——75 篇样本中 11 篇中招。转成
// Markdown 管道表格 + KaTeX 公式即可在现有渲染管线里活下来。
var (
	tableRe  = regexp.MustCompile(`(?s)<table[^>]*>.*?</table>`)
	trRe     = regexp.MustCompile(`(?si)<tr[^>]*>(.*?)</tr>`)
	cellRe   = regexp.MustCompile(`(?si)<t([dh])[^>]*>(.*?)</t[dh]>`)
	stripRe  = regexp.MustCompile(`(?si)</?(?:b|i|u|em|strong|sup|sub|small|center|font)[^>]*>`)
	entities = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&apos;", "'", "&nbsp;", " ",
	)
	cellWs = regexp.MustCompile(`\s+`)
)

// tablesToMarkdown 把文档内全部 HTML 表格转成 Markdown 管道表格。
// 单元格内 <eq>X</eq> → $X$；rowspan/colspan 按标准网格算法展开
// （跨行/跨列用重复/空单元格近似，保数据可见性）；单元格内竖线转义。
func tablesToMarkdown(md string, st *Stats) string {
	return tableRe.ReplaceAllStringFunc(md, func(block string) string {
		grid := expandGrid(block)
		if len(grid) == 0 || len(grid[0]) == 0 {
			return block
		}
		var b strings.Builder
		for r, row := range grid {
			b.WriteString("|")
			for _, c := range row {
				b.WriteString(" " + c + " |")
			}
			b.WriteString("\n")
			if r == 0 {
				b.WriteString("|")
				for range row {
					b.WriteString(" --- |")
				}
				b.WriteString("\n")
			}
		}
		st.Tables++
		st.detail(fmt.Sprintf("HTML 表格 → Markdown 表格（%d 行 × %d 列）", len(grid), len(grid[0])))
		return b.String()
	})
}

// expandGrid 解析表格块为文本网格（处理 rowspan/colspan）。
func expandGrid(block string) [][]string {
	type cell struct {
		text             string
		rowspan, colspan int
	}
	var rows [][]cell
	for _, rm := range trRe.FindAllStringSubmatch(block, -1) {
		var cs []cell
		for _, cm := range cellRe.FindAllStringSubmatch(rm[1], -1) {
			c := cell{text: cleanCell(cm[2])}
			c.rowspan = attrNum(cm[0], "rowspan")
			c.colspan = attrNum(cm[0], "colspan")
			cs = append(cs, c)
		}
		rows = append(rows, cs)
	}
	if len(rows) == 0 {
		return nil
	}
	nCols := 0
	for _, cs := range rows {
		w := 0
		for _, c := range cs {
			w += c.colspan
		}
		if w > nCols {
			nCols = w
		}
	}
	if nCols == 0 {
		return nil
	}
	grid := [][]string{}
	used := map[[2]int]bool{}
	for r, cs := range rows {
		if r >= len(grid) {
			grid = append(grid, make([]string, nCols))
		}
		c := 0
		for _, cell := range cs {
			for c < nCols && used[[2]int{r, c}] {
				c++
			}
			if c >= nCols {
				break
			}
			for i := 0; i < cell.rowspan; i++ {
				for j := 0; j < cell.colspan; j++ {
					rr, cc := r+i, c+j
					for rr >= len(grid) {
						grid = append(grid, make([]string, nCols))
					}
					if cc < nCols && !used[[2]int{rr, cc}] {
						used[[2]int{rr, cc}] = true
						// 跨行/跨列以重复内容近似，保持表格自含可读
						grid[rr][cc] = cell.text
					}
				}
			}
			c += cell.colspan
		}
	}
	return grid
}

func attrNum(s, name string) int {
	re := regexp.MustCompile(`(?i)` + name + `\s*=\s*["']?(\d+)`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 1
	}
	n := 1
	fmt.Sscanf(m[1], "%d", &n)
	if n < 1 {
		return 1
	}
	if n > 64 {
		return 64
	}
	return n
}

// cleanCell 单元格文本化：<eq>X</eq>→$X$，剥内联标签，解实体，转义竖线。
func cleanCell(s string) string {
	s = eqInnerRe.ReplaceAllStringFunc(s, func(m string) string {
		return "$" + eqInnerRe.FindStringSubmatch(m)[1] + "$"
	})
	s = stripRe.ReplaceAllString(s, "")
	s = entities.Replace(s)
	s = cellWs.ReplaceAllString(strings.TrimSpace(s), " ")
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

// eqInnerRe MinerU 用 <eq>X</eq> 包 LaTeX；渲染管线只认 $…$。
var eqInnerRe = regexp.MustCompile(`(?s)<eq>(.*?)</eq>`)

// eqToMath 把表格之外残留的 <eq>（图题、正文碎表）一并转为 $…$。
func eqToMath(md string, st *Stats) string {
	n := 0
	res := eqInnerRe.ReplaceAllStringFunc(md, func(m string) string {
		n++
		return "$" + eqInnerRe.FindStringSubmatch(m)[1] + "$"
	})
	if n > 0 {
		st.Syms += n
		st.detail(fmt.Sprintf("<eq> 标签 → $…$ 公式 ×%d", n))
	}
	return res
}
