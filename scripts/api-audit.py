#!/usr/bin/env python3
"""客户端 ↔ 服务端 API 全量对账（静态抽取 + 真实请求逐条验证）。

用法（在仓库根目录，先 `cargo build`）：

    python3 scripts/api-audit.py

它会：
1. 从 `src/server.rs` 抽路由表与处理器签名（方法 / 路径 / 请求体类型 / 响应类型）；
2. 从 `src/client.rs` 抽客户端全部调用点，比对**方法与路径**；
3. 起一个沙盒后端（`/tmp/microchat-api-audit` 下的 config/data，**不碰**你的 `config/`、`data/`），
   外加一个假上游，然后对**每条路由**：重建夹具 → 发正确方法 → 断言期望状态码，
   再发一个错误方法 → 断言 405。

AGENTS.md 里那张「HTTP API」表就是它核出来的；改了接口就跑一遍。
"""
import json
import pathlib
import re
import shutil
import socket
import subprocess
import sys
import threading
import time
import http.server
import urllib.error
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
PORT = 8791
BASE = f'http://127.0.0.1:{PORT}/api/v1'
SANDBOX = pathlib.Path('/tmp/microchat-api-audit')

srv = (ROOT / 'src/server.rs').read_text()
cli = (ROOT / 'src/frontend/client.rs').read_text()

# ── 1) 路由表 + 处理器签名 ────────────────────────────────────────
routes: dict[str, dict[str, str]] = {}
for m in re.finditer(
    r'\.route\(\s*"([^"]+)"\s*,\s*([a-z]+)\((\w+)\)\s*(?:\.([a-z]+)\((\w+)\))?', srv
):
    path, m1, h1, m2, h2 = m.groups()
    routes.setdefault(path, {})[m1.upper()] = h1
    if m2:
        routes[path][m2.upper()] = h2


def signature(name: str):
    """括号配平地取出 `(参数)` 与 `-> 返回类型`——参数里嵌着括号，正则数不清。"""
    start = srv.find('async fn ' + name + '(')
    if start < 0:
        return None, None
    i = srv.index('(', start)
    depth, j = 0, i
    while j < len(srv):
        if srv[j] == '(':
            depth += 1
        elif srv[j] == ')':
            depth -= 1
            if depth == 0:
                break
        j += 1
    args = ' '.join(srv[i + 1:j].split())
    arrow = srv.index('->', j)
    brace = srv.index('{', arrow)
    return args, ' '.join(srv[arrow + 2:brace].split())


def handler_types(name: str):
    args, ret = signature(name)
    if args is None:
        return '?', '?'
    req = '（无请求体）'
    for mm in re.finditer(r'Json\(req\): Json<(\w+)>', args):
        req = mm.group(1)
    ret = ret.strip()
    for pattern, repl in (
        (r'Result<\(StatusCode, Json<(.+?)>\), ApiError>', r'\1 · 201/202'),
        (r'Result<Json<(.+?)>, ApiError>', r'\1'),
        (r'Result<StatusCode, ApiError>', r'204 无内容'),
    ):
        ret = re.sub(pattern, repl, ret)
    return req, ret


# ── 2) 客户端调用点 ───────────────────────────────────────────────
calls: set[tuple[str, str]] = set()
for m in re.finditer(
    r'(write_json|write|get)\(\s*http,\s*(?:reqwest::Method::([A-Z]+),\s*)?&?format!\(\s*"([^"]*)"',
    cli,
):
    fn, method, url = m.groups()
    calls.add((method or 'GET', url))
for m in re.finditer(r'(write_json|write|get)\(\s*http,\s*reqwest::Method::([A-Z]+),\s*"([^"]*)"', cli):
    fn, method, url = m.groups()
    calls.add((method, url))
for m in re.finditer(r'(write_json|write|get)\(\s*http,\s*&base,\s*[^,]+,\s*"([^"]*)"', cli):
    fn, url = m.groups()
    calls.add(('GET', url))


def norm(u: str) -> str:
    return re.sub(r'\{[^}]*\}', '{}', u.split('/api/v1')[-1])


client_calls = sorted({(m, norm(u)) for m, u in calls if 'api/v1' in u})
route_norm = {norm(p): ms for p, ms in routes.items()}

print('════ 静态对账：客户端调用 vs 服务端路由 ════')
mismatches = []
for method, path in client_calls:
    allow = route_norm.get(path)
    ok = allow is not None and method in allow
    if not ok:
        mismatches.append((method, path, allow))
    print(f'  {method:6s} {path:46s} {"✓" if ok else f"✗ 服务端只挂 {allow}"}')
