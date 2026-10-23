# -*- coding: utf-8 -*-
"""
统一配置管理 —— 所有 skill 的 run.py 都只从这里拿配置, 不许自己读文件。

疑问解答区:
Q: 为什么要有 config.example.yaml 和 config.yaml 两个文件?
A: example 是"说明书", 入库, 告诉所有人有哪些配置项; config.yaml 是"你的答案",
   被 .gitignore 挡在 git 门外, 里面的 api_key 永远不会泄露。
   这是业界管理密钥的标准做法(Go 的 .env / .env.example 同理)。

Q: deep_merge 是干嘛的?
A: 你只写了 config.yaml 里的 3 个字段, 其他字段从 DEFAULTS 补。
   好处: 配置项增加新版本后, 你的旧 config.yaml 不用改也能跑(新字段用默认值)。

Q: 为什么 get_dingtalk 里能自动推导 db_path 和 salt?
A: 这是"单一信息源"原则: data_dir 才是根, db_path/salt 都是它的派生物。
   你只管填 data_dir, 派生的事交给代码 —— 人只记一件事, 机器推导其余。

Q: 下面这行 stdout.reconfigure 是干嘛的?
A: 这是血的教训(6.5.6 编码事故): Python 在 Windows 上把 stdout 接到管道时,
   默认编码是系统区域设置(中文机 = GBK/cp936), 不是 UTF-8。谁启动我们,
   环境就不一样: 你从 PowerShell 跑可能有 PYTHONIOENCODING=utf-8, 而 Go
   daemon 从托盘启动时啥都没有 → 我们输出的中文按 GBK 编码 → 父进程按
   UTF-8 解码炸掉 → 链式崩溃, 宿主只看到"未知错误"。
   契约是"stdout 恒为 UTF-8"(Go 端 json.Unmarshal 只认 UTF-8), 所以由产生
   方(我们自己)在入口把编码钉死, 不依赖任何外部环境的恩赐。
   放这里是因为所有 skill 的 run.py 都 import config —— 一处修复全覆盖。
"""
import base64
import copy
import json
import sys
from pathlib import Path

import yaml

# 契约: 本进程 stdout 恒为 UTF-8。必须在任何 print 之前生效, 所以放模块级。
sys.stdout.reconfigure(encoding="utf-8")

# 第一层: 默认值。你 config.yaml 里没写的项, 从这儿补。
DEFAULTS = {
    "debug": True,
    "llm": {"provider": "mock", "api_key": "", "model": "deepseek-chat", "mock_script_file": ""},
    "dingtalk": {"uid": "", "salt": "", "data_dir": "", "db_path": ""},
    "collect": {"conversations": [], "since_hour": "9:00", "only_mine": True, "max_messages": 500,
                "date": "", "output_file": ""},
    "report": {"messages_file": "", "corrections": "data/corrections.md", "date": ""},
}


def project_root() -> Path:
    """项目根目录。本文件在 skills/_shared/ 下, 往上三级就是根。
    疑问: 为什么用 __file__ 推导, 不用 os.getcwd()?
    答: cwd 是"你从哪运行"会变; __file__ 是"我躺在哪"永远不变。
       工具必须依赖后者, 否则换个目录运行就找不到配置了。"""
    return Path(__file__).resolve().parent.parent.parent


def deep_merge(base: dict, override: dict) -> dict:
    """递归合并: override 里有的覆盖 base, 没有的保留 base。"""
    for key, value in (override or {}).items():
        if isinstance(value, dict) and isinstance(base.get(key), dict):
            deep_merge(base[key], value)
        else:
            base[key] = value
    return base


def load_config() -> dict:
    """读 config.yaml 并与默认值合并。这是唯一合法的读配置入口。"""
    path = project_root() / "config.yaml"
    if not path.exists():
        print(json.dumps({
            "ok": False,
            "error": "config_not_found",
            "hint": f"把 config.example.yaml 复制为 config.yaml 再改: {path}",
        }, ensure_ascii=False))
        sys.exit(1)
    user_cfg = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    return deep_merge(copy.deepcopy(DEFAULTS), user_cfg)


