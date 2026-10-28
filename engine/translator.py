"""translator.py — OpenAI 兼容 chat/completions 客户端。

只用最朴素的请求体（model/messages/temperature），非流式，
不依赖任何厂商私有参数，保证对各类兼容服务的最大适配面。
网络层重试（429/5xx/超时，指数退避）只在这里做；
质量重试（校验失败回喂）由 engine 循环负责，两层互不混淆。
"""

from __future__ import annotations

import time

import httpx


class LLMError(Exception):
    """调用失败（可读文本，可直接进日志/界面）。"""


class LLMClient:
    def __init__(self, base_url: str, api_key: str, model: str,
                 timeout: float = 180.0, max_attempts: int = 4):
        self._url = base_url.rstrip("/") + "/chat/completions"
        self._model = model
        self._max_attempts = max_attempts
        self._client = httpx.Client(
            timeout=timeout,
            headers={"Authorization": f"Bearer {api_key}"},
        )

    def complete(self, system: str, user: str, temperature: float = 0.1,
                 history: list[dict] | None = None) -> str:
        """一次补全。网络层自动重试；仍失败抛 LLMError。

        history 为可选的追加消息（质量重试时带上次的 assistant 译文
        与纠错指令，让模型看到自己上一次的输出）。非流式。
        """
        messages = [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ]
        if history:
            messages.extend(history)
        body = {
            "model": self._model,
            "messages": messages,
            "temperature": temperature,
        }
        last_err = ""
        for attempt in range(1, self._max_attempts + 1):
            try:
                resp = self._client.post(self._url, json=body)
                if resp.status_code == 200:
                    try:
                        data = resp.json()
                    except ValueError as err:
                        # 200 但 body 非 JSON：多为模型 API 地址配错（如打到了网页）
                        raise LLMError(
                            f"返回了非 JSON 内容（检查模型 API 地址是否正确）: {resp.text[:120]}") from err
                    if not isinstance(data, dict):
                        # 合法 JSON 但非对象（如顶层数组）：后续 .get 会抛
                        # AttributeError 穿透重试层，这里显式转为可读文本
                        raise LLMError(
                            f"返回了非预期格式（应为 JSON 对象）: {str(data)[:120]}")
                    content = (data.get("choices") or [{}])[0] \
                        .get("message", {}).get("content")
                    if not content or not content.strip():
                        raise LLMError("模型返回了空内容")
                    return content.strip()
                if resp.status_code in (429, 500, 502, 503, 504):
                    last_err = f"HTTP {resp.status_code}: {resp.text[:200]}"
                    # 落到下方退避重试
                else:
                    # 4xx（鉴权失败、模型名错误等）重试无意义，直接抛
                    raise LLMError(
                        f"HTTP {resp.status_code}: {resp.text[:300]}")
            except httpx.HTTPError as e:
                last_err = f"网络错误: {e}"
            except LLMError:
                raise
            if attempt < self._max_attempts:
                time.sleep(min(2 ** (attempt - 1), 8))
        raise LLMError(f"重试 {self._max_attempts} 次后仍失败，最后错误：{last_err}")

    def close(self) -> None:
        self._client.close()