print('  结论：', '全对上 ✓' if not mismatches else f'✗ {mismatches}')


# ── 3) 沙盒后端 + 假上游 ─────────────────────────────────────────
class Stub(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"data":[{"id":"stub-model","name":"Stub Model"}]}'
        self.send_response(200)
        self.send_header('content-type', 'application/json')
        self.send_header('content-length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        # 必须说 **SSE**：provider 默认开流，对话请求会带 `stream: true`，
        # 回普通 JSON 会被当成"流里一个字都没有"（对账就会在夹具里翻车）。
        self.rfile.read(int(self.headers.get('content-length', 0)))
        body = (
            'data: {"choices":[{"delta":{"content":"沙盒回复"}}]}\n\n'
            'data: [DONE]\n\n'
        ).encode()
        self.send_response(200)
        self.send_header('content-type', 'text/event-stream')
        self.send_header('content-length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


def wait_port(port: int, timeout: float = 20.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        with socket.socket() as s:
            if s.connect_ex(('127.0.0.1', port)) == 0:
                return
        time.sleep(0.1)
    raise SystemExit(f'端口 {port} 一直没起来')


def api(method: str, path: str, data=None):
    req = urllib.request.Request(
        BASE + path,
        method=method,
        data=json.dumps(data).encode() if data is not None else None,
        headers={'content-type': 'application/json'},
    )
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, (json.loads(raw) if raw else None)
        except Exception:
            return e.code, None


shutil.rmtree(SANDBOX, ignore_errors=True)
(SANDBOX / 'config').mkdir(parents=True)
(SANDBOX / 'data').mkdir(parents=True)
(SANDBOX / 'config' / 'config.json').write_text(
    json.dumps({"version": 1, "server": {"host": "127.0.0.1", "port": PORT}, "chat": {"title_chars": 30}})
)

stub = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Stub)
threading.Thread(target=stub.serve_forever, daemon=True).start()
STUB = f'http://127.0.0.1:{stub.server_address[1]}/v1'

server = subprocess.Popen(
    [str(ROOT / 'target/debug/server')],
    env={
        'PATH': '/usr/bin:/bin',
        'MICROCHAT_CONFIG_DIR': str(SANDBOX / 'config'),
        'MICROCHAT_DATA_DIR': str(SANDBOX / 'data'),
    },
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
)
try:
    wait_port(PORT)

    def seed():
        """每条路由都从干净夹具开始——上一条 DELETE 别把下一条的地基拆了。"""
        api('DELETE', '/providers/stub')
        api('DELETE', '/providers/seeded')
        api('POST', '/providers',
            {"id": "stub", "kind": "openai-compat", "base_url": STUB, "api_key": "sk-stub"})
        _, agent = api('POST', '/agents', {"name": "对账用 agent", "system_prompt": "x"})
        _, conv = api('POST', '/conversations', {"provider": "stub", "model": "stub-model"})
        api('POST', f"/conversations/{conv['id']}/messages", {"content": "在吗"})
        time.sleep(0.5)
        _, msgs = api('GET', f"/conversations/{conv['id']}/messages")
        return {'cid': conv['id'], 'aid': msgs[1]['id'], 'pid': 'stub', 'agent': agent['id']}

    def fill(path: str, f) -> str:
        out = path.replace('{name}', 'config.json')
        if out.startswith('/conversations/{id}/messages/{message_id}'):
            return out.replace('{id}', f['cid']).replace('{message_id}', f['aid'])
        if out.startswith('/conversations/{id}'):
            return out.replace('{id}', f['cid'])
        if out.startswith('/providers/{id}'):
            return out.replace('{id}', f['pid'])
        if out.startswith('/agents/{id}'):
            return out.replace('{id}', f['agent'])
        return out

    bodies = {
        ('POST', '/conversations'): {"provider": "stub", "model": "stub-model"},
        ('PATCH', '/conversations/{id}'): {"title": "改个名"},
        ('POST', '/conversations/{id}/messages'): {"content": "再来一句"},
        ('PATCH', '/conversations/{id}/messages/{message_id}'): {"content": "改一句"},
        ('POST', '/conversations/{id}/resend'): {},
        ('POST', '/conversations/{id}/archive'): {},
        ('POST', '/conversations/{id}/compact'): {"blocks": 1},
        ('POST', '/conversations/{id}/fork'): {},
        ('POST', '/conversations/{id}/stop'): {},
        ('POST', '/providers'): {"id": "seeded", "kind": "openai-compat", "base_url": STUB},
        ('POST', '/providers/{id}/refresh'): {},
        ('PATCH', '/providers/{id}'): {"base_url": STUB},
        ('PUT', '/config/chat'): {"title_chars": 32, "model_context_tokens": 131072,
                                  "compact_trigger_tokens": None},
        ('PUT', '/subagents'): {"version": 1, "tools": {}},
        ('PATCH', '/models'): {"provider": "stub", "upstream_id": "不存在的模型",
                               "context_override": 123456},
        ('POST', '/models/probe'): {"base_url": STUB},
        ('POST', '/agents'): {"name": "新 agent", "system_prompt": "y"},
        ('PATCH', '/agents/{id}'): {"name": "改过的 agent"},
    }
    expect = {
        ('GET', '/health'): 200, ('GET', '/conversations'): 200, ('POST', '/conversations'): 201,
        ('PATCH', '/conversations/{id}'): 200, ('DELETE', '/conversations/{id}'): 204,
        ('GET', '/conversations/{id}/messages'): 200, ('POST', '/conversations/{id}/messages'): 202,
        ('PATCH', '/conversations/{id}/messages/{message_id}'): 200,
        ('DELETE', '/conversations/{id}/messages/{message_id}'): 200,
        ('DELETE', '/conversations/{id}/messages/{message_id}/siblings'): 200,
        ('GET', '/conversations/{id}/outgoing'): 200, ('GET', '/conversations/{id}/variables'): 200,
        ('GET', '/conversations/{id}/context'): 200,
        ('GET', '/conversations/{id}/summaries'): 200,
        ('GET', '/conversations/{id}/export'): 200,
        ('POST', '/conversations/{id}/archive'): 200, ('POST', '/conversations/{id}/compact'): 202,
        ('POST', '/conversations/{id}/fork'): 201,
        ('GET', '/config/chat'): 200, ('PUT', '/config/chat'): 200,
        ('GET', '/subagents'): 200, ('PUT', '/subagents'): 200,
        ('GET', '/tasks'): 200,
        # 库里没有这个模型 ⇒ 404：验的是"路由在、参数被解析"，不是"模型存在"
        ('PATCH', '/models'): 404,
        ('GET', '/conversations/{id}/branches'): 200, ('GET', '/conversations/{id}/status'): 200,
        ('GET', '/conversations/{id}/turn/text'): 200,
        ('POST', '/conversations/{id}/resend'): 202, ('POST', '/conversations/{id}/stop'): 200,
        ('GET', '/providers'): 200, ('POST', '/providers'): 201,
        ('PATCH', '/providers/{id}'): 200, ('DELETE', '/providers/{id}'): 204,
        ('POST', '/providers/{id}/refresh'): 200, ('DELETE', '/providers/{id}/models'): 200,
        ('GET', '/models'): 200, ('POST', '/models/probe'): 200,
        ('GET', '/agents'): 200, ('POST', '/agents'): 201,
        ('PATCH', '/agents/{id}'): 200, ('DELETE', '/agents/{id}'): 204,
        ('GET', '/debug/state'): 200, ('GET', '/debug/file/{name}'): 200,
        ('GET', '/debug/last-payload'): 200,
    }

    print('\n════ 接口清单（抽取自处理器签名）════')
    for path, methods in sorted(routes.items()):
        for method, handler in sorted(methods.items()):
            req, ret = handler_types(handler)
            print(f'  {method:6s} {path:52s} {req:22s} → {ret}')

    print('\n════ 动态验证：真实请求（每条前重建夹具）════')
    problems = []
    for path, methods in sorted(routes.items()):
        for method, _handler in sorted(methods.items()):
            f = seed()
            target = fill(path, f)
            code, _ = api(method, target, bodies.get((method, path)))
            want = expect.get((method, path))
            ok = code == want
            if not ok:
                problems.append((method, path, code, want))
            print(f'  {method:6s} {path:52s} → {code} 期望 {want} {"✓" if ok else "✗"}')
            wrong = next(m for m in ('PATCH', 'DELETE', 'GET', 'PUT') if m not in methods)
            code2, _ = api(wrong, target, None)
            if code2 != 405:
                problems.append((wrong, path, code2, 405))
            print(f'    （{wrong}）→ {code2} 期望 405 {"✓" if code2 == 405 else "✗"}')

    print('\n════ 结论 ════')
    print('  客户端与服务端路由：', '全对上 ✓' if not mismatches else mismatches)
    print('  真实请求：', '全部符合期望 ✓' if not problems else problems)
    sys.exit(0 if not (mismatches or problems) else 1)
finally:
    server.terminate()
    shutil.rmtree(SANDBOX, ignore_errors=True)
