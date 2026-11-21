#!/usr/bin/env python3
"""端到端验证 /api/favicon 的 SVG 同源风险已被堵住。

威胁：目标站点的 <link rel="icon"> 指向一个带 onload 的 SVG，
JustNav 抓回来存在本地，然后 /api/favicon 原样同源返回 —— 谁打开那个
URL，脚本就在本站源下执行。

两层防御都要验：
  ① 落盘层：脏 SVG 不被缓存（回退透明 PNG）
  ② HTTP 层：即使是 SVG，响应也带 sandbox CSP

做法：本地起假站点 → 起 JustNav → 请求 /api/favicon → 检查响应。
退出码 0 = 全通过。
"""
import http.server
import os
import shutil
import signal
import socketserver
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
JN_PORT = 8761
SVC_PORT = 8762

EVIL_SVG = b'<svg xmlns="http://www.w3.org/2000/svg" onload = "window.__FAV=1"></svg>'
CLEAN_SVG = b'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>'


def make_server(port, icon_href, svg_bytes):
    """起一个假站点：首页声明 icon_href，该路径返回 svg_bytes。

    注意 favicon 是按 host 缓存的（base = scheme://host，忽略 path），
    所以两个用例必须用不用的端口，否则第二次会命中第一次的 key。
    """
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.startswith(icon_href):
                body, ct = svg_bytes, "image/svg+xml"
            else:
                body = (f'<!doctype html><html><head>'
                        f'<link rel="icon" type="image/svg+xml" href="{icon_href}">'
                        f'</head><body>x</body></html>').encode()
                ct = "text/html; charset=utf-8"
            self.send_response(200)
            self.send_header("Content-Type", ct)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):
            pass

    socketserver.TCPServer.allow_reuse_address = True
    srv = socketserver.TCPServer(("127.0.0.1", port), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv


def main():
    failures = []

    def fail(m):
        failures.append(m)
        print("  ❌ " + m)

    def ok(m):
        print("  ✅ " + m)

    svc_evil = make_server(SVC_PORT, "/evil.svg", EVIL_SVG)
    svc_clean = make_server(SVC_PORT + 1, "/ok.svg", CLEAN_SVG)

    tmp = tempfile.mkdtemp(prefix="justnav-fav-")
    proc = None
    try:
        binary = os.path.join(tmp, "justnav")
        print("→ 编译二进制…")
        env = dict(os.environ, CGO_ENABLED="0")
        env.pop("GOROOT", None)
        subprocess.run(["go", "build", "-o", binary, "."], cwd=ROOT, check=True, env=env)

        data_dir = os.path.join(tmp, "data")
        env = dict(os.environ,
                   JUSTNAV_PORT=str(JN_PORT),
                   JUSTNAV_DATA_DIR=data_dir,
                   JUSTNAV_AUTH="false",
                   JUSTNAV_PROBE="false",
                   JUSTNAV_WALLPAPER="false")
        base = f"http://127.0.0.1:{JN_PORT}"
        proc = subprocess.Popen([binary], env=env,
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(60):
            try:
                urllib.request.urlopen(base + "/api/health", timeout=1)
                break
            except Exception:
                time.sleep(0.2)
        else:
            print("实例没起来")
            return 2

        def fetch(path):
            url = base + path
            req = urllib.request.Request(url, headers={"Origin": base})
            try:
                with urllib.request.urlopen(req, timeout=25) as r:
                    return r.status, dict(r.headers), r.read()
            except urllib.error.HTTPError as e:
                return e.code, dict(e.headers), e.read()

        # ── 1. 脏 SVG 必须被挡
        print("\n[1/2] 目标站点声明带 onload 的 SVG")
        target = urllib.parse.quote(f"http://127.0.0.1:{SVC_PORT}/")
        st, hdr, body = fetch(f"/api/favicon?url={target}")
        ct = hdr.get("Content-Type", "")
        csp = hdr.get("Content-Security-Policy", "")
        miss = hdr.get("X-Favicon", "")
        is_svg = "svg" in ct.lower()
        has_onload = b"onload" in body
        sandboxed = "sandbox" in csp
        print(f"      HTTP {st}  Content-Type {ct}  X-Favicon {miss or '-'}")
        if is_svg and has_onload and not sandboxed:
            fail("脏 SVG 被原样同源返回且无 CSP 隔离")
        elif is_svg and sandboxed:
            ok("返回 SVG 但套了 sandbox CSP（第二层拦住）")
        elif miss == "miss":
            ok("脏 SVG 被拒，回退透明 PNG（第一层拦住）")
        else:
            ok("没拿到脏 SVG")

        # ── 2. 干净 SVG 正常返回，且带 CSP
        print("\n[2/2] 目标站点声明干净 SVG")
        target2 = urllib.parse.quote(f"http://127.0.0.1:{SVC_PORT + 1}/")
        st2, hdr2, body2 = fetch(f"/api/favicon?url={target2}")
        ct2 = hdr2.get("Content-Type", "")
        csp2 = hdr2.get("Content-Security-Policy", "")
        print(f"      HTTP {st2}  Content-Type {ct2}")
        print(f"      CSP {csp2 or '（无）'}")
        if st2 != 200:
            fail(f"干净 SVG 该返回 200，got {st2}")
        elif "svg" in ct2.lower() and "sandbox" not in csp2:
            fail("返回了 SVG 但没带 sandbox CSP")
        else:
            ok("干净图标正常返回，SVG 带 sandbox CSP" if "svg" in ct2.lower()
               else "干净图标正常返回（位图无执行能力，不需要 CSP）")

        print("\n" + "=" * 50)
        if failures:
            print(f"❌ {len(failures)} 条失败")
            return 1
        print("✅ 全部通过")
        return 0
    finally:
        if proc:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
        svc_evil.shutdown()
        svc_clean.shutdown()
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
