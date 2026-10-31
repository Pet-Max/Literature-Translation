#!/usr/bin/env python3
"""lint_workflows.py — GitHub Actions 工作流全量静态检查（仅标准库）。

背景：一次实际事故链——bash 步骤混入 pwsh 语法、needs 漏声明导致
outputs 静默为空、job 引用仓库文件但没有 checkout、外置脚本没被提交——
全部只能在 CI 运行时暴露。本脚本把这些固化成规则，push 前即可拦截。

规则：
  R1  shell: bash 的 run 块与 *.sh 禁用 $env:VAR（pwsh 语法，应写 $VAR）
  R2  shell: pwsh 的 run 块与 *.ps1 禁用裸 $GITHUB_*（应写 $env:GITHUB_*）
  R3  needs.X.outputs.Y 的引用：X 必须在该 job 的 needs 里，且 X 必须声明
      该 output（防「引用上游输出但 needs 漏声明」→ 静默空值）
  R4  inputs.X 的引用：X 必须是 workflow_dispatch 已声明的输入
  R5  无 checkout 的 job 不得引用仓库内文件（.github/ engine/ internal/
      build/installer build/windows LICENSE samples）——检出缺失类事故
  R6  run 中引用的本地脚本（bash *.sh / pwsh *.ps1）必须存在于仓库树
  R7  本地 uses:（./.github/...）路径必须存在

扫描范围：.github/workflows/*.yml、.github/actions/**/*.yml、
.github/workflows/scripts/*.{sh,ps1}。
退出码：0 = 通过；1 = 存在违规（逐条打印 file:line）。
"""

import glob
import io
import json
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
WF_DIR = os.path.join(ROOT, ".github", "workflows")
SCRIPT_DIR = os.path.join(WF_DIR, "scripts")

R1 = re.compile(r"\$env:[A-Za-z_]+")
R2 = re.compile(r"(?<!env:)\$GITHUB_[A-Z_]+")
NEEDS_OUT = re.compile(r"needs\.([A-Za-z0-9_-]+)\.outputs\.([A-Za-z0-9_-]+)")
NEEDS_RESULT = re.compile(r"needs\.([A-Za-z0-9_-]+)\.result")
INPUT_REF = re.compile(r"inputs\.([A-Za-z0-9_-]+)")
REPO_FILE_HINTS = (".github/", "engine/", "internal/", "build/installer/",
                   "build/windows/", "LICENSE", "samples/")
LOCAL_USES = re.compile(r"uses:\s*(\./\S+)")
SCRIPT_CALL = re.compile(r"(?:bash|pwsh|-File)\s+\.github/\S+\.(?:sh|ps1)")


def inline_run_blocks(text):
    """从工作流 yml 文本中提取内联 run 块，产出 (shell, 块文本, 起始行号)。"""
    lines = text.splitlines()
    shell = None
    i = 0
    while i < len(lines):
        m = re.match(r"^(\s*)shell:\s*(\S+)\s*$", lines[i])
        if m:
            shell = m.group(2)
            i += 1
            continue
        m = re.match(r"^(\s*)run:\s*(.*)$", lines[i])
        if m:
            indent = len(m.group(1))
            body = [m.group(2)] if m.group(2) else []
            j = i + 1
            while j < len(lines):
                nxt = lines[j]
                if nxt.strip() and (len(nxt) - len(nxt.lstrip())) > indent:
                    body.append(nxt)
                    j += 1
                else:
                    break
            yield (shell or "bash", "\n".join(body), i + 1)
            i = j
            continue
        if re.match(r"^\s*-\s+uses:", lines[i]):
            shell = None
        i += 1


def violations_in(shell, text, where):
    out = []
    if shell == "bash":
        for k, line in enumerate(text.splitlines(), 1):
            if R1.search(line):
                out.append(f"{where}:{k} [R1] bash 块混入 pwsh 语法 $env:（应写 $VAR）: {line.strip()}")
    if shell in ("pwsh", "powershell"):
        for k, line in enumerate(text.splitlines(), 1):
            if R2.search(line):
                out.append(f"{where}:{k} [R2] pwsh 块混入 bash 语法裸 $GITHUB_*（应写 $env:GITHUB_*）: {line.strip()}")
    return out


def has_repo_file_refs(job):
    """job 内联脚本是否引用了仓库内文件（粗匹配，用于 R5 检出缺失检查）。"""
    blob = ""
    for s in job.get("steps", []):
        blob += str(s.get("run", "")) + "\n"
    return any(h in blob for h in REPO_FILE_HINTS)


