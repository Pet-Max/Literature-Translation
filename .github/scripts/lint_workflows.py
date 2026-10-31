#!/usr/bin/env python3
"""lint_workflows.py — GitHub Actions 的 shell 语法一致性静态检查（仅标准库）。

背景：一次实际事故是 bash 步骤混入 pwsh 环境变量语法（$env:VAR →
"env: unbound variable"），且只在 CI 运行时暴露。本脚本把该类问题固化
为两条规则，push 前即可拦截：

  R1  shell: bash 的 run 块与 *.sh 脚本禁用 $env:VAR —— bash 应写 $VAR
  R2  shell: pwsh 的 run 块与 *.ps1 脚本禁用裸 $GITHUB_* —— 应写 $env:GITHUB_*

扫描范围：.github/workflows/*.yml 的内联 run 块、.github/actions/**/*.yml、
.github/workflows/scripts/ 下的 *.sh 与 *.ps1。
退出码：0 = 通过；1 = 存在违规（逐条打印 file:line）。
"""

import glob
import io
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
WF_DIR = os.path.join(ROOT, ".github", "workflows")
SCRIPT_DIR = os.path.join(WF_DIR, "scripts")

# bash 块里的 pwsh 环境变量语法
R1 = re.compile(r"\$env:[A-Za-z_]+")
# pwsh 块里的裸 GITHUB_ 环境变量（$env:GITHUB_* 为正确形态，负向断言排除）
R2 = re.compile(r"(?<!env:)\$GITHUB_[A-Z_]+")


def inline_run_blocks(text):
    """从工作流 yml 文本中提取内联 run 块，产出 (shell, 块文本, 起始行号)。

    启发式：跟踪最近出现的 `shell: X`；遇到 `run:` 后收集缩进更深的行，
    直到缩进回落到不深于 run 行为止。对本仓库的排版足够可靠。
    """
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


def main():
    problems = []
    scanned = 0

    for path in sorted(glob.glob(os.path.join(WF_DIR, "*.yml"))):
        text = io.open(path, encoding="utf-8").read()
        scanned += 1
        for shell, block, lineno in inline_run_blocks(text):
            problems += violations_in(shell, block, f"{os.path.relpath(path, ROOT)}:{lineno}")

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
