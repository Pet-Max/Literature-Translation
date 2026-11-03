// migration_test.go — 历史格式回归：以既有引擎在黄金
// 样本上的输出为基准（internal/engine/testdata/*.json，由
// _v1.2.0_baseline/gen_golden.py 生成），逐字节核对当前引擎行为。
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readGolden(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读黄金文件 %s: %v", name, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("解析黄金文件 %s: %v", name, err)
	}
	return m
}

func goldenTexts(t *testing.T) map[string]string {
	t.Helper()
	m := readGolden(t, "inputs_golden.json")
	texts, _ := m["texts"].(map[string]any)
	out := make(map[string]string)
	for k, v := range texts {
		out[k], _ = v.(string)
	}
	return out
}

// ---------------------------------------------------------------- segmenter

func TestSegmenterGolden(t *testing.T) {
	g := readGolden(t, "segmenter_golden.json")
	texts := goldenTexts(t)
	for name, entryAny := range g {
		entry, _ := entryAny.(map[string]any)
		text, ok := texts[name]
		if !ok {
			t.Fatalf("黄金文件引用了未知输入 %q", name)
		}
		for mcStr, chunksAny := range entry {
			var want []string
			raw, _ := json.Marshal(chunksAny)
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatalf("%s/mc=%s: 解析黄金段列表: %v", name, mcStr, err)
			}
			var mc int
			for _, c := range mcStr {
				mc = mc*10 + int(c-'0')
			}
			got := Split(text, mc)
			if len(got) != len(want) {
				t.Fatalf("%s/mc=%s: 段数不一致 got=%d want=%d", name, mcStr, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s/mc=%s: 第 %d 段逐字节不一致\n GOT %q\n WANT %q", name, mcStr, i, got[i], want[i])
				}
			}
		}
	}
}

// ---------------------------------------------------------------- freezer

func TestFreezerGolden(t *testing.T) {
	g := readGolden(t, "freezer_golden.json")
	texts := goldenTexts(t)
	for name, entryAny := range g {
		entry, _ := entryAny.(map[string]any)
		text, ok := texts[name]
		if !ok {
			t.Fatalf("黄金文件引用了未知输入 %q", name)
		}
		f := Freeze(text, 1)

		if f.Text != entry["frozen_text"].(string) {
			t.Fatalf("%s: 冻结文本不一致\n GOT %q\n WANT %q", name, f.Text, entry["frozen_text"].(string))
		}
		wantMap := map[int]string{}
		for k, v := range entry["mapping"].(map[string]any) {
			n := 0
			for _, c := range k {
				n = n*10 + int(c-'0')
			}
			wantMap[n] = v.(string)
		}
		if len(f.Mapping) != len(wantMap) {
			t.Fatalf("%s: mapping 大小不一致 got=%d want=%d", name, len(f.Mapping), len(wantMap))
		}
		for k, v := range wantMap {
			if got, ok := f.Mapping[k]; !ok || got != v {
				t.Fatalf("%s: mapping[%d] 不一致 got=%q want=%q", name, k, got, v)
			}
		}

		// spans（rune 坐标，Python 字符坐标）
		var wantSpans [][2]int
		raw, _ := json.Marshal(entry["spans"])
		if err := json.Unmarshal(raw, &wantSpans); err != nil {
			t.Fatalf("%s: 解析黄金 spans: %v", name, err)
		}
		gotSpans := ProtectedSpans(text)
		if len(gotSpans) != len(wantSpans) {
			t.Fatalf("%s: spans 数不一致 got=%d want=%d", name, len(gotSpans), len(wantSpans))
		}
		for i, ws := range wantSpans {
			if gotSpans[i].Start != ws[0] || gotSpans[i].End != ws[1] {
				t.Fatalf("%s: spans[%d] 不一致 got=[%d,%d] want=[%d,%d]", name, i, gotSpans[i].Start, gotSpans[i].End, ws[0], ws[1])
			}
		}

		// placeholders
		var wantPhs []float64
		raw, _ = json.Marshal(entry["placeholders"])
		json.Unmarshal(raw, &wantPhs)
		gotPhs := Placeholders(f.Text)
		if len(gotPhs) != len(wantPhs) {
			t.Fatalf("%s: 占位符数不一致 got=%d want=%d", name, len(gotPhs), len(wantPhs))
		}
		for i := range wantPhs {
			if gotPhs[i] != int(wantPhs[i]) {
				t.Fatalf("%s: 占位符[%d] 不一致 got=%d want=%d", name, i, gotPhs[i], int(wantPhs[i]))
			}
		}

		// roundtrip
		if entry["roundtrip_ok"].(bool) {
			if back := Unfreeze(f.Text, f.Mapping); back != text {
				t.Fatalf("%s: 回填结果与原文不一致", name)
			}
		}
	}
}

