# 使用已验证的提交和精确产物发布；任何必要步骤失败立即停止。
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Get-GitHubJson {
    param([string]$Endpoint, [switch]$AllowNotFound)
    $raw = @(& gh api --include $Endpoint 2>$null)
    $code = $LASTEXITCODE
    $text = $raw -join "`n"
    if ($text -notmatch '^HTTP/\S+ (\d{3})[^\n]*\n') { throw 'GitHub 返回了无法识别的响应' }
    $status = [int]$Matches[1]
    if ($AllowNotFound -and $status -eq 404) { return $null }
    if ($code -ne 0 -or $status -ne 200) { throw "GitHub 查询失败（HTTP $status），发布已停止" }
    $separator = [regex]::Match($text, '\r?\n\r?\n')
    if (-not $separator.Success) { throw 'GitHub 响应缺少正文' }
    $data = ConvertFrom-Json -InputObject $text.Substring($separator.Index + $separator.Length) -NoEnumerate
    if ($null -eq $data) { throw 'GitHub 返回了空响应' }
    return ,$data
}

function Invoke-GhChecked {
    param([string[]]$Arguments)
    & gh @Arguments --repo $env:GITHUB_REPOSITORY
    if ($LASTEXITCODE -ne 0) { throw 'GitHub 发布操作失败，后续操作已停止' }
}