def structural_checks(wf, where, problems):
    jobs = wf.get("jobs", {})
    declared_outputs = {}
    for jid, job in jobs.items():
        outs = job.get("outputs") or {}
        declared_outputs[jid] = set(outs.keys())

    # R7：本地 uses 路径必须存在
    for jid, job in jobs.items():
        for s in job.get("steps", []):
            m = LOCAL_USES.search(str(s.get("uses", "")))
            if m:
                rel = m.group(1).lstrip("./")
                if not os.path.isdir(os.path.join(ROOT, ".github", rel)) and \
                   not os.path.isfile(os.path.join(ROOT, ".github", rel)):
                    problems.append(f"{where}:{jid} [R7] 本地 action 路径不存在: {m.group(1)}")

    # R6：run 引用的外置脚本必须存在于仓库树
    for jid, job in jobs.items():
        for s in job.get("steps", []):
            for m in re.finditer(SCRIPT_CALL, str(s.get("run", ""))):
                rel = m.group(0).split()[-1].replace("/", os.sep)
                if not os.path.isfile(os.path.join(ROOT, rel)):
                    problems.append(f"{where}:{jid} [R6] 引用的脚本不在仓库树: {m.group(0)}")

    for jid, job in jobs.items():
        needs = job.get("needs", [])
        needs = [needs] if isinstance(needs, str) else list(needs)
        has_checkout = any("checkout" in str(s.get("uses", "")) for s in job.get("steps", []))

        # R3：needs.X.outputs.Y 引用必须 X ∈ needs 且 X 声明了该 output
        job_blob = json.dumps(job, ensure_ascii=False)
        for m in NEEDS_OUT.finditer(job_blob):
            x, y = m.group(1), m.group(2)
            if x not in needs:
                problems.append(f"{where}:{jid} [R3] 引用 needs.{x}.outputs.{y} 但 x 不在 needs 里（静默空值）")
            elif y not in declared_outputs.get(x, set()):
                problems.append(f"{where}:{jid} [R3] needs.{x} 未声明输出 {y}")
        for m in NEEDS_RESULT.finditer(job_blob):
            if m.group(1) not in needs:
                problems.append(f"{where}:{jid} [R3] 引用 needs.{m.group(1)}.result 但不在 needs 里")

        # R4：inputs.X 引用必须是已声明的 dispatch 输入
        on = wf.get(True) or wf.get("on") or {}
        declared_inputs = set((on.get("workflow_dispatch", {}).get("inputs") or {}).keys())
        for m in INPUT_REF.finditer(job_blob):
            if m.group(1) not in declared_inputs:
                problems.append(f"{where}:{jid} [R4] 引用未声明的 inputs.{m.group(1)}")

        # R5：引用仓库文件但没有 checkout
        if not has_checkout and has_repo_file_refs(job):
            problems.append(f"{where}:{jid} [R5] job 引用仓库内文件但没有 checkout 步骤")

    # R4 的全局形态：outputs/env 里引用 inputs.* 的 job 级声明
    for jid, job in jobs.items():
        env = job.get("env") or {}
        for m in INPUT_REF.finditer(json.dumps(env, ensure_ascii=False)):
            if m.group(1) not in declared_inputs:
                problems.append(f"{where}:{jid} [R4] env 引用未声明的 inputs.{m.group(1)}")


def main():
    problems = []
    scanned = 0

    for path in sorted(glob.glob(os.path.join(WF_DIR, "*.yml")) +
                       glob.glob(os.path.join(ROOT, ".github", "actions", "**", "*.yml"), recursive=True)):
        text = io.open(path, encoding="utf-8").read()
        scanned += 1
        rel = os.path.relpath(path, ROOT)
        for shell, block, lineno in inline_run_blocks(text):
            problems += violations_in(shell, block, f"{rel}:{lineno}")
        try:
            import yaml
            wf = yaml.safe_load(text)
        except ImportError:
            print("[skip] 结构检查需要 pyyaml（pip install pyyaml）")
            wf = None
        if wf is not None:
            structural_checks(wf, rel, problems)

    for path in sorted(glob.glob(os.path.join(SCRIPT_DIR, "*.sh"))):
        scanned += 1
        text = io.open(path, encoding="utf-8").read()
        problems += violations_in("bash", text, os.path.relpath(path, ROOT))

    for path in sorted(glob.glob(os.path.join(SCRIPT_DIR, "*.ps1"))):
        scanned += 1
        text = io.open(path, encoding="utf-8").read()
        problems += violations_in("pwsh", text, os.path.relpath(path, ROOT))

    for p in problems:
        print(p)
    print(f"workflow lint: 扫描 {scanned} 个文件，{'OK' if not problems else f'{len(problems)} 处违规'}")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
