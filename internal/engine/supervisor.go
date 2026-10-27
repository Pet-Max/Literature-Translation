// Package engine 管理 Python 翻译引擎边车子进程。
//
// 协议见 engine/engine.py：stdin 发 JSON 行请求，stdout 收 JSON 行
// 响应（带 id）与事件（带 event 字段）。stderr 转发到本进程日志。
package engine

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Event 是边车推送的进度事件。DoneIdx/FailedIdx 为已完成/失败段的
// 0 基编号（前端用于逐段着色）；Sweep 表示正处收尾补翻阶段
// （旧版引擎不带这些字段，保持零值）。
type Event struct {
	Done      int   `json:"done"`
	Total     int   `json:"total"`
	Failed    int   `json:"failed"`
	DoneIdx   []int `json:"done_idx"`
	FailedIdx []int `json:"failed_idx"`
	Sweep     bool  `json:"sweep"`
}

type rpcLine struct {
	ID     *int64          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	// 事件行是扁平的：{"event":"progress","done":12,"total":34,"failed":1}。
	// event 用 string、计数用指针区分「未携带」与 0，整个行才能正确反序列化。
	Event     string `json:"event"`
	Done      *int   `json:"done"`
	Total     *int   `json:"total"`
	Failed    *int   `json:"failed"`
	DoneIdx   *[]int `json:"done_idx"`
	FailedIdx *[]int `json:"failed_idx"`
	Sweep     *bool  `json:"sweep"`
}

// Supervisor 持有一个边车进程实例。非线程安全的并发调用由内部
// 互斥保证串行（翻译任务本身在边车里已并发）。
type Supervisor struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex
	nextID int64
	pend   map[int64]chan *rpcLine

	// Events 广播进度事件；边车停止后关闭。
	Events chan Event

	stderr io.Writer
	done   chan struct{}
}

// FindPython 定位 Python 解释器：
// LITERATURE_PYTHON 环境变量 → exe 同目录 runtime\python.exe（打包形态）→ PATH 上的 python。
func FindPython() (string, error) {
	if p := os.Getenv("LITERATURE_PYTHON"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		embedded := filepath.Join(filepath.Dir(exe), "runtime", "python.exe")
		if _, err := os.Stat(embedded); err == nil {
			return embedded, nil
		}
	}
	if p, err := exec.LookPath("python"); err == nil {
		return p, nil
	}
	return "", errors.New("未找到 Python 解释器（可设置 LITERATURE_PYTHON 指定路径）")
}

// Start 拉起边车进程。engineDir 为 engine/ 目录绝对路径。
func Start(engineDir string, stderr io.Writer) (*Supervisor, error) {
	py, err := FindPython()
	if err != nil {
		return nil, err
	}
	script := filepath.Join(engineDir, "engine.py")
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("找不到引擎脚本 %s: %w", script, err)
	}

	cmd := exec.Command(py, script)
	cmd.Dir = engineDir
	// PYTHONUTF8=1：强制边车全程 UTF-8——stdio 管道在非中文代码页的
	// Windows（如 cp1252）上会按 ANSI 解码 Go 发来的 UTF-8 JSON，
	// 中文文本变成孤立代理字符，引擎回写时直接崩（surrogates not allowed）。
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONUTF8=1")
	// GUI 主程序自身无控制台，Windows 会给控制台子进程（python.exe）新开一个
	// 黑窗。CREATE_NO_WINDOW 让引擎边车静默运行；stdio 走自建管道，不受影响。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if stderr != nil {
		cmd.Stderr = stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 Python 引擎失败: %w", err)
	}

	s := &Supervisor{
		cmd:    cmd,
		stdin:  stdin,
		pend:   make(map[int64]chan *rpcLine),
		Events: make(chan Event, 64),
		stderr: stderr,
		done:   make(chan struct{}),
	}
	go s.readLoop(stdout)
	return s, nil
}

// readLoop 分发行：有 id 的交给对应等待者，有 event 的进 Events。
func (s *Supervisor) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4*1024*1024), 16*1024*1024) // 单段译文可达 MB 级
	defer close(s.done)
	defer close(s.Events)
	for sc.Scan() {
		var line rpcLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue
		}
		if line.Event != "" {
			ev := Event{}
			if line.Done != nil {
				ev.Done = *line.Done
			}
			if line.Total != nil {
				ev.Total = *line.Total
			}
			if line.Failed != nil {
				ev.Failed = *line.Failed
			}
			if line.DoneIdx != nil {
				ev.DoneIdx = *line.DoneIdx
			}
			if line.FailedIdx != nil {
				ev.FailedIdx = *line.FailedIdx
			}
			if line.Sweep != nil {
				ev.Sweep = *line.Sweep
			}
			select {
			case s.Events <- ev:
			default: // 订阅者不读也不阻塞引擎
			}
			continue
		}
		if line.ID != nil {
			s.mu.Lock()
			ch, ok := s.pend[*line.ID]
			delete(s.pend, *line.ID)
			s.mu.Unlock()
			if ok {
				ch <- &line
			}
		}
	}
}

// Call 发起一次 RPC 并等待响应。
func (s *Supervisor) Call(op string, payload any, timeout time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan *rpcLine, 1)
	s.pend[id] = ch
	req, err := json.Marshal(map[string]any{"id": id, "op": op, "payload": payload})
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	_, err = s.stdin.Write(append(req, '\n'))
	s.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("发送给引擎失败: %w", err)
	}

	select {
	case resp := <-ch:
		if !resp.OK {
			return nil, fmt.Errorf("引擎报错: %s", resp.Error)
		}
		return resp.Result, nil
	case <-s.done:
		return nil, errors.New("引擎进程已退出")
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pend, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("引擎响应超时（%s）", timeout)
	}
}

// Ping 自检。
func (s *Supervisor) Ping() error {
	_, err := s.Call("ping", map[string]any{}, 15*time.Second)
	return err
}

// Stop 关闭 stdin 让引擎自然退出，超时强杀。
func (s *Supervisor) Stop() {
	if s.stdin != nil {
		s.stdin.Close()
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		s.cmd.Process.Kill()
	}
}