// ---------------------------------------------------------------- validator

func TestValidatorGolden(t *testing.T) {
	g := readGolden(t, "validator_golden.json")
	cases := g["cases"].(map[string]any)

	valCases, _ := cases["validate"].(map[string]any)
	for name, caseAny := range valCases {
		c, _ := caseAny.(map[string]any)
		var want []string
		raw, _ := json.Marshal(g["validate"].(map[string]any)[name])
		json.Unmarshal(raw, &want)
		got := Validate(c["src"].(string), c["dst"].(string))
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("validate/%s 不一致\n GOT %q\n WANT %q", name, got, want)
		}
	}

	finalCases, _ := cases["final"].(map[string]any)
	for name, caseAny := range finalCases {
		c, _ := caseAny.(map[string]any)
		var want []string
		raw, _ := json.Marshal(g["final"].(map[string]any)[name])
		json.Unmarshal(raw, &want)
		got := ValidateFinal(c["md"].(string))
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("final/%s 不一致\n GOT %q\n WANT %q", name, got, want)
		}
	}
}

// ---------------------------------------------------------------- checkpoints

func TestChunkHashGolden(t *testing.T) {
	g := readGolden(t, "checkpoints_golden.json")
	hashes, _ := g["hashes"].(map[string]any)
	// 输入串由 gen_golden.py 的 hash_inputs 定义；这里用确定性重构：
	// 黄金文件里存的是 name→hash，Go 侧同输入串写死在本测试中。
	inputs := map[string]string{
		"empty":       "",
		"hello":       "hello",
		"cjk":         "量子热机 ΛΩψ 与 🧪☕",
		"multiline":   "line1\nline2\r\nline3",
		"html_chars":  "<table> & \"quotes\" 'single' \\backslash\\",
		"placeholder": "⟦F1⟧ ⟦F23⟧ τ=$\\hbar\\omega$",
		"long":        strings.Repeat("x", 5000),
	}
	for name, s := range inputs {
		want, _ := hashes[name].(string)
		if got := ChunkHash(s); got != want {
			t.Fatalf("chunk_hash/%s: got=%s want=%s", name, got, want)
		}
	}
}

