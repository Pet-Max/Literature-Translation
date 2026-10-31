# publish-release.ps1 — 发布收口（被 release.yml 的 publish job 调用）。
# 前置：publish job 已用 download-artifact 把双产物落在 dist/。
# 输入（经 step/job env 注入）：GH_TOKEN / TAG / PRERELEASE；
# 运行时提供：GITHUB_REPOSITORY / RUNNER_TEMP / GITHUB_SHA。
# 行为：生成动态双语发布说明（Changelog / What's Changed / New Contributors /
# Full Changelog）→ 创建或更新 Release（稳定版 --verify-tag；预发布
# --prerelease --target 当前 commit）→ 上传双产物与 checksums.txt（--clobber）。

$tag = "$env:TAG"
$owner = ($env:GITHUB_REPOSITORY -split '/')[0]
# 发布页三段式：Changelog（全部 commit + 短哈希）→ What's Changed /
# New Contributors（仅当有合并 PR）→ Full Changelog。
# 不用 --generate-notes：个人仓库不走 PR，其 PR 段永远为空。
# 一次 API 调用同时取「首行标题 + 短哈希」：commit 消息按行输出
# 会被拍平，PR 提取只对首行做，避免把 commit 正文里的 "(#N)"
# 引用误认为合并的 PR。短哈希与 tag 区间在 Release 页由 GitHub
# 自动渲染为链接。
$prev = gh api "repos/$env:GITHUB_REPOSITORY/releases" --jq 'map(select(.prerelease == false)) | .[0].tag_name' 2>$null
if ($prev) {
  $rows = gh api "repos/$env:GITHUB_REPOSITORY/compare/$prev...$tag" --jq '.commits[] | (.commit.message | split("\n")[0]) + "\t" + .sha[0:7]'
} else {
  # 首个发布：无对比基准，取 tag 历史上的提交
  $rows = gh api "repos/$env:GITHUB_REPOSITORY/commits?sha=$tag&per_page=50" --jq '.[] | (.commit.message | split("\n")[0]) + "\t" + .sha[0:7]'
}
$commits = @()
$prTitles = @{}
foreach ($r in $rows) {
  $p = $r -split "`t", 2
  $commits += "* " + $p[0] + " (" + $p[1] + ")"
  if ($p[0] -match '\(#(\d+)\)\s*$' -or $p[0] -match '^Merge pull request #(\d+) from') {
    $prTitles[[int]$Matches[1]] = $p[0]
  }
}
$prNums = @($prTitles.Keys | Sort-Object)
$notes = "## Changelog`n`n" + ($commits -join "`n") + "`n"
if ($prNums.Count -gt 0) {
  # 有 PR：What's Changed 列 PR（标题+作者+短链）；New Contributors
  # 列仓库所有者之外的 PR 作者——个人仓库阶段外部作者即新贡献者，
  # 常客出现后可在发布页手动校正措辞
  $lines = @()
  $newC = @()
  $userPr = @{}
  foreach ($n in $prNums) {
    $info = gh api "repos/$env:GITHUB_REPOSITORY/pulls/$n" --jq '.title + "\t" + .user.login' 2>$null
    if ($info) {
      $title, $user = $info -split "`t", 2
      $lines += "* $title by @$user in #$n"
      if ($user -and $user -ne $owner -and -not $userPr.ContainsKey($user)) {
        $userPr[$user] = $n
        $newC += $user
      }
    } else {
      # (#N) 引用的是 issue 而非 PR：按 commit 标题列出，不加 PR 链接
      $lines += "* $($prTitles[$n])"
    }
  }
  $notes += "`n## What's Changed`n`n" + ($lines -join "`n") + "`n"
  if ($newC.Count -gt 0) {
    $notes += "`n## New Contributors`n`n"
    foreach ($u in $newC) {
      $notes += "* @$u made their first contribution in #$($userPr[$u])`n"
    }
    $notes += "`n"
  }
}
$changelog = if ($prev) { "$prev...$tag" } else { "$env:GITHUB_REPOSITORY/commits/$tag" }
$notes += "`n**Full Changelog**: $changelog"
$notesFile = "$env:RUNNER_TEMP/release-notes.md"
Set-Content -Path $notesFile -Value $notes -Encoding utf8NoBOM
# 稳定版：tag 已存在 → --verify-tag；预发布：tag 不存在 → 发布时由
# gh 代建于当前 commit（--target），并标记 --prerelease
$relFlags = @()
if ($env:PRERELEASE -eq 'true') {
  $relFlags += @('--prerelease', '--target', "$env:GITHUB_SHA")
} else {
  $relFlags += '--verify-tag'
}
# 已存在同版本 Release（重发场景）→ 编辑更新说明；不存在 → 创建
if (gh release view $tag 2>$null) {
  gh release edit $tag --notes-file $notesFile
} else {
  gh release create $tag --title $tag --notes-file $notesFile @relFlags
}
$zip = "Literature-Translation-$tag-windows-amd64-portable.zip"
$exe = "Literature-Translation-Setup-$tag-windows-amd64.exe"
gh release upload $tag "dist/$zip" "dist/$exe" "dist/checksums.txt" --clobber
