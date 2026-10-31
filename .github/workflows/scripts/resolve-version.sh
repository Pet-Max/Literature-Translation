#!/usr/bin/env bash
# resolve-version.sh — 解析发布版本号（被 release.yml 的 resolve job 调用）。
# 输入（经 step env 注入）：EVENT / CHANNEL / OVERRIDE / PUBLISH_INPUT / GH_TOKEN；
# 运行时提供：GITHUB_REPOSITORY / GITHUB_REF / GITHUB_OUTPUT。
# 输出（写入 $GITHUB_OUTPUT）：tag / version / checkout_ref / prerelease / publish。
# 语义：
#   tag push            → 稳定版，一律发布，守卫校验 tag == internal/version
#   channel=stable      → 重发最新稳定版 / 指定已存在 tag（守卫生效）
#   channel=prerelease  → 版本覆盖（OVERRIDE）或最新稳定版补丁+1（跳过守卫，tag 可代建）
set -euo pipefail
PUBLISH="$PUBLISH_INPUT"  # dispatch 的发布开关；tag push 分支下强制 true

STABLE_LATEST=$(gh api "repos/$GITHUB_REPOSITORY/releases" \
  --jq 'map(select(.prerelease == false)) | .[0].tag_name' 2>/dev/null || true)

case "$EVENT-$CHANNEL" in
  push-* )            # tag push：稳定版，一律发布
    TAG="$GITHUB_REF_NAME"; REF="$TAG"; PRERELEASE=false; PUBLISH=true ;;
  *dispatch-prerelease )
    # 预发布：版本可覆盖；留空 = 最新稳定版补丁+1；tag 不存在时发布代建
    if [ -n "$OVERRIDE" ]; then TAG="$OVERRIDE"
    else
      [ -n "$STABLE_LATEST" ] || { echo "::error::仓库无稳定版可推算，请填版本号"; exit 1; }
      MAJOR=$(printf '%s' "$STABLE_LATEST" | sed -E 's/^v([0-9]+)\..*/\1/')
      MINOR=$(printf '%s' "$STABLE_LATEST" | sed -E 's/^v[0-9]+\.([0-9]+)\..*/\1/')
      PATCH=$(printf '%s' "$STABLE_LATEST" | sed -E 's/^v[0-9]+\.[0-9]+\.([0-9]+).*/\1/')
      TAG="v$MAJOR.$MINOR.$((PATCH + 1))"
    fi
    REF="$GITHUB_REF"; PRERELEASE=true ;;
  * )                 # stable：重发最新稳定版 / 指定已存在 tag（守卫生效）
    if [ -n "$OVERRIDE" ]; then TAG="$OVERRIDE"
    else
      [ -n "$STABLE_LATEST" ] || { echo "::error::仓库无稳定版可重发，请填 tag"; exit 1; }
      TAG="$STABLE_LATEST"
    fi
    REF="$TAG"; PRERELEASE=false ;;  # PUBLISH 尊重 dispatch 输入
esac

# 版本守卫：稳定通道读「tag 自己的」internal/version 比对——稳定留空重发时
# resolve 检出的是 main，读工作区会误杀合法重发。raw 媒体类型直取文件原文，
# 绕过 base64/jq 解码链（CI 曾在此产出空值）。
if [ "$PRERELEASE" = "false" ]; then
  RAW=$(gh api -H "Accept: application/vnd.github.raw" "repos/$GITHUB_REPOSITORY/contents/internal/version/version.go?ref=$TAG")
  SRC=$(printf '%s' "$RAW" | sed -nE 's/.*"(v[^"]+)".*/\1/p')
  if [ -z "$SRC" ]; then
    echo "::error::tag($TAG) 的 version.go 未能提取版本号（原始内容前 200 字符如下）"
    printf '%s' "$RAW" | head -c 200
    echo
    exit 1
  fi
fi

echo "tag=$TAG"          >> "$GITHUB_OUTPUT"
echo "version=${TAG#v}"  >> "$GITHUB_OUTPUT"
echo "checkout_ref=$REF" >> "$GITHUB_OUTPUT"
echo "prerelease=$PRERELEASE" >> "$GITHUB_OUTPUT"
echo "publish=$PUBLISH"  >> "$GITHUB_OUTPUT"
