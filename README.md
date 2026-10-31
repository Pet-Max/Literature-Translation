<p align="center"><img src="images/logo.svg" width="100" alt="Literature Translation"></p>

<h1 align="center">Literature Translation</h1>

<p align="center"><b>英文学术文献翻译桌面应用</b> —— PDF / MD 拖入即翻，产出带公式、图表、参考文献的中文 PDF</p>

<p align="center">
  <a href="https://go.dev"><img alt="Go" src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoSize=auto&label=%E8%AF%AD%E8%A8%80"></a>
  <a href="https://www.python.org"><img alt="Python" src="https://img.shields.io/badge/Python-3.10%2B-3776AB?logo=python&logoSize=auto&label=%E8%AF%AD%E8%A8%80"></a>
  <a href="https://wails.io"><img alt="Framework" src="https://img.shields.io/badge/Framework-Wails%20v2-FF5227"></a>
  <a href="https://github.com/Pet-Max/Literature-Translation/releases"><img alt="Platform" src="https://img.shields.io/badge/Platform-Windows-lightgrey"></a>
  <a href="https://github.com/Pet-Max/Literature-Translation/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/Pet-Max/Literature-Translation?logo=github&label=Release"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/License-MIT-blue"></a>
</p>

## 界面预览

| 浅色主题（默认） | 深色主题 |
|:-:|:-:|
| ![浅色主题](images/preview-tasks-light.png) | ![深色主题](images/preview-tasks-dark.png) |

## 主要功能

- **智能路由**：按语言自动分流——英文 PDF 全流程、英文 MD 直接翻译、中文 MD 仅导出、中文 PDF 秒拒
- **解析校准**：以原 PDF 文本层为参照，自动修复解析损伤（符号、拼写、真词、重音符、控制码）；HTML 表格自动转 Markdown，可离线重跑
- **公式零损伤**：公式整体冻结、原样保留，机械校验不合格自动重试；超宽公式自动缩放
- **规则与术语**：多套规则预设可编辑切换；术语表统一译名
- **双语对照**：译文段落后附原文，方便核对
- **断点续跑**：译文即时落盘，中断重跑自动跳过已完成段
- **文献归档**：每篇一个档案夹，四类产物与过程文件全保留——`01_en_pdf` 原文 PDF · `02_en_md` 英文 MD · `03_zh_md` 中文 MD · `04_zh_pdf` 成品
- **字体与主题**：三套内嵌字体 + 自定义导入；浅深双主题一键切换
- **队列控制**：任务手动调度，支持暂停与单卡优先

## 快速开始

### 下载安装

