package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"literature-translation/internal/engine"
	"literature-translation/internal/version"
)

type runMeta struct {
	Schema               int               `json:"schema"`
	Build                string            `json:"build"`
	SourceHash           string            `json:"source_sha256"`
	ConfigFingerprint    string            `json:"config_fingerprint"`
	InputHash            string            `json:"input_sha256"`
	InputResources       map[string]string `json:"input_resources"`
	TranslationHash      string            `json:"translation_sha256"`
	TranslationResources map[string]string `json:"translation_resources"`
	ProgressFile         string            `json:"progress_file"`
	Complete             bool              `json:"complete"`
	Total                int               `json:"total"`
	Done                 int               `json:"done"`
	References           int               `json:"references"`
}

// Only non-secret fields enter the fingerprint and metadata. Presentation-only
// changes can reuse a complete translation; parse/repair/provider changes cannot.
func (p *Pipeline) configFingerprint() string {
	c := p.Cfg
	system := engine.BuildSystem(glossaryPairs(c), c.Rules.ActiveText(), "")
	data, _ := json.Marshal(struct {
		Key, Provider, Parser string
		Spelling, Symbols     bool
	}{
		engine.BuildCacheKey(system, c.Export.Bilingual, c.Translate.SkipReferences, c.LLM.Model),
		c.LLM.BaseURL, c.MinerU.ModelVersion, c.Repair.Spelling, c.Repair.Symbols,
	})
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func metadataPath(out string, plan *ArchivePlan) string {
	if plan != nil {
		return filepath.Join(plan.ProcessDir, "run.json")
	}
	return out + ".run.json"
}

func readRunMeta(path string) (runMeta, error) {
	var m runMeta
	b, e := os.ReadFile(path)
	if e != nil {
		return m, e
	}
	e = json.Unmarshal(b, &m)
	return m, e
}

func writeRunMeta(ctx context.Context, path string, m runMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".run-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (p *Pipeline) beginRun(ctx context.Context, source, md, out string, plan *ArchivePlan) (string, error) {
	progress := out + ".progress.jsonl"
	if plan != nil {
		progress = plan.ProgressPath
	}
	path := metadataPath(out, plan)
	old, e := readRunMeta(path)
	fingerprint := p.configFingerprint()
	if e == nil && (old.Schema == 1 || old.Schema == 2) {
		if old.ConfigFingerprint != fingerprint {
			progress = strings.TrimSuffix(progress, ".jsonl") + "-" + fingerprint[:16] + ".jsonl"
		} else if old.ProgressFile != "" && filepath.Base(old.ProgressFile) == old.ProgressFile {
			progress = filepath.Join(filepath.Dir(progress), old.ProgressFile)
		}
	} else if e == nil || !os.IsNotExist(e) {
		// A damaged new manifest must not grant legacy cache reuse.
		progress = strings.TrimSuffix(progress, ".jsonl") + "-" + fingerprint[:16] + ".jsonl"
	}
	m := runMeta{Schema: 2, Build: version.Display(), ConfigFingerprint: fingerprint, ProgressFile: filepath.Base(progress)}
	if m.SourceHash, e = fileHash(source); e != nil {
		return "", e
	}
	if m.InputHash, e = fileHash(md); e != nil {
		return "", e
	}
	if m.InputResources, e = resourceHashes(md); e != nil {
		return "", e
	}
	if e = writeRunMeta(ctx, path, m); e != nil {
		return "", e
	}
	return progress, nil
}

func (p *Pipeline) finishRun(ctx context.Context, source, input, out string, plan *ArchivePlan, tr *translateOutcome) error {
	path := metadataPath(out, plan)
	m, e := readRunMeta(path)
	if e != nil {
		return e
	}
	current, e := fileHash(source)
	if e != nil {
		return e
	}
	if current != m.SourceHash {
		return fmt.Errorf("翻译期间源文件发生变化，请重新运行")
	}
	inputHash, e := fileHash(input)
	if e != nil {
		return e
	}
	resources, e := resourceHashes(input)
	if e != nil {
		return e
	}
	if inputHash != m.InputHash || !reflect.DeepEqual(resources, m.InputResources) {
		return fmt.Errorf("翻译期间原文或图片发生变化")
	}
	if m.TranslationHash, e = fileHash(out); e != nil {
		return e
	}
	if m.TranslationResources, e = resourceHashes(out); e != nil {
		return e
	}
	m.Total, m.Done, m.References = tr.Total, tr.Done, tr.KeptReferences
	m.Complete = tr.Total > 0 && tr.Done == tr.Total && len(tr.Failed) == 0
	return writeRunMeta(ctx, path, m)
}

func (p *Pipeline) resumableRun(plan *ArchivePlan) (runMeta, error) {
	m, e := readRunMeta(metadataPath(plan.ZhMDPath, plan))
	if e != nil {
		return m, fmt.Errorf("缺少完整运行元数据")
	}
	if m.Schema != 2 || !m.Complete || m.Total < 1 || m.Done != m.Total || m.References < 0 || m.References > m.Done {
		return m, fmt.Errorf("运行元数据未确认全部完成")
	}
	if m.ConfigFingerprint != p.configFingerprint() {
		return m, fmt.Errorf("解析、翻译配置已变化")
	}
	h, e := fileHash(plan.SourcePath)
	if e != nil || h != m.SourceHash {
		return m, fmt.Errorf("源文件已变化或缺失")
	}
	input := plan.SourcePath
	if strings.EqualFold(filepath.Ext(input), ".pdf") {
		input = plan.EnMDPath
	}
	h, e = fileHash(input)
	if e != nil || h != m.InputHash {
		return m, fmt.Errorf("英文归档或原文已变化")
	}
	resources, e := resourceHashes(input)
	if e != nil || !reflect.DeepEqual(resources, m.InputResources) {
		return m, fmt.Errorf("原文图片已变化或缺失")
	}
	h, e = fileHash(plan.ZhMDPath)
	if e != nil || h != m.TranslationHash {
		return m, fmt.Errorf("译文已变化或缺失")
	}
	resources, e = resourceHashes(plan.ZhMDPath)
	if e != nil || !reflect.DeepEqual(resources, m.TranslationResources) {
		return m, fmt.Errorf("译文图片已变化或缺失")
	}
	return m, nil
}
