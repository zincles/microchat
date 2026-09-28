#!/usr/bin/env python3
"""Rust 版 vs Go 版：同一份数据、同一批 GET，**响应逐字节对比**。

迁移期间的卡关工具：接口层转对了没有，这条比"看着像"硬。
用法：
    python3 scripts/go-parity.py                    # 默认比 /health + 全部会话的 messages
    python3 scripts/go-parity.py --rust URL --go URL
    python3 scripts/go-parity.py --path /providers --path /agents
两边必须读**同一份数据**（一般：把 data/ 拷一份给 Go 版跑）。
"""
import argparse, json, sys, urllib.request


def fetch(base: str, path: str) -> str:
    with urllib.request.urlopen(base.rstrip("/") + path) as response:
        return response.read().decode()  # 不 strip：换行也要对齐


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--rust", default="http://127.0.0.1:8787/api/v1")
    parser.add_argument("--go", default="http://127.0.0.1:8803/api/v1")
    parser.add_argument("--path", action="append", default=[], help="额外要比的路径（可多次）")
    args = parser.parse_args()

    paths = ["/health", "/conversations", *args.path]
    try:
        for conversation in json.loads(fetch(args.rust, "/conversations")):
            paths.append(f"/conversations/{conversation['id']}/messages")
    except Exception as error:  # 后端没起 / 鉴权没配
        print(f"✗ 拉不了会话列表：{error}")
        return 2

    failures = []
    for path in paths:
        try:
            rust, go = fetch(args.rust, path), fetch(args.go, path)
        except Exception as error:
            print(f"✗ {path:52} {error}")
            failures.append(path)
            continue
        if rust == go:
            print(f"✓ {path:52} 逐字节一致")
        else:
            print(f"✗ {path:52} **不一致**")
            for index, (left, right) in enumerate(zip(rust, go)):
                if left != right:
                    print(f"    首个差异在第 {index} 字节：")
                    print(f"      Rust: …{rust[max(0,index-60):index+60]}…")
                    print(f"      Go  : …{go[max(0,index-60):index+60]}…")
                    break
            else:
                print(f"      Rust {len(rust)} 字节 / Go {len(go)} 字节（一个是另一个的前缀）")
            failures.append(path)

    print()
    if failures:
        print(f"✗ {len(failures)}/{len(paths)} 条不一致")
        return 1
    print(f"★ {len(paths)} 条响应**逐字节全等** ✓✓")
    return 0


if __name__ == "__main__":
    sys.exit(main())