前往 [GitHub Releases](https://github.com/Pet-Max/Literature-Translation/releases) 下载（发布后两个形态都有）：

- **安装版** `Literature-Translation-Setup-*-windows-amd64.exe`——安装向导、自定义目录、开始菜单与卸载器
- **便携版** `Literature-Translation-*-windows-amd64-portable.zip`——解压即用，程序与归档自包含

### 从源码构建

#### 环境要求

| 组件 | 说明 |
|------|------|
| Windows 10 / 11 | 目前仅支持 Windows（WebView2 与 Edge 系统内置，无需另装） |
| Go 1.26+ | 构建主程序 |
| Python 3.10+ | 翻译引擎（[PyMuPDF](https://pymupdf.readthedocs.io) + [httpx](https://www.python-httpx.org)） |
| [MinerU](https://mineru.net) 账号 | 精准解析 API Token |
| 任意 OpenAI 兼容 LLM | 模型 API 地址 + API Key + 模型名 |

#### 安装依赖

```bash
pip install pymupdf httpx
```

#### 构建

```bash
scripts\build.bat
```

产物在 `build/bin/Literature-Translation.exe`

### 首次配置

1. 启动应用，进入「设置 → 服务连接」
2. 填入 MinerU API Token（点「前往 API 管理 ↗」直达获取页）
3. 填入 LLM 模型 API 地址、API Key、模型名（可点「拉取模型」从上游列表选择）
4. 点输入框右侧 ⇄ 图标测试连通，两个 ✓ 就绪
5. 点「保存设置」

### 使用

1. 拖入 PDF 或 MD 文件（多文件可一次拖入）
2. 按需切换入队模式：自动 / 翻译 / 导出
3. 点「开始」——队列串行处理，可暂停 / 单卡优先
4. 完成后点「中文 PDF」打开成品，「打开文件夹」直达归档目录

> **看看效果**：[该文献](samples/example-document-cn.pdf) 是 arXiv 论文（2207.00867）经本应用实际翻译导出的成品——排版、公式、参考文献样式即真实所见。对应的[英文原文](samples/example-document-en.pdf)也可以直接拖进应用，自己跑一遍。

**进阶：用环境变量管理密钥（可选）**

四个凭据字段支持环境变量，且优先级高于配置文件，设置后重启应用生效：

`MINERU_TOKEN`（或 `MINERU_API_TOKEN`）、`LLM_API_KEY`、`LLM_BASE_URL`、`LLM_MODEL`。

注意：若之后在设置页点过「保存设置」，页面回显的值（含环境变量注入的）会写入 `config.json`。

## 常见问题

- **双击 exe 弹出 SmartScreen 警告？** 应用暂未做代码签名，点「更多信息 → 仍要运行」即可。
- **提示找不到 Microsoft Edge？** 导出 PDF 依赖系统 Edge（Win10/11 内置）。若系统被精简过，安装 Edge 即可，无需任何配置。
- **启动提示 Python 相关错误？** 翻译引擎需要 Python 3.10+ 与依赖（`pip install pymupdf httpx`）；也可用环境变量 `LITERATURE_PYTHON` 指定解释器路径。
- **MinerU 报 Token 无效 / A0202？** 到 [mineru.net「API 管理」](https://mineru.net/apiManage/token)重新生成 Token，回设置页粘贴后点输入框右侧 ⇄ 重测。
- **翻译卡住或某段失败？** 先看「日志」视图——解析、重试、导出的全过程都在。个别段最终失败会保留英文原文并标注，处理完直接重跑，自动断点续译。

## 隐私与安全

- 凭据保存在本机 `%APPDATA%/Literature Translation/config.json`，不上传、不遥测
- 翻译内容仅经过你配置的 LLM 接口和 MinerU 云端，本工具自身不收集任何数据
- 断点缓存只含源段哈希与译文，不含 API Key

## 开发指南

```
main.go        GUI 入口（Wails v2，go:embed 前端资源）
app/           Wails 绑定层（队列、配置、连接测试；路由测试在此）
cmd/literature-translation/   CLI 入口（与 GUI 共用 internal/pipeline）
internal/      能力包：config / pipeline / engine / mineru / render /
               export / fonts / repair / version（版本号唯一定义）
engine/        Python 翻译边车（无窗口子进程，stdio JSON-RPC：切段→冻结→翻译→校验→回填）
frontend/dist/ 前端三件套，即源码、零构建（embed 进主程序）
tests/         管线 E2E、前端 E2E、overlay 服务器、gui_shim 模拟壳
build/         图标源与打包产物（bin/ 不入库）
```

### 命令行（CLI）

GUI 之外提供同能力的命令行（与 GUI 共用 internal/pipeline 管线）：

```bash
literature-translation check                       # 预检配置与环境
literature-translation run paper.pdf               # 全流程：解析 → 校准 → 翻译 → 导出
literature-translation translate doc.md            # 翻译已有 MD 并导出（跳过解析）
literature-translation print doc.md                # 仅排版导出 PDF（不翻译）
literature-translation calibrate doc.md paper.pdf  # 离线重跑解析校准（改写 MD，自动留 .bak）
literature-translation font add|list|use|remove    # 字体管理
```

### 本地开发

- 改前端直接编辑 `frontend/dist/` 三个文件，无构建步骤；改完 `scripts\build.bat` 重新打包（前端经 embed 进 exe）
- 前端 E2E 的 overlay 服务器（`tests/overlay_server.mjs`）直供 dist 并注入 `tests/gui_preview/gui_shim.js`（window.go 的 mock）——**新增 Go 绑定时必须在 shim 里加同名 mock**，否则前端测试挂

### 本地打包

```bat
:: 一次性：下载便携 Python（嵌入式 3.12 + pymupdf/httpx），落 build\runtime\
scripts\prepare_runtime.bat

:: 之后每次：构建 exe + 组装载荷，产出两种分发物
scripts\package.bat
```

产物在 `build\bin\`：`Literature-Translation-Setup-vX.Y.Z-windows-amd64.exe`（安装版）与 `Literature-Translation-vX.Y.Z-windows-amd64-portable.zip`（便携版）。载荷 = 主 exe + engine\*.py + 便携运行时，两版同源。

### 质量门禁

三层测试体系 + 静态分析（vet / ruff / Python 单测 / go test 含管线 E2E / 前端 E2E），全部本地运行，不需要真实 LLM Key 与 MinerU 配额。一键体检：

```bat
scripts\check.bat
```

两个需要单独跑的项：

```bash
# 模糊测试：吃不可信外部输入的函数（种子语料随 go test 常跑，深挖用 -fuzz）
go test ./internal/pipeline -fuzz FuzzNewArchivePlan -fuzztime 30s

# 前端组件 E2E：Playwright 驱动真实前端 + mock 壳（需 Chrome/Edge）
npm i --no-save playwright-core
node tests/e2e_frontend.mjs
```

覆盖：语言路由、归档方案、字体校验、公式冻结/解冻、解析校准全链路、断点续跑、前端交互与主题。

### 欢迎 PR

1. fork → 建分支
2. `scripts\check.bat` 五步全绿（vet / ruff / Python 单测 / go test / 前端 E2E）
3. 提 PR——CI 自动跑同一套门禁（PR 只跑门禁、不产出安装包；首次贡献者的 CI 需维护者在 Actions 页批准一次后运行）
4. 约定：gofmt 与 ruff 零告警；新 Go 绑定必须同步 shim mock；归档目录常量在 `internal/pipeline/outputplan.go`，默认规则文本在 `internal/config`

## License

[MIT](LICENSE)

## 致谢

| 项目                                                         | 许可               | 用途                       |
| ------------------------------------------------------------ | ------------------ | -------------------------- |
| [Wails v2](https://wails.io)                                 | MIT                | Go 桌面框架                |
| [KaTeX](https://katex.org)                                   | MIT                | 公式排版引擎（内嵌）       |
| [霞鹜文楷](https://github.com/lxgw/LxgwWenKai)（[webfont 打包](https://github.com/chawyehsu/lxgw-wenkai-webfont)） | SIL OFL 1.1 / MIT  | 内嵌中文字体               |
| [PyMuPDF](https://pymupdf.readthedocs.io)                    | AGPL-3.0           | 引擎的 PDF 文本层抽取      |
| [MinerU](https://mineru.net)                                 | 云服务             | 文档精准解析               |
