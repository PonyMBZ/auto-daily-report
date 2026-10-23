# -*- coding: utf-8 -*-
"""
LLM 抽象层 —— 让"用哪个 AI"成为可替换的选择。

设计要点:
1. 所有 LLM 都实现同一个接口 chat(messages) -> str
   宿主代码只依赖接口, 不关心背后是 DeepSeek 还是 Mock
2. MockLLM 不是玩具 —— 它是测试基础设施。
   没有它, 你想测试循环逻辑就得每次真的调 API(慢、要钱、要网络)。
   有了它, 循环逻辑的测试是瞬间的、免费的、可重复的。
   这叫做"依赖注入", 是写可测试代码的核心手艺。
"""
import json
import urllib.error
import urllib.request


class MockLLM:
    """按剧本演出的假 AI。script 里预先写好它要"说"的每一句话。"""

    def __init__(self, script: list[str]):
        self._script = list(script)

    def chat(self, messages: list[dict]) -> str:
        if not self._script:
            raise RuntimeError("MockLLM 剧本耗尽 —— 循环比预期多跑了,"
                               "或者 AI 没有宣布 done 就断了")
        reply = self._script.pop(0)
        # 打印出来让你能看到"大脑此刻在想什么", 调试循环必备
        print(f"[MockLLM] {reply}", file=__import__("sys").stderr)
        return reply


class DeepSeekLLM:
    """真 AI。OpenAI 兼容接口, 只用标准库 urllib, 不引第三方依赖。

    现在只是"装好备用", 第三章接入真实任务, 第五章写日报时重度使用。
    """

    def __init__(self, api_key: str, model: str = "deepseek-chat"):
        self.api_key = api_key
        self.model = model

    def chat(self, messages: list[dict]) -> str:
        payload = json.dumps({
            "model": self.model,
            "messages": messages,
            "temperature": 0,  # 任务型调用要确定性, 别让它发挥
        }).encode("utf-8")
        req = urllib.request.Request(
            "https://api.deepseek.com/chat/completions",
            data=payload,
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {self.api_key}",
            },
        )
        # 7.3 修: HTTPError 自带状态和响应体(DeepSeek 的 400 会在 body 里写明
        # 哪错了), 原样抛出就只剩 "HTTP Error 400" 干巴巴一句。包一层把
        # 状态和 body 摘要带出来 —— 错误信息的价值在于"告诉你下一步查哪"。
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                data = json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", "replace")[:300]
            raise RuntimeError(f"DeepSeek HTTP {e.code}: {body}") from e
        return data["choices"][0]["message"]["content"]


def create_llm(spec: dict):
    """工厂函数: 根据配置创建 LLM 实例。宿主传 mock 就 mock, 传 deepseek 就 deepseek。"""
    provider = spec.get("provider", "mock")
    if provider == "mock":
        return MockLLM(spec["script"])
    if provider == "deepseek":
        return DeepSeekLLM(spec["api_key"], model=spec.get("model", "deepseek-chat"))
    raise ValueError(f"未知 LLM provider: {provider}")
