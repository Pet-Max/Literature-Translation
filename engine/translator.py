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
    """调用失败（人话消息，可直接进日志/界面）。"""


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

    def complete(self, system: str, user: str, temperature: float = 0.1) -> str:
        """一次补全。网络层自动重试；仍失败抛 LLMError。"""
        body = {
            "model": self._model,
            "messages": [
                {"role": "system", "content": system},
                {"role": "user", "content": user},
            ],
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
                        # 200 但 body 非 JSON：多为接口地址配错（如打到了网页）
                        raise LLMError(
                            f"返回了非 JSON 内容（检查接口地址是否正确）: {resp.text[:120]}") from err
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