function Get-TagCommit {
    param([string]$Tag)
    $encoded = [uri]::EscapeDataString($Tag)
    $ref = Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/git/ref/tags/$encoded" -AllowNotFound
    if ($null -eq $ref) { return $null }
    $obj = $ref.object
    for ($depth = 0; $depth -lt 16; $depth++) {
        if ($obj.sha -cnotmatch '^[0-9a-f]{40}$') { throw '标签提交信息无效' }
        if ($obj.type -eq 'commit') { return $obj.sha }
        if ($obj.type -ne 'tag') { throw '标签未指向可构建的提交' }
        $obj = (Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/git/tags/$($obj.sha)").object
    }
    throw '标签引用层数异常'
}

$tag = $env:TAG
$target = $env:TARGET_SHA
if ($tag -cnotmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$') { throw '发布版本号无效' }
if ($target -cnotmatch '^[0-9a-f]{40}$') { throw '发布提交无效' }
if ($env:PRERELEASE -cnotin @('true', 'false')) { throw '发布通道无效' }

$zip = "Literature-Translation-$tag-windows-amd64-portable.zip"
$exe = "Literature-Translation-Setup-$tag-windows-amd64.exe"
$names = @($zip, $exe)
$files = @(Get-ChildItem -LiteralPath 'dist' -Force)
if ($files.Count -ne 3 -or @($files | Where-Object { $_.PSIsContainer }).Count -ne 0) { throw '发布目录必须只包含两个包和 checksums.txt' }
foreach ($name in @($names) + 'checksums.txt') {
    $file = Get-Item -LiteralPath (Join-Path 'dist' $name)
    if ($file.Length -eq 0) { throw "发布文件为空：$name" }
}
$checksums = @{}
foreach ($line in Get-Content -LiteralPath 'dist/checksums.txt') {
    if ($line -notmatch '^([0-9a-fA-F]{64})  (.+)$') { throw '校验文件格式错误' }
    $hash, $name = $Matches[1], $Matches[2]
    if ($name -cnotin $names -or $checksums.ContainsKey($name)) { throw '校验文件存在多余或重复条目' }
    $checksums[$name] = $hash
}
if ($checksums.Count -ne 2) { throw '校验文件条目不完整' }
foreach ($name in $names) {
    if ((Get-FileHash -LiteralPath (Join-Path 'dist' $name) -Algorithm SHA256).Hash -ne $checksums[$name]) { throw "发布文件校验失败：$name" }
}
$tagCommit = Get-TagCommit $tag
if ($tagCommit -and $tagCommit -cne $target) { throw '标签已不再指向实际构建的提交，发布已停止' }
if (-not $tagCommit -and $env:PRERELEASE -eq 'false') { throw '正式版标签不存在' }

$encodedTag = [uri]::EscapeDataString($tag)
$release = Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/releases/tags/$encodedTag" -AllowNotFound
if (-not $release) {
    # 按标签查询只返回已发布版本；失败重试还需要找到此前留下的草稿。
    for ($page = 1; ; $page++) {
        $listed = Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/releases?per_page=100&page=$page"
        if ($listed -isnot [array]) { throw 'Release 列表响应无效' }
        $matching = @($listed | Where-Object { $_.tag_name -ceq $tag })
        if ($matching.Count -gt 1) { throw '存在多个同版本 Release，请先确认目标' }
        if ($matching.Count -eq 1) { $release = $matching[0]; break }
        if ($listed.Count -lt 100) { break }
    }
}
if ($release -and $release.tag_name -cne $tag) { throw 'Release 响应与目标版本不一致' }
if ($release -and $release.draft -and -not $tagCommit -and $release.target_commitish -cne $target) { throw '已有草稿的目标提交与实际构建不一致' }
$keepNotes = $release -and -not [string]::IsNullOrWhiteSpace($release.body)
$notesFile = Join-Path $env:RUNNER_TEMP 'release-notes.md'

if (-not $keepNotes) {
    $prev = $null
    $comparison = $null
    for ($page = 1; -not $prev; $page++) {
        $releases = Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/releases?per_page=100&page=$page"
        if ($releases -isnot [array]) { throw 'Release 列表响应无效' }
        foreach ($candidate in $releases) {
            if ($candidate.draft -or $candidate.prerelease -or $candidate.tag_name -ceq $tag) { continue }
            if ([string]::IsNullOrWhiteSpace($candidate.tag_name)) { throw 'Release 列表缺少版本号' }
            $base = [uri]::EscapeDataString($candidate.tag_name)
            $compare = Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/compare/$base...$target`?per_page=100&page=1"
            if ($compare.status -notin @('ahead', 'behind', 'diverged', 'identical')) { throw '提交比较响应无效' }
            if ($compare.status -eq 'ahead' -and $compare.ahead_by -gt 0 -and $compare.behind_by -eq 0) {
                $prev = $candidate.tag_name
                $comparison = $compare
                break
            }
        }
        if ($releases.Count -lt 100) { break }
    }
    $commits = [Collections.Generic.List[object]]::new()
    if ($prev) {
        $base = [uri]::EscapeDataString($prev)
        for ($page = 1; ; $page++) {
            $result = if ($page -eq 1) { $comparison } else { Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/compare/$base...$target`?per_page=100&page=$page" }
            if ($result.commits -isnot [array] -or $null -eq $result.total_commits -or $result.total_commits -le 0 -or $result.commits.Count -eq 0) { throw '提交列表响应无效' }
            foreach ($commit in $result.commits) { $commits.Add($commit) }
            if ($commits.Count -ge $result.total_commits) { break }
        }
    } else {
        for ($page = 1; ; $page++) {
            $rows = Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/commits?sha=$target&per_page=100&page=$page"
            if ($rows -isnot [array]) { throw '提交历史响应无效' }
            foreach ($commit in $rows) { $commits.Add($commit) }
            if ($rows.Count -lt 100) { break }
        }
    }
    if ($commits.Count -eq 0) { throw '目标提交历史为空' }
    $lines = @()
    $prTitles = @{}
    foreach ($commit in $commits) {
        if ($commit.sha -cnotmatch '^[0-9a-f]{40}$' -or [string]::IsNullOrWhiteSpace($commit.commit.message)) { throw '提交内容响应无效' }
        $title = ($commit.commit.message -split '\r?\n', 2)[0]
        $lines += "* $title ($($commit.sha.Substring(0, 7)))"
        if ($title -match '\(#(\d+)\)\s*$' -or $title -match '^Merge pull request #(\d+) from') { $prTitles[[int]$Matches[1]] = $title }
    }
    $notes = "## Changelog`n`n" + ($lines -join "`n") + "`n"
    $prLines = @()
    $contributors = @{}
    $owner = ($env:GITHUB_REPOSITORY -split '/')[0]
    foreach ($number in @($prTitles.Keys | Sort-Object)) {
        $pr = Get-GitHubJson "repos/$env:GITHUB_REPOSITORY/pulls/$number" -AllowNotFound
        if ($pr) {
            if ([string]::IsNullOrWhiteSpace($pr.title) -or [string]::IsNullOrWhiteSpace($pr.user.login)) { throw 'PR 信息响应无效' }
            $prLines += "* $($pr.title) by @$($pr.user.login) in #$number"
            if ($pr.user.login -ne $owner -and -not $contributors.ContainsKey($pr.user.login)) { $contributors[$pr.user.login] = $number }
        } else { $prLines += "* $($prTitles[$number])" }
    }
    if ($prLines.Count -gt 0) { $notes += "`n## What's Changed`n`n" + ($prLines -join "`n") + "`n" }
    if ($contributors.Count -gt 0) {
        $notes += "`n## New Contributors`n`n"
        foreach ($user in @($contributors.Keys | Sort-Object)) { $notes += "* @$user made their first contribution in #$($contributors[$user])`n" }
    }
    $changelog = if ($prev) { "$prev...$tag" } else { "$env:GITHUB_REPOSITORY/commits/$target" }
    $notes += "`n**Full Changelog**: $changelog"
    Set-Content -LiteralPath $notesFile -Value $notes -Encoding utf8NoBOM
}

if (-not $release) {
    $create = @('release', 'create', $tag, '--title', $tag, '--notes-file', $notesFile, '--draft', '--target', $target)
    if ($tagCommit) { $create += '--verify-tag' }
    if ($env:PRERELEASE -eq 'true') { $create += '--prerelease' }
    Invoke-GhChecked $create
}
# 新发布在上传完成后才公开；上传失败留下草稿，供后续重试。
Invoke-GhChecked @('release', 'upload', $tag, "dist/$zip", "dist/$exe", 'dist/checksums.txt', '--clobber')
$edit = @('release', 'edit', $tag, "--prerelease=$env:PRERELEASE", '--draft=false')
if ($release -and -not $keepNotes) { $edit += @('--notes-file', $notesFile) }
Invoke-GhChecked $edit