func TestCheckpointsJSONL(t *testing.T) {
	g := readGolden(t, "checkpoints_golden.json")
	chunksAny, _ := readGolden(t, "inputs_golden.json")["checkpoint_chunks"].([]any)
	var chunks []string
	raw, _ := json.Marshal(chunksAny)
	json.Unmarshal(raw, &chunks)

	wantJSONL := g["jsonl"].(string)
	wantOld := g["old_jsonl"].(string)
	key, _ := g["cache_key"].(string)

	dir := t.TempDir()

	// 1) 读 Python 写的断点文件：指纹一致、哈希匹配才可复用
	pyFile := filepath.Join(dir, "py.jsonl")
	if err := os.WriteFile(pyFile, []byte(wantJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := NewProgress(pyFile, key)
	if err != nil {
		t.Fatalf("加载 Python 断点: %v", err)
	}
	if !p.Reusable(0, chunks[0]) || !p.Reusable(1, chunks[1]) || !p.Reusable(2, chunks[2]) {
		t.Fatal("Python 写入的完成段应全部可复用")
	}
	if zh, ok := p.Done(1); !ok || zh != "第二段重试成功：ΛΩψ。" {
		t.Fatalf("Done(1) = %q, %v", zh, ok)
	}
	// 源文本改动 → 哈希不匹配 → 不可复用
	if p.Reusable(0, chunks[0]+" ") {
		t.Fatal("源文本被修改后不应复用")
	}
	// 指纹不一致 → 整体作废 + 文件删除
	pWrong, err := NewProgress(pyFile, "wrong-key")
	if err != nil {
		t.Fatal(err)
	}
	if pWrong.Reusable(0, chunks[0]) || pWrong.Reusable(2, chunks[2]) {
		t.Fatal("指纹不一致时不应复用任何段")
	}
	if _, err := os.Stat(pyFile); !os.IsNotExist(err) {
		t.Fatal("指纹不一致后断点文件应被删除")
	}

	// 2) Go 写同样序列 → 行内容与 Python 写的逐行一致（行尾 \r\n 归一）
	goFile := filepath.Join(dir, "go.jsonl")
	pg, err := NewProgress(goFile, key)
	if err != nil {
		t.Fatal(err)
	}
	pg.Record(0, chunks[0], "第一段译文：$E=mc^2$ 保留。")
	pg.RecordFailed(1, "占位符缺失 ⟦F2⟧")
	pg.Record(2, chunks[2], "第三段：⟦F1⟧ 回填后 <b>粗体</b> 保留。")
	pg.Record(1, chunks[1], "第二段重试成功：ΛΩψ。")
	goData, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	norm := func(s string) []string {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		var out []string
		for _, line := range strings.Split(s, "\n") {
			if strings.TrimSpace(line) != "" {
				out = append(out, line)
			}
		}
		return out
	}
	wantLines, gotLines := norm(wantJSONL), norm(string(goData))
	if len(gotLines) != len(wantLines) {
		t.Fatalf("Go 写出行数不一致 got=%d want=%d\n GOT %q\n WANT %q", len(gotLines), len(wantLines), gotLines, wantLines)
	}
	for i := range wantLines {
		if gotLines[i] != wantLines[i] {
			t.Fatalf("Go 写出第 %d 行逐字节不一致\n GOT %q\n WANT %q", i, gotLines[i], wantLines[i])
		}
	}

	// 3) 旧版格式（无 meta、部分无 h 字段）：无 h 的段永不匹配
	oldFile := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(oldFile, []byte(wantOld), 0o644); err != nil {
		t.Fatal(err)
	}
	po, err := NewProgress(oldFile, "")
	if err != nil {
		t.Fatal(err)
	}
	if po.Reusable(0, chunks[0]) {
		t.Fatal("旧格式无 h 字段的段不应复用")
	}
	if po.Reusable(1, chunks[1]) {
		t.Fatal("失败段不应复用")
	}
}

// ---------------------------------------------------------------- prompt

func TestPromptGolden(t *testing.T) {
	g := readGolden(t, "prompt_golden.json")
	cases, _ := g["cases"].(map[string]any)
	for name, caseAny := range cases {
		c, _ := caseAny.(map[string]any)
		var glossary [][]string
		if c["glossary"] != nil {
			raw, _ := json.Marshal(c["glossary"])
			json.Unmarshal(raw, &glossary)
		}
		// Python 端 None → Go 空串；入参含空格填充，BuildSystem 内部 strip
		sysIn, _ := c["system"].(string)
		userIn, _ := c["user"].(string)
		got := BuildSystem(glossary, sysIn, userIn)
		want, _ := c["out"].(string)
		if got != want {
			t.Fatalf("prompt/%s 不一致\n GOT %q\n WANT %q", name, got, want)
		}
	}
	var reasons []string
	raw, _ := json.Marshal(g["correction_reasons"])
	json.Unmarshal(raw, &reasons)
	wantOut, _ := g["correction_out"].(string)
	if got := BuildCorrection(reasons); got != wantOut {
		t.Fatalf("correction 不一致\n GOT %q\n WANT %q", got, wantOut)
	}
}
