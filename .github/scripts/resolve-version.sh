#!/usr/bin/env bash
# 固定发布目标提交并核对版本；仅输出工作流所需的非敏感信息。
set -euo pipefail

fail() { echo "::error::$*" >&2; exit 1; }

# gh 负责解析 JSON；仅明确的 HTTP 404 可以表示目标不存在。
api_text() {
  local response rc status body
  if response=$(gh api --include "$1" --jq "$2" 2>/dev/null); then rc=0; else rc=$?; fi
  status=$(printf '%s\n' "$response" | sed -nE 's/^HTTP\/[^ ]+ ([0-9]{3}).*/\1/p' | tail -n 1)
  if [[ "$status" == 404 && "${3:-false}" == true ]]; then return 4; fi
  [[ "$rc" == 0 && "$status" == 200 ]] || fail "GitHub 查询失败（HTTP ${status:-未知}），发布已停止"
  body=$(printf '%s\n' "$response" | awk 'body { sub(/\r$/, ""); print; next } /^\r?$/ { body=1 }')
  [[ -n "$body" ]] || fail "GitHub 返回了空响应"
  printf '%s' "$body"
}

tag_sha() {
  local object rc kind sha depth
  if object=$(api_text "repos/$GITHUB_REPOSITORY/git/ref/tags/$1" '.object | [.type, .sha] | @tsv' true); then :;
  else rc=$?; return "$rc"; fi
  for ((depth=0; depth<16; depth++)); do
    read -r kind sha <<< "$object"
    [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || fail "标签提交信息无效"
    case "$kind" in
      commit) printf '%s' "$sha"; return ;;
      tag) object=$(api_text "repos/$GITHUB_REPOSITORY/git/tags/$sha" '.object | [.type, .sha] | @tsv') || return $? ;;
      *) fail "标签未指向可构建的提交" ;;
    esac
  done
  fail "标签引用层数异常"
}

PUBLISH="${PUBLISH_INPUT:-false}"
case "$EVENT" in
  push) TAG="$GITHUB_REF_NAME"; PRERELEASE=false; PUBLISH=true ;;
  workflow_dispatch)
    case "${CHANNEL:-stable}" in
      stable|prerelease) ;;
      *) fail "发布通道无效" ;;
    esac
    PRERELEASE=false
    [[ "${CHANNEL:-stable}" != prerelease ]] || PRERELEASE=true
    if [[ -n "${OVERRIDE:-}" ]]; then TAG="$OVERRIDE"
    else
      if LATEST=$(api_text "repos/$GITHUB_REPOSITORY/releases/latest" '.tag_name' true); then :;
      else
        rc=$?
        [[ "$rc" != 4 ]] || fail "仓库没有正式版，请填写版本号"
        exit "$rc"
      fi
      TAG="$LATEST"
      if [[ "$PRERELEASE" == true ]]; then
        [[ "$LATEST" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || fail "最新正式版版本号无法推算"
        TAG="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] + 1))"
      fi
    fi ;;
  *) fail "不支持的触发方式" ;;
esac
[[ "$PUBLISH" == true || "$PUBLISH" == false ]] || fail "发布开关无效"
[[ "$TAG" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]] || fail "版本号格式无效，请使用 v1.2.1 这样的版本号"
for part in "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"; do
  [[ "${#part}" -le 5 && "$part" -le 65535 ]] || fail "版本号超出 Windows 资源范围"
done

if SHA=$(tag_sha "$TAG"); then :;
else
  rc=$?
  [[ "$rc" == 4 && "$PRERELEASE" == true ]] || fail "无法取得发布标签对应的提交"
  SHA="$GITHUB_SHA"
fi
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || fail "发布提交无效"
ENCODED=$(api_text "repos/$GITHUB_REPOSITORY/contents/internal/version/version.go?ref=$SHA" 'if .encoding == "base64" then .content else error("unexpected encoding") end')
RAW=$(printf '%s' "$ENCODED" | base64 --decode) || fail "无法读取目标提交的版本文件"
SRC=$(printf '%s\n' "$RAW" | sed -nE 's/^[[:space:]]*const Version = "(v[^"]+)"[[:space:]]*$/\1/p')
[[ "$SRC" == "$TAG" ]] || fail "目标版本 $TAG 与源码版本 ${SRC:-未知} 不一致；请先准备对应版本的源码"
{
  echo "tag=$TAG"
  echo "version=${TAG#v}"
  echo "checkout_ref=$SHA"
  echo "prerelease=$PRERELEASE"
  echo "publish=$PUBLISH"
} >> "$GITHUB_OUTPUT"
