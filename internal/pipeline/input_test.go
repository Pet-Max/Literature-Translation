package pipeline

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"literature-translation/internal/config"
)

func TestTaskInputIsStableAndCleaned(t *testing.T) {
	d := t.TempDir()
	md, image := filepath.Join(d, "paper.md"), filepath.Join(d, "fig.svg")
	os.WriteFile(md, []byte("![figure](fig.svg)"), 0600)
	os.WriteFile(image, []byte("image A"), 0600)
	s, err := snapshotInput(context.Background(), md, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	os.WriteFile(image, []byte("image B"), 0600)
	if err := s.check(context.Background()); err == nil {
		t.Fatal("changed image accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(s.dir, "fig.svg")); string(b) != "image A" {
		t.Fatal("snapshot image changed")
	}
	os.WriteFile(md, []byte("source B"), 0600)
	if err := s.check(context.Background()); err == nil {
		t.Fatal("changed source accepted")
	}
	s.Close()
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatal("input snapshot leaked", err)
	}
	if _, err := os.Stat(md); err != nil {
		t.Fatal("original removed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := snapshotInput(ctx, md, true); err == nil || s != nil {
		t.Fatal("cancelled task created input")
	}
}

func TestPDFChangedBeforeUploadStopsLocally(t *testing.T) {
	pdf := filepath.Join(t.TempDir(), "paper.pdf")
	os.WriteFile(pdf, []byte("%PDF-source-A"), 0600)
	cfg := config.Default()
	cfg.MinerU.Token = "mock-token"
	p, _ := New(cfg, Callbacks{Stage: func(stage string) {
		if stage == "解析中" {
			os.WriteFile(pdf, []byte("%PDF-source-B"), 0600)
		}
	}})
	old := http.DefaultClient
	var requests atomic.Int32
	http.DefaultClient = &http.Client{Transport: rejectExternalTransport{&requests}}
	defer func() { http.DefaultClient = old }()
	_, err := p.RunPDF(context.Background(), pdf, "", nil)
	if err == nil || !strings.Contains(err.Error(), "源文件发生变化") || requests.Load() != 0 {
		t.Fatal("changed source reached upload", err, requests.Load())
	}
}

type rejectExternalTransport struct{ requests *atomic.Int32 }

func (tr rejectExternalTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.requests.Add(1)
	return nil, fmt.Errorf("unexpected network call in local source check")
}

type localMinerUTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (tr localMinerUTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "mineru.net" && r.URL.Host != tr.target.Host {
		return nil, fmt.Errorf("unexpected external request: %s", r.URL.Host)
	}
	r = r.Clone(r.Context())
	u := *r.URL
	r.URL = &u
	if u.Host == "mineru.net" {
		r.URL.Scheme = tr.target.Scheme
		r.URL.Host = tr.target.Host
		r.URL.Path = strings.TrimPrefix(u.Path, "/api/v4")
	}
	return tr.base.RoundTrip(r)
}

// Runs the public PDF entry used by CLI, including parse, translate and local PDF printing.
func TestPDFActualPipelineSourceSnapshot(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	for _, replace := range []bool{false, true} {
		t.Run(fmt.Sprint("replace=", replace), func(t *testing.T) {
			d := t.TempDir()
			pdf := filepath.Join(d, "paper.pdf")
			original := []byte("%PDF-mock-source-A")
			os.WriteFile(pdf, original, 0600)
			var zipBody bytes.Buffer
			zw := zip.NewWriter(&zipBody)
			f, _ := zw.Create("paper/full.md")
			fmt.Fprint(f, "This is the complete English source for a local mock translation test.")
			zw.Close()
			var server *httptest.Server
			var uploaded atomic.Value
			var calls atomic.Int32
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/file-urls/batch":
					fmt.Fprintf(w, `{"code":0,"data":{"batch_id":"b","file_urls":[%q]}}`, server.URL+"/upload")
				case "/upload":
					body, _ := io.ReadAll(r.Body)
					uploaded.Store(body)
					if replace {
						os.WriteFile(pdf, []byte("%PDF-source-B"), 0600)
					}
				case "/zip":
					w.Write(zipBody.Bytes())
				case "/chat/completions":
					calls.Add(1)
					var req struct {
						Messages []struct{ Role, Content string }
					}
					json.NewDecoder(r.Body).Decode(&req)
					text := ""
					for _, m := range req.Messages {
						if m.Role == "user" {
							text = m.Content
						}
					}
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text + "\n\n（本地模拟译文）"}}}})
				default:
					fmt.Fprintf(w, `{"code":0,"data":{"extract_result":[{"state":"done","full_zip_url":%q}]}}`, server.URL+"/zip")
				}
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			oldClient := http.DefaultClient
			http.DefaultClient = &http.Client{Transport: localMinerUTransport{u, http.DefaultTransport}}
			defer func() { http.DefaultClient = oldClient }()
			cfg := config.Default()
			cfg.MinerU.Token = "mock-token"
			cfg.LLM.BaseURL = server.URL
			cfg.LLM.APIKey = "mock-key"
			cfg.LLM.Model = "mock"
			cfg.Repair.Spelling = false
			cfg.Repair.Symbols = false
			p, _ := New(cfg, Callbacks{})
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			res, err := p.RunPDF(ctx, pdf, "", nil)
			if uploaded.Load() == nil || !bytes.Equal(uploaded.Load().([]byte), original) {
				t.Fatal("upload not based on original snapshot")
			}
			if replace {
				if err == nil || !strings.Contains(err.Error(), "源文件发生变化") || calls.Load() != 0 {
					t.Fatal("changed source continued translation", err, calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatal("CLI PDF full pipeline failed", err)
			}
			if res.Done != res.Total || len(res.Failed) != 0 || calls.Load() == 0 {
				t.Fatal("mock translation incomplete", res)
			}
			if b, e := os.ReadFile(res.OutPDF); e != nil || !bytes.HasPrefix(b, []byte("%PDF")) {
				t.Fatal("missing PDF", e)
			}
			m, e := readRunMeta(metadataPath(res.ZHMD, nil))
			if e != nil || m.Schema != 2 || !m.Complete {
				t.Fatal("invalid completion metadata", m, e)
			}
			h, _ := fileHash(pdf)
			if m.SourceHash != h || m.SourceHash == m.InputHash {
				t.Fatal("PDF and Markdown identity conflated")
			}
		})
	}
}

