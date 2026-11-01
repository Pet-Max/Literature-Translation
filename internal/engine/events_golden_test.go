// events_golden_test.go — M4 对拍：Go 引擎主循环 vs Python 边车母本
// （engine.py op_translate），在相同 mock LLM、相同输入、concurrency=1
// 下的进度事件流与最终结果逐字段一致；任务指纹 BuildCacheKey 与
// Python chunk_hash(json.dumps(..., sort_keys=True)) 逐字节一致。
//
// 黄金文件由 _v1.2.0_baseline/gen_events_golden.py 生成（驱动真实
// engine.py 子进程录制）。事件比较忽略键序与 "event":"progress" 包装。
package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type goldenEvent map[string]any

func TestTranslateEventStreamGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "events_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		MD    string `json:"md"`
		Mark  string `json:"mark"`
		Cases map[string]struct {
			Events []goldenEvent `json:"events"`
			Final  struct {
				Result struct {
					Total          int              `json:"total"`
					Done           int              `json:"done"`
					Failed         []SegmentFailure `json:"failed"`
					KeptReferences int              `json:"kept_references"`
					Warnings       []string         `json:"warnings"`
				} `json:"result"`
			} `json:"final"`
			// FailSub 不在黄金里——两种场景的 mock 行为写死在本测试，
			// 与 gen_events_golden.py 保持一致：all_pass 回显+标记；
			// mid_fail 对含 ZZFAILZZ 的段回「拒答占位符全丢」。
		} `json:"cases"`
		CacheKeys map[string]any `json:"cache_keys"`
	}
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}

	for caseName, want := range g.Cases {
		t.Run(caseName, func(t *testing.T) {
			failSub := ""
			if caseName == "mid_fail" {
				failSub = "ZZFAILZZ"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				body, _ := ioReadAll(r)
				if json.Unmarshal(body, &req) != nil {
					http.Error(w, "bad json", http.StatusBadRequest)
					return
				}
				user := ""
				for _, m := range req.Messages {
					if m.Role == "user" {
						user = m.Content
					}
				}
				content := user + g.Mark
				if failSub != "" && strings.Contains(user, failSub) {
					content = "拒答占位符全丢"
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
				})
			}))
			defer srv.Close()

			outPath := filepath.Join(t.TempDir(), "evt_zh.md")
			var gotEvents []Event
			res, err := Translate(TranslateOptions{
				MDText:         g.MD,
				OutPath:        outPath,
				ProgressPath:   outPath + ".progress.jsonl",
				SkipReferences: true,
				Bilingual:      false,
				LLM: LLMOptions{
					BaseURL: srv.URL, APIKey: "test-key",
					Model: "mock-model", Concurrency: 1,
				},
				OnEvent: func(ev Event) { gotEvents = append(gotEvents, ev) },
			})
			if err != nil {
				t.Fatal(err)
			}

			// 事件流逐事件逐字段比对
			if len(gotEvents) != len(want.Events) {
				t.Fatalf("事件数不一致 got=%d want=%d\n got: %+v\n want: %v",
					len(gotEvents), len(want.Events), gotEvents, want.Events)
			}
			for i, ge := range want.Events {
				if err := eventEqual(gotEvents[i], ge); err != nil {
					for j, e := range gotEvents {
						t.Logf("got 事件 #%d: %+v", j, e)
					}
					t.Fatalf("事件 #%d 不一致: %v", i, err)
				}
			}

			// 最终结果逐字段比对（out_path 是本测临时路径，单独断言非空）
			fr := want.Final.Result
			if res.Total != fr.Total || res.Done != fr.Done ||
				res.KeptReferences != fr.KeptReferences ||
				res.OutPath == "" ||
				!reflect.DeepEqual(res.Failed, fr.Failed) ||
				!reflect.DeepEqual(res.Warnings, fr.Warnings) {
				t.Fatalf("最终结果不一致\n got %+v\n want %+v", res, fr)
			}

			// 译文内容：LLM 标记句进入译文、文献条目保留原文
			zh, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if caseName == "all_pass" {
				if n := strings.Count(string(zh), "机翻译文冒烟标记"); n != 3 {
					t.Errorf("标记句 %d 处，期望 3（文献段保留原文不计）", n)
				}
				if !strings.Contains(string(zh), "Mock citation for testing purposes") {
					t.Errorf("文献条目未保留原文")
				}
			}
		})
	}
}

// eventEqual 逐字段核对 Go 事件与黄金事件（容忍键序/包装键差异）。
func eventEqual(got Event, want goldenEvent) error {
	check := func(key string, wantVal any) error {
		switch key {
		case "done":
			if got.Done != int(wantVal.(float64)) {
				return fmt.Errorf("done=%d want=%v", got.Done, wantVal)
			}
		case "total":
			if got.Total != int(wantVal.(float64)) {
				return fmt.Errorf("total=%d want=%v", got.Total, wantVal)
			}
		case "failed":
			if got.Failed != int(wantVal.(float64)) {
				return fmt.Errorf("failed=%d want=%v", got.Failed, wantVal)
			}
		case "done_idx":
			return intSliceErr("done_idx", got.DoneIdx, wantVal)
		case "failed_idx":
			return intSliceErr("failed_idx", got.FailedIdx, wantVal)
		case "sweep":
			if got.Sweep != wantVal.(bool) {
				return fmt.Errorf("sweep=%v want=%v", got.Sweep, wantVal)
			}
		}
		return nil
	}
	for _, k := range []string{"done", "total", "failed", "done_idx", "failed_idx", "sweep"} {
		if v, ok := want[k]; ok {
			if err := check(k, v); err != nil {
				return err
			}
		} else if k == "sweep" && got.Sweep {
			return fmt.Errorf("sweep=true 但黄金无该字段")
		}
	}
	return nil
}

func intSliceErr(name string, got []int, wantVal any) error {
	raw, _ := json.Marshal(wantVal)
	var want []int
	json.Unmarshal(raw, &want)
	if len(got) != len(want) {
		return fmt.Errorf("%s=%v want=%v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("%s=%v want=%v", name, got, want)
		}
	}
	return nil
}

func ioReadAll(r *http.Request) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}

// TestBuildCacheKeyGolden 任务指纹与 Python 母本逐字节一致。
func TestBuildCacheKeyGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "events_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		CacheKeys map[string]any `json:"cache_keys"`
	}
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	for key, wantAny := range g.CacheKeys {
		if strings.HasSuffix(key, "|system") {
			continue
		}
		want, _ := wantAny.(string)
		parts := strings.SplitN(key, "|", 3)
		bilingual, skipRefs := parts[1] == "true", parts[2] == "true"
		system := g.CacheKeys[key+"|system"].(string)
		if got := BuildCacheKey(system, bilingual, skipRefs, "mock-model"); got != want {
			t.Fatalf("cache_key/%s: got=%s want=%s", key, got, want)
		}
	}
}
