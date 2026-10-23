# -*- coding: utf-8 -*-
"""
工具: find_uid —— 从钉钉进程的内存里找出用户 uid。

疑问解答区:
Q: 为什么这么暴力? 不能从配置文件里读 uid 吗?
A: 试过了(第一章侦查时)。钉钉把 uid 存在加密的 storage.db 里, 配置文件中找不到。
   但程序运行时 uid 必然以明文出现在进程内存里(JSON 格式), 所以扫内存是可靠的最后手段。

Q: 扫内存会不会很慢/很危险?
A: 只读不写, 对钉钉无影响。速度上: 只在钉钉进程里扫, 且一旦找到强信号(出现次数多)
   就提前收工, 通常几十秒内。

Q: 怎么知道扫到的数字就是 uid 而不是别的?
A: 看"出现次数"。普通数字在内存里出现几次, uid 出现成百上千次
   (每个网络请求、每条消息都带它)。按出现频率排序, 第一名几乎必然是 uid。
   这体现一个通用原则: 单个证据不可信, 统计特征可信。
"""
import argparse
import ctypes
import json
import re
import subprocess
import sys

# 契约: stdout 恒为 UTF-8(Windows 管道默认 GBK 会炸链, 详见 _shared/config.py 的 Q&A)
sys.stdout.reconfigure(encoding="utf-8")
from ctypes import wintypes


class MEMORY_BASIC_INFORMATION(ctypes.Structure):
    """Windows 内存区域信息结构体。ctypes 的手艺: Python 里描述 C 结构体。"""
    _fields_ = [("BaseAddress", ctypes.c_ulonglong), ("AllocationBase", ctypes.c_ulonglong),
                ("AllocationProtect", wintypes.DWORD), ("__align1", wintypes.DWORD),
                ("RegionSize", ctypes.c_size_t), ("State", wintypes.DWORD),
                ("Protect", wintypes.DWORD), ("Type", wintypes.DWORD)]


def get_dingtalk_pids() -> list[int]:
    """用系统命令 tasklist 列出所有钉钉进程 PID。疑问: 为什么不用 psutil?
    答: 少一个依赖是一个。tasklist 是 Windows 自带的, 够用。

    注意 encoding="gbk": tasklist 的输出编码跟随系统区域设置(简体中文 = GBK),
    不是 UTF-8。乱设 encoding 会 UnicodeDecodeError —— 这是 Windows 上
    调外部命令最常见的坑, 没有之一。"""
    proc = subprocess.run(
        ["tasklist", "/FI", "IMAGENAME eq DingTalk.exe", "/FO", "CSV"],
        capture_output=True, text=True, encoding="gbk", errors="replace", timeout=15,
    )
    pids = []
    for line in proc.stdout.splitlines()[1:]:  # 跳过 CSV 表头
        # 行格式: "DingTalk.exe","1948","Console","1","420,192 K"
        m = re.match(r'"DingTalk\.exe","(\d+)"', line)
        if m:
            pids.append(int(m.group(1)))
    return pids


def scan_pid(pid: int, counts: dict) -> None:
    """扫描单个进程的内存, 统计 uid 模式的出现次数, 结果累积进 counts。"""
    MEM_COMMIT, READABLE = 0x1000, (0x02, 0x04, 0x08, 0x20, 0x40)
    k32 = ctypes.WinDLL("kernel32", use_last_error=True)
    k32.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
    k32.OpenProcess.restype = wintypes.HANDLE
    k32.VirtualQueryEx.argtypes = [wintypes.HANDLE, ctypes.c_ulonglong,
                                   ctypes.POINTER(MEMORY_BASIC_INFORMATION), ctypes.c_size_t]
    k32.ReadProcessMemory.argtypes = [wintypes.HANDLE, ctypes.c_ulonglong, wintypes.LPVOID,
                                      ctypes.c_size_t, ctypes.POINTER(ctypes.c_size_t)]

    proc = k32.OpenProcess(0x0400 | 0x0010, False, pid)  # 查询+读取权限
    if not proc:
        return
    buf = (ctypes.c_char * 262144)()
    addr = 0
    uid_patterns = [
        re.compile(rb'"uid"\s*:\s*"?(\d{9,10})"?'),
        re.compile(rb'"userId"\s*:\s*"?(\d{9,10})"?'),
    ]
    while True:
        mbi = MEMORY_BASIC_INFORMATION()
        if k32.VirtualQueryEx(proc, addr, ctypes.byref(mbi), ctypes.sizeof(mbi)) == 0:
            break
        base, size = mbi.BaseAddress, mbi.RegionSize
        if mbi.State == MEM_COMMIT and mbi.Protect in READABLE and 0 < size < 256 * 1024 * 1024:
            off = 0
            while off < size:
                chunk = min(262144, size - off)
                read = ctypes.c_size_t(0)
                if k32.ReadProcessMemory(proc, base + off, buf, chunk, ctypes.byref(read)):
                    data = buf.raw[:read.value]
                    for pat in uid_patterns:
                        for m in pat.finditer(data):
                            uid = m.group(1).decode()
                            counts[uid] = counts.get(uid, 0) + 1
                off += chunk
        addr = base + size
        if addr > 0x7FFFFFFFFFFF:
            break
    k32.CloseHandle(proc)


def main() -> int:
    parser = argparse.ArgumentParser(description="从钉钉进程内存查找用户 uid")
    parser.parse_args()

    try:
        pids = get_dingtalk_pids()
        if not pids:
            print(json.dumps({"ok": False, "error": "dingtalk_not_running",
                              "hint": "钉钉没在运行, 启动钉钉后再试, 或在配置里手动填 uid"},
                             ensure_ascii=False))
            return 2

        counts = {}
        for pid in pids:
            scan_pid(pid, counts)
            # 提前收工: 已经有强信号就不必扫剩下的进程
            if counts and max(counts.values()) >= 30:
                break

        if not counts:
            print(json.dumps({"ok": False, "error": "uid_not_found"}, ensure_ascii=False))
            return 1

        uid, freq = max(counts.items(), key=lambda x: x[1])
        print(json.dumps({"ok": True, "uid": uid, "confidence": freq}, ensure_ascii=False))
        return 0
    except Exception as e:
        print(json.dumps({"ok": False, "error": "scan_failed", "detail": str(e)}, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    sys.exit(main())
