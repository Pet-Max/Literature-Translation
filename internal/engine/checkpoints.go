// checkpoints.go — 断点续跑：每段译文即时落盘 JSONL。
//
// 断点文件格式（每行一个 JSON 对象）：
//
//	{"meta": {"key": "任务指纹"}}                        首行：规则/术语/模型指纹
//	{"i": 3, "h": "源段内容哈希", "zh": "……译文……"}   该段完成
//	{"i": 5, "failed": "占位符缺失 ⟦F7⟧"}              该段最终失败
//
// 重启同一任务时，仅当源段哈希与当前段一致才复用——源文本被校准/修改过
// 的情况下不会错误配对。任务指纹不一致（改过规则预设/术语表/模型）则
// 整体弃用旧进度重译，避免静默复用旧规则的译文。
//
// 兼容性与逐字节约定：
//   - chunk_hash 输入串与输出（sha1 hex 前 12 位）与历史断点格式完全一致；
//   - 写出行与历史断点文件逐字节一致（", "/": " 分隔符、键序、
//     不转义 HTML 字符），旧进度因此可继续复用；
//   - 历史实现在 Windows 上以文本模式写 \r\n，读取端两种行尾都认；
//     Go 端写入统一 \n。
package engine

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ChunkHash 源段内容哈希（sha1 hex 前 12 位，输入按 UTF-8 字节）。
func ChunkHash(text string) string {
	sum := sha1.Sum([]byte(text))
	return hex.EncodeToString(sum[:])[:12]
}

// jsonString 按历史断点文件的转义约定编码单个字符串（不转义 <>&；
// 已知差异：Go 无条件转义 U+2028/U+2029，且 \b/\f 用 \uXXXX 形式——
// 学术 MD 文本中不会出现）。
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := b.String()
	return out[:len(out)-1] // Encode 追加了换行
}

type doneRec struct{ h, zh string }

// Progress 断点进度。
type Progress struct {
	path        string
	cacheKey    string
	hasKey      bool
	metaStamped bool
	mu          sync.Mutex
	done        map[int]doneRec
	failed      map[int]string
}

// NewProgress 加载断点文件。cacheKey 为空串表示不写 meta 行、不做指纹
// 校验。文件不存在返回空进度。
func NewProgress(path, cacheKey string) (*Progress, error) {
	p := &Progress{
		path:     path,
		cacheKey: cacheKey,
		hasKey:   cacheKey != "",
		done:     make(map[int]doneRec),
		failed:   make(map[int]string),
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return nil, err
	}
	p.load(data)
	return p, nil
}

// load 解析 JSONL（容忍坏行）。识别顺序：meta → zh → failed。
func (p *Progress) load(data []byte) {
	stale := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024) // 单行译文可能很大
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue // JSONDecodeError → 跳过
		}
		if raw, ok := rec["meta"]; ok {
			if p.hasKey {
				var meta map[string]string
				key := ""
				if json.Unmarshal(raw, &meta) == nil {
					key = meta["key"]
				}
				if key != p.cacheKey {
					stale = true
				}
			}
		} else if raw, ok := rec["zh"]; ok {
			var zh string
			if json.Unmarshal(raw, &zh) != nil {
				continue
			}
			iRaw, ok := rec["i"]
			if !ok {
				continue
			}
			var i int
			if json.Unmarshal(iRaw, &i) != nil {
				continue
			}
			// 旧格式无 h 字段：哈希记为空，永不匹配（强制重译）
			h := ""
			if hRaw, ok := rec["h"]; ok {
				json.Unmarshal(hRaw, &h)
			}
			p.done[i] = doneRec{h: h, zh: zh}
			// 同段可能先失败后成功（重试成功），成功行覆盖失败标记
			delete(p.failed, i)
		} else if raw, ok := rec["failed"]; ok {
			var reason string
			if json.Unmarshal(raw, &reason) != nil {
				continue
			}
			iRaw, ok := rec["i"]
			if !ok {
				continue
			}
			var i int
			if json.Unmarshal(iRaw, &i) != nil {
				continue
			}
			p.failed[i] = reason
		}
	}
	if stale {
		p.done = make(map[int]doneRec)
		p.failed = make(map[int]string)
		if err := os.Remove(p.path); err != nil {
			// 删除失败（杀软/句柄占用，Windows 上常见）：截断兜底。
			// 只吞异常不截断的话，旧 meta 会留在文件头混进后续追加流，
			// 下一轮 resume 又整体作废、文件还只增不减
			_ = os.Truncate(p.path, 0)
		}
	}
}

// Reusable 仅当已记录且源段哈希与当前段一致时才可复用。
func (p *Progress) Reusable(i int, chunk string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.done[i]
	if !ok {
		return false
	}
	if _, bad := p.failed[i]; bad {
		return false
	}
	return rec.h == ChunkHash(chunk)
}

// Done 返回某段已记录的译文（主循环恢复用）。
func (p *Progress) Done(i int) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.done[i]
	return rec.zh, ok
}

// Record 记录该段完成并落盘。
func (p *Progress) Record(i int, chunk, zh string) {
	h := ChunkHash(chunk)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done[i] = doneRec{h: h, zh: zh}
	delete(p.failed, i)
	p.append(fmt.Sprintf(`{"i": %d, "h": %s, "zh": %s}`, i, jsonString(h), jsonString(zh)))
}

// RecordFailed 记录该段最终失败并落盘。
func (p *Progress) RecordFailed(i int, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failed[i] = reason
	p.append(fmt.Sprintf(`{"i": %d, "failed": %s}`, i, jsonString(reason)))
}

// append 追加一行（必要时先补 meta 首行）。磁盘满/文件被锁：断点丢失
// 但 run 继续（译文仍在内存，最终会写进 out_path）；调用栈上抛会杀掉
// 整轮翻译。
func (p *Progress) append(line string) {
	f, err := os.OpenFile(p.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[checkpoints] 断点写入失败（本次不落盘）：%v\n", err)
		return
	}
	defer f.Close()
	if p.hasKey && !p.metaStamped {
		if _, err := f.WriteString(`{"meta": {"key": ` + jsonString(p.cacheKey) + "}}\n"); err != nil {
			fmt.Fprintf(os.Stderr, "[checkpoints] 断点写入失败（本次不落盘）：%v\n", err)
			return
		}
		p.metaStamped = true
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		fmt.Fprintf(os.Stderr, "[checkpoints] 断点写入失败（本次不落盘）：%v\n", err)
	}
}
