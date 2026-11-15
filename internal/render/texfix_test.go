package render

import "testing"

// texfix.go 的规则锁定测试。三道准入门在此落地：
// 恒等性（合法公式逐字节不变）、等价性（每条规则的精确输出）、
// 幂等性（清洗两次 = 清洗一次）。真实语料对拍另在本地验证环境执行。

// mustSame 断言清洗后逐字节相同（不命中任何规则的输入）。
func mustSame(t *testing.T, tex string) {
	t.Helper()
	out, stats := sanitizeTex(tex)
	if out != tex {
		t.Fatalf("恒等性破坏:\n  入: %q\n  出: %q\n  命中: %v", tex, out, stats)
	}
	if len(stats) != 0 {
		t.Fatalf("合法公式不应命中规则: %q → %v", tex, stats)
	}
}

func mustEq(t *testing.T, in, want string) {
	t.Helper()
	out, _ := sanitizeTex(in)
	if out != want {
		t.Fatalf("规则输出不符:\n  入:  %q\n  出:  %q\n  期:  %q", in, out, want)
	}
	// 幂等：清洗两次输出一致。首个 \tag 参数内嵌 \tag 的形态按设计
	// 保留内层（交由降级），二次清洗会再命中多 \tag 规则但输出不变，
	// 故只断言输出收敛，不断言统计归零。
	out2, _ := sanitizeTex(out)
	if out2 != out {
		t.Fatalf("幂等性破坏:\n  一次: %q\n  两次: %q", out, out2)
	}
}

func TestIdentityValidFormulas(t *testing.T) {
	cases := []string{
		`x ^ { 2 } + y ^ { 2 } = z ^ { 2 }`,
		`\left( x + y \right) ^ { 2 }`,
		`\left\{ x \right\}`,
		`\left. x \right| _ { 0 }`,
		`\left\langle \psi | \phi \right\rangle`,
		`\rightarrow \leftarrow \leftrightarrow`,
		`\begin{array}{c} a & b \\ c & d \end{array}`,
		`\begin{array}{r} { a } \\ { b } \end{array}`,
		`\begin{aligned} x &= 1 \\ y &= 2 \end{aligned}`,
		`a \tag {12.2.36}`,                 // 单 \tag 合法，原样保留
		`\tag{\( \sigma _ { 1 } \)}`,       // \tag 参数含嵌套括号（整词与括号感知）
		`\int \limits _ { 0 } ^ { \pi } f`, // \limits 在运算符后合法
		`\int \nolimits _ { a } ^ { \infty }`,
		`\mathbb { R } \boldsymbol { x } \pmb { y }`,
		`c _ { 1 } , \dots , c _ { N }`,
		`\begin{array}{c} a \cr b \end{array}`, // \cr 是数组行终止符，不剥
		`100\% \& \text{fine}`,                 // \& 控制符号不受孤立 & 规则影响
	}
	for _, c := range cases {
		mustSame(t, c)
	}
}

func TestSynonymCommands(t *testing.T) {
	mustEq(t, `c _ { 1 } , \hdots , c _ { N }`, `c _ { 1 } , \dots , c _ { N }`)
	mustEq(t, `\int \displaylimits _ { a } ^ { \infty }`, `\int \limits _ { a } ^ { \infty }`)
	// 整词边界：\hdotsx 不是 \hdots
	mustSame(t, `\hdotsx`)
	mustSame(t, `\displaylimitsx`)
}

func TestNullCommands(t *testing.T) {
	mustEq(t, `\mathrm { x \normalfont ~ ) }`, `\mathrm { x  ~ ) }`)
	mustEq(t, `\noindent x ^ { 2 }`, ` x ^ { 2 }`)
	mustEq(t, `a \medskip b`, `a  b`)
}