def read_stdin_json() -> dict:
    """读 stdin 的 JSON, 全程只读一次(带缓存)。

    教训(6.2 实测踩坑): 以前 merge_stdin_overrides 和 report-writer 各自
    json.load(sys.stdin) —— stdin 是流, 第一次读完第二次就是 EOF,
    导致生产路径(debug=false)永远拿不到消息。所以读 stdin 必须收口到这一个函数。
    stdin 没东西/不是 JSON 都按 {} 处理(比如人手动跑、IDE 点运行按钮)。

    教训(7.3 生产事故, 排查了一下午): 不能 json.load(sys.stdin)!
    文本模式的 sys.stdin 编码随环境变 —— 有 PYTHONIOENCODING=utf-8 的机器
    按 UTF-8 解码(对), 没有的 Windows 机器按 cp936 + surrogateescape 解码:
    宿主喂进来的 UTF-8 中文变成孤立代理字符 \\udcXX(无效 Unicode),
    json.dumps 原样转义传给 DeepSeek → API 直接 400 → traceback → 降级。
    而且"能不能复现"取决于你从哪个终端启动 daemon —— 同一份代码时好时坏。
    解法与 stdout 那边(6.5.6)对称: 绕过文本层, 读原始字节自己按 UTF-8 解码,
    钉死契约, 不依赖任何外部环境的恩赐。"""
    global _STDIN_CACHE
    if _STDIN_CACHE is None:
        try:
            raw = sys.stdin.buffer.read()  # 原始字节, 绕过环境相关的文本解码层
            _STDIN_CACHE = json.loads(raw.decode("utf-8")) if raw.strip() else {}
        except (json.JSONDecodeError, UnicodeDecodeError, EOFError, OSError,
                AttributeError):  # AttributeError: 测试环境 stdin 可能是 StringIO, 没有 .buffer
            _STDIN_CACHE = {}
    return _STDIN_CACHE


# read_stdin_json 的缓存。None = 还没读过。
_STDIN_CACHE = None


def run_guarded(main_fn):
    """门面入口的统一兜底(7.3 审计后收口): 任何未捕获异常都转成结构化 JSON + 退出码 1,
    不让宿主收到裸 traceback。守住"stdout 恒为 JSON"契约的最后一道防线。
    用法: if __name__ == "__main__": sys.exit(run_guarded(main))"""
    try:
        return main_fn()
    except Exception as e:  # noqa: BLE001 —— 门面层的最后一道防线, 就是要全兜
        import traceback
        print(json.dumps({"ok": False, "error": type(e).__name__,
                          "detail": f"{e}", "trace": traceback.format_exc()[-400:]},
                         ensure_ascii=False))
        return 1


def merge_stdin_overrides(cfg: dict) -> dict:
    """第三层: 宿主经 stdin 传入的配置覆盖文件配置。
    疑问: 为什么要这层? 答: 上线后 Go 服务可能管理多份配置(界面改的),
    它传入的应该优先于文件。开发期 debug=true 跳过这层。

    例外: stdin 里显式给了 "debug" 字段就以它为准 —— 宿主用它在
    生产/调试之间切换, 这是唯一合法的远程开关(6.2 踩坑后补的规则)。"""
    incoming = read_stdin_json()
    if not incoming:
        return cfg
    if "debug" in incoming:
        cfg["debug"] = incoming["debug"]
    if cfg.get("debug"):
        return cfg
    return deep_merge(cfg, incoming)


def get_dingtalk(cfg: dict) -> dict:
    """取钉钉配置并推导派生字段。返回完整可用的 {db_path, uid, salt, data_dir}。"""
    dt = dict(cfg.get("dingtalk") or {})
    data_dir = dt.get("data_dir") or ""

    # 派生1: db_path = data_dir/DBFiles/dingtalk.db
    if not dt.get("db_path") and data_dir:
        dt["db_path"] = str(Path(data_dir) / "DBFiles" / "dingtalk.db")

    # 派生2: salt 从 user_config 里自动提取(Base64 编码的 JSON)
    if not dt.get("salt") and data_dir:
        uc = Path(data_dir) / "user_config"
        if uc.exists():
            raw = json.loads(base64.b64decode(uc.read_bytes()))
            dt["salt"] = raw.get("salt", "")

    return dt