func TestPDFWithoutArchiveSourceIdentity(t *testing.T) {
	d := t.TempDir()
	pdf, md, zh := filepath.Join(d, "source.pdf"), filepath.Join(d, "full.md"), filepath.Join(d, "zh.md")
	os.WriteFile(pdf, []byte("%PDF-source"), 0600)
	os.WriteFile(md, []byte("English source"), 0600)
	os.WriteFile(zh, []byte("translated text"), 0600)
	p, _ := New(config.Default(), Callbacks{})
	if _, err := p.beginRun(context.Background(), pdf, md, zh, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.finishRun(context.Background(), pdf, md, zh, nil, &translateOutcome{Total: 1, Done: 1}); err != nil {
		t.Fatal("unmodified CLI PDF rejected:", err)
	}
}

func TestImageExamplesAreNotResources(t *testing.T) {
	md := filepath.Join(t.TempDir(), "example.md")
	for _, example := range []string{
		"```html\n<img src=\"example.png\">\n```",
		"Inline `<img src=\"example.png\">`",
		"<!-- <img src=\"example.png\"> -->",
		"<!--\n<img src=\"example.png\">\n-->",
	} {
		os.WriteFile(md, []byte(example), 0600)
		files, err := resourceFiles(md)
		if err != nil || len(files) != 0 {
			t.Fatalf("code/comment treated as image: %q: %v", example, err)
		}
	}
	for _, example := range []string{"<img src=\"missing.png\">", "![image](missing.png)"} {
		os.WriteFile(md, []byte(example), 0600)
		if _, err := resourceFiles(md); err == nil || !strings.Contains(err.Error(), "缺失") {
			t.Fatal("real missing image accepted", err)
		}
	}
}
