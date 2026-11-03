package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// inputSnapshot owns only temporary task inputs, never the user's source files.
type inputSnapshot struct {
	Path, original, dir, hash string
	resources                 map[string]string
	markdown                  bool
}

func snapshotInput(ctx context.Context, source string, markdown bool) (*inputSnapshot, error) {
	dir, err := os.MkdirTemp("", "literature-task-input-*")
	if err != nil {
		return nil, err
	}
	s := &inputSnapshot{original: source, dir: dir, Path: filepath.Join(dir, filepath.Base(source)), markdown: markdown}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	if err = copyFileContext(ctx, source, s.Path); err != nil {
		return nil, err
	}
	if s.hash, err = fileHash(s.Path); err != nil {
		return nil, err
	}
	if markdown {
		if err = archiveResources(ctx, source, dir); err != nil {
			return nil, err
		}
		if s.resources, err = resourceHashes(s.Path); err != nil {
			return nil, err
		}
	}
	if err = s.check(ctx); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func (s *inputSnapshot) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h, err := fileHash(s.original)
	if err != nil || h != s.hash {
		return fmt.Errorf("任务期间源文件发生变化或无法读取，请重新运行")
	}
	if s.markdown {
		resources, err := resourceHashes(s.original)
		if err != nil || !reflect.DeepEqual(resources, s.resources) {
			return fmt.Errorf("任务期间原文图片发生变化或无法读取，请重新运行")
		}
	}
	return ctx.Err()
}

func (s *inputSnapshot) Close() { _ = os.RemoveAll(s.dir) }
