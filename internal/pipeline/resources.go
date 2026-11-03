package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	htmltoken "golang.org/x/net/html"
)

// Resolve existing parents too, so junctions cannot hide a nested destination.
func resolvedPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		r, e := filepath.EvalSymlinks(abs)
		if e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				r = filepath.Join(r, tail[i])
			}
			return filepath.Clean(r), nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", e
		}
		tail = append(tail, filepath.Base(abs))
		abs = parent
	}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resourceFiles(mdPath string) (map[string]string, error) {
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, err
	}
	root, err := resolvedPath(filepath.Dir(mdPath))
	if err != nil {
		return nil, err
	}
	var paths []string
	addHTML := func(raw []byte) {
		z := htmltoken.NewTokenizer(strings.NewReader(string(raw)))
		for {
			typ := z.Next()
			if typ == htmltoken.ErrorToken {
				return
			}
			if typ != htmltoken.StartTagToken && typ != htmltoken.SelfClosingTagToken {
				continue
			}
			token := z.Token()
			if token.Data == "img" {
				for _, a := range token.Attr {
					if a.Key == "src" {
						paths = append(paths, a.Val)
						break
					}
				}
			}
		}
	}
	doc := goldmark.DefaultParser().Parse(text.NewReader(data))
	_ = ast.Walk(doc, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.Image:
			paths = append(paths, string(node.Destination))
		case *ast.RawHTML:
			var raw []byte
			for i := 0; i < node.Segments.Len(); i++ {
				seg := node.Segments.At(i)
				raw = append(raw, seg.Value(data)...)
			}
			addHTML(raw)
		case *ast.HTMLBlock:
			raw := append([]byte(nil), node.Lines().Value(data)...)
			if node.HasClosure() {
				raw = append(raw, node.ClosureLine.Value(data)...)
			}
			addHTML(raw)
		}
		return ast.WalkContinue, nil
	})
	files := map[string]string{}
	for _, raw := range paths {
		u, e := url.Parse(raw)
		if e != nil {
			return nil, fmt.Errorf("无效图片地址: %q", raw)
		}
		if u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "data" || u.Host != "" {
			continue
		}
		if u.Scheme != "" {
			return nil, fmt.Errorf("本地图片不支持该地址类型: %q", raw)
		}
		local := filepath.FromSlash(u.Path)
		if local == "" || filepath.IsAbs(local) || strings.Contains(local, ":") {
			return nil, fmt.Errorf("图片须使用文档目录内的相对路径: %q", raw)
		}
		resolved, e := resolvedPath(filepath.Join(root, local))
		if e != nil {
			return nil, e
		}
		if !within(root, resolved) {
			return nil, fmt.Errorf("图片越出源文档目录: %q", raw)
		}
		info, e := os.Stat(resolved)
		if e != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("本地图片缺失或不是普通文件: %q", raw)
		}
		files[filepath.Clean(local)] = resolved
	}
	return files, nil
}

func archiveResources(ctx context.Context, mdPath string, destinations ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	files, err := resourceFiles(mdPath)
	if err != nil {
		return err
	}
	for rel, src := range files {
		for _, dst := range destinations {
			root, e := resolvedPath(dst)
			if e != nil {
				return e
			}
			target, e := resolvedPath(filepath.Join(dst, rel))
			if e != nil {
				return e
			}
			if !within(root, target) {
				return fmt.Errorf("图片归档目标越界: %s", rel)
			}
			if err = copyFileContext(ctx, src, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func fileHash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func resourceHashes(mdPath string) (map[string]string, error) {
	files, err := resourceFiles(mdPath)
	if err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for rel, path := range files {
		h, e := fileHash(path)
		if e != nil {
			return nil, e
		}
		hashes[filepath.ToSlash(rel)] = h
	}
	return hashes, nil
}