func TestMultiTag(t *testing.T) {
	// 多 \tag 只留首个（KaTeX 硬限制每公式一个）
	mustEq(t, `a \tag{12.2.1} \\ b \tag{12.2.2}`, `a \tag{12.2.1} \\ b `)
	// 参数含嵌套花括号也要整体剥除
	mustEq(t, `x \tag {\( \sigma \)} + y \tag{2}`, `x \tag {\( \sigma \)} + y `)
	// 首个 \tag 的参数内嵌另一个 \tag：参数整体保留（内层 tag 随之保留），
	// 后续 tag 正常剥除——无守卫时 prev 越过内层起点，切片反序 panic
	mustEq(t, `\tag{a \tag{b}} + c \tag{d}`, `\tag{a \tag{b}} + c `)
}

func TestEnvironmentAlignment(t *testing.T) {
	// \end 与栈顶 \begin 名字错配 → 对齐
	mustEq(t, `\begin{array} { c } { x } \end{bmatrix}`,
		`\begin{array} { c } { x } \end{array}`)
	// 正确嵌套不受影响
	mustSame(t, `\begin{array}{c} \begin{bmatrix} 1 \\ 2 \end{bmatrix} \end{array}`)
	// 孤立 \end 剥除
	mustEq(t, `x + y \end{array}`, `x + y `)
	// \end{ } 参数含乱码（MinerU 产物 \end{\right.array}）→ 对齐栈顶
	mustEq(t, `\begin{array}{c} x \end{ \right.array }`,
		`\begin{array}{c} x \end{array}`)
}

func TestLeftRightPairing(t *testing.T) {
	// 未闭合 \left：剥命令保裸定界符
	mustEq(t, `a \left( b + c`, `a ( b + c`)
	mustEq(t, `\left\{ x`, `\{ x`)
	mustEq(t, `\left\langle x`, `\langle x`)
	// 孤立 \right：对称剥命令保裸定界符
	mustEq(t, `x \right| ^ { 2 }`, `x | ^ { 2 }`)
	// 先开先合的栈式配对：正常配对不受影响
	mustSame(t, `\left[ \left( x \right) \right]`)
}

func TestStrayAmpersand(t *testing.T) {
	// 环境外的孤立 & 非法（KaTeX 实证抛错），剥除
	mustEq(t, `a & b`, `a  b`)
	mustEq(t, `\text { a & b }`, `\text { a  b }`)
	// \& 是控制符号（分词阶段整体消费），不受影响
	mustSame(t, `Q \& A`)
	// 环境内的 & 是合法列分隔符
	mustSame(t, `\begin{array}{cc} a & b \end{array}`)
}

func TestRealDamagedSamples(t *testing.T) {
	// 真实损伤样本（扫描教材 MinerU 提取产物，逐条真机 KaTeX 验证过）：
	// 未闭合 \left 在数组内——数组结构保留，\left 转裸括号
	mustEq(t, `\begin{array} { r } { \left( x + y } \end{array}`,
		`\begin{array} { r } { ( x + y } \end{array}`)
	// 生造词 \equivq 不修（目标不唯一：可能是 \equiv 或 "\equiv q"），
	// 原样保留走渲染降级——等价性门禁的边界样本
	mustSame(t, `\hat { I } _ { j } \equivq`)
}

func TestFixerAggregation(t *testing.T) {
	fx := &texFixer{stats: TexFixStats{}}
	fx.fix(`a \hdots b`)
	fx.fix(`c \hdots d`)
	// 无命中公式不进 affected 口径
	fx.fix(`x ^ { 2 }`)
	if got := fx.stats["映射 \\hdots→\\dots"]; got != 2 {
		t.Fatalf("命中次数口径不符: %v", fx.stats)
	}
	if fx.affected != 2 {
		t.Fatalf("受影响公式数口径不符: %d", fx.affected)
	}
	// nil fixer：清洗照常执行，不panic
	var nilFixer *texFixer
	if out := nilFixer.fix(`a \hdots b`); out != `a \dots b` {
		t.Fatalf("nil fixer 清洗失败: %q", out)
	}
	// 零值 fixer：stats 惰性初始化，清洗照常执行
	zf := &texFixer{}
	zf.fix(`a \hdots b`)
	if zf.stats["映射 \\hdots→\\dots"] != 1 || zf.affected != 1 {
		t.Fatalf("零值 fixer 统计异常: %v / %d", zf.stats, zf.affected)
	}
}
