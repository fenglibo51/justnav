#!/usr/bin/env python3
"""XSS 回归测试 —— 端到端，起真实例、注真 payload、用真浏览器断言。

为什么不用纯静态扫描：JustNav 的前端是单文件 index.html，模板串里几十处
拼接，静态规则（"有没有 esc()"）既会漏也会误报。唯一可信的判据是
「这段 payload 在真浏览器里到底执行了没有」。

依赖：
    pip install playwright && playwright install chromium

用法：
    python3 scripts/test-xss.py                 # 自动起临时实例、测完清理
    python3 scripts/test-xss.py --port 8700     # 换端口
    python3 scripts/test-xss.py --keep          # 保留临时数据目录

退出码 0 = 全部通过；非 0 = 有断言失败（可直接进 CI）。
"""
import argparse
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

# ── payload 组合：每个都对应一个真实注入点 ────────────────────────────────
# 注意分组名有 24 字上限，所以分组那条走 items.group（该字段没有长度校验）。
P_GROUP = "<img src=x onerror=window.__X1=1>"          # 分组名（经 items.group 写入）
P_TAG = '"><svg onload=window.__X2=1>'                # 条目 tag
P_HIDDEN = "<svg onload=window.__X4=1>"               # 隐藏条目的名字
P_ENG_ID = '"><svg onload=window.__X7=1>'             # 搜索引擎 id
P_ENG_NAME = "<svg onload=window.__X8=1>"             # 搜索引擎名字
P_ENG_URL = 'https://x/%s"><svg onload=window.__X9=1>'  # 搜索引擎 url
P_SEARCH = "<img src=x onerror=window.__S1=1>"        # 搜索框里现打的字
# 书签标题必须是 HTML 实体版 —— DOMParser 的 textContent 会把原始标签吃掉，
# 实体版才会在预览时还原成字面标签、才测得到真漏洞。
P_BOOKMARK = "&lt;img src=x onerror=window.__B3=1&gt;"

ALL_FLAGS = ["__X1", "__X2", "__X4", "__X7", "__X8", "__X9", "__S1", "__B3"]

BOOKMARK_FILE = """<!DOCTYPE NETSCAPE-Bookmark-file-1>
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><H3 ADD_DATE="1700000000">书签栏</H3>
    <DL><p>
        <DT><A HREF="https://example.com/a" ADD_DATE="1700000001">%s</A>
        <DT><A HREF="https://example.com/b" ADD_DATE="1700000002">第二个书签</A>
    </DL><p>
</DL><p>
""" % P_BOOKMARK


class Api:
    def __init__(self, base):
        self.base = base
        self.cookie = ""

    def call(self, method, path, payload=None):
        data = json.dumps(payload).encode() if payload is not None else None
        headers = {"Content-Type": "application/json"}
        if self.cookie:
            headers["Cookie"] = self.cookie
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                if not self.cookie:
                    self.cookie = (r.headers.get("Set-Cookie") or "").split(";")[0]
                return r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()


def wait_health(base, tries=60):
    for _ in range(tries):
        try:
            with urllib.request.urlopen(base + "/api/health", timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        time.sleep(0.5)
    return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8701)
    ap.add_argument("--keep", action="store_true", help="保留临时数据目录")
    ap.add_argument("--binary", default=None, help="已编译的二进制路径（默认临时构建）")
    args = ap.parse_args()

    tmpdir = tempfile.mkdtemp(prefix="justnav-xss-")
    proc = None
    failures = []

    def fail(msg):
        failures.append(msg)
        print("  ❌ " + msg)

    def ok(msg):
        print("  ✅ " + msg)

    try:
        # ── 1. 准备二进制
        binary = args.binary
        if not binary:
            binary = os.path.join(tmpdir, "justnav")
            print("→ 编译二进制…")
            env = dict(os.environ, CGO_ENABLED="0")
            # 用户环境里可能是 go1.x 自带 toolchain，别覆盖它的 GOROOT
            env.pop("GOROOT", None)
            subprocess.run(["go", "build", "-o", binary, "."], cwd=ROOT, check=True, env=env)
        print("→ 二进制:", binary)

        # ── 2. 起实例
        data_dir = os.path.join(tmpdir, "data")
        env = dict(os.environ,
                   JUSTNAV_PORT=str(args.port),
                   JUSTNAV_DATA_DIR=data_dir,
                   JUSTNAV_AUTH="true",
                   JUSTNAV_USER="tester",
                   JUSTNAV_PASSWORD="TestPw123456",
                   JUSTNAV_INTERNAL_HOST="127.0.0.1",
                   JUSTNAV_PROBE="false",
                   JUSTNAV_WALLPAPER="false")
        base = f"http://127.0.0.1:{args.port}"
        print("→ 启动实例:", base)
        proc = subprocess.Popen([binary], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        if not wait_health(base):
            print("实例没起来，stderr:", (proc.stderr.read() or b"").decode()[:800])
            return 2

        # ── 3. 注入恶意数据（全部走真实 API，等价于用户在界面上这么填）
        #
        # 每一次注入都必须 HTTP 2xx —— 注入失败还继续跑，那些 "payload 没执行"
        # 的断言就是假绿：页面里压根没这条数据，当然不会执行。
        api = Api(base)
        api.call("POST", "/api/auth/login", {"username": "tester", "password": "TestPw123456"})

        def must(desc, method, path, payload=None):
            st, body = api.call(method, path, payload)
            if not (200 <= st < 300):
                fail(f"注入失败：{desc} → HTTP {st} {body[:160]}")
                return False
            return True

        injected = []
        injected.append(must("建正常组", "POST", "/api/groups", {"name": "正常组"}))
        injected.append(must("正常卡片", "POST", "/api/items", {
            "name": "正常卡片", "group": "正常组", "internal": "http://127.0.0.1:1/"}))
        injected.append(must("分组名 payload", "POST", "/api/items", {
            "name": "X1组里的条目", "group": P_GROUP, "internal": "http://127.0.0.1:1/"}))
        injected.append(must("隐藏条目 payload", "POST", "/api/items", {
            "name": P_HIDDEN, "group": "正常组", "internal": "http://127.0.0.1:1/", "hidden": True}))
        injected.append(must("tag payload", "POST", "/api/items", {
            "name": "带tag卡片", "group": "正常组", "internal": "http://127.0.0.1:1/", "tag": P_TAG}))
        injected.append(must("搜索引擎 payload", "POST", "/api/settings", {
            "engines": [
                {"id": "baidu", "name": "百度", "url": "https://www.baidu.com/s?wd=%s"},
                {"id": P_ENG_ID, "name": P_ENG_NAME, "url": P_ENG_URL},
            ],
            "engineId": P_ENG_ID,
        }))

        if not all(injected):
            print("\n有注入没成功，后续断言不可信，直接中止")
            return 1

        # 注入完再回读一遍，确认数据真的落盘了（不是接口 200 但什么都没存）
        st, body = api.call("GET", "/api/items")
        if st != 200:
            fail(f"回读 /api/items 失败：HTTP {st}")
            return 1
        d = json.loads(body)
        groups = d.get("groups", [])
        items = d.get("items", [])
        if P_GROUP not in groups:
            fail(f"分组 payload 没落盘：groups={groups[:5]}")
        names = [it.get("name", "") for it in items]
        if P_HIDDEN not in names:
            fail("隐藏条目 payload 没落盘")
        if not any(it.get("tag") == P_TAG for it in items):
            fail("tag payload 没落盘")
        if P_ENG_ID not in [e.get("id") for e in (d.get("settings") or {}).get("engines", [])]:
            fail("搜索引擎 payload 没落盘")
        if failures:
            print("\n注入校验没过，中止 —— 不能拿没注入的数据当没执行")
            return 1

        # ── 4. 浏览器阶段
        from playwright.sync_api import sync_playwright

        bookmark_path = os.path.join(tmpdir, "bookmarks.html")
        with open(bookmark_path, "w", encoding="utf-8") as f:
            f.write(BOOKMARK_FILE)

        with sync_playwright() as p:
            launch_kw = {"headless": True,
                         "args": ["--no-sandbox", "--disable-dev-shm-usage", "--no-proxy-server"]}
            # 容器里通常要用预装的 chromium；找不到就让 playwright 自己找
            chrome = os.environ.get("JUSTNAV_TEST_CHROME")
            if chrome and os.path.exists(chrome):
                launch_kw["executable_path"] = chrome
            b = p.chromium.launch(**launch_kw)
            pg = b.new_page(viewport={"width": 2560, "height": 1440})
            errors = []
            pg.on("pageerror", lambda e: errors.append(str(e)))

            pg.goto(base, wait_until="domcontentloaded", timeout=30000)
            pg.wait_for_timeout(800)
            # 密码框用 evaluate 设值：Playwright 的 fill 对它不生效
            pg.evaluate("""() => {
                document.getElementById('lgUser').value = 'tester';
                document.getElementById('lgPass').value = 'TestPw123456';
            }""")
            pg.click("#lgBtn")
            pg.wait_for_timeout(3500)
            if pg.evaluate("() => document.body.classList.contains('locked')"):
                print("登录失败，后续断言无意义")
                return 2

            print("\n[1/5] 首屏（P0-1 分组名 / P0-1 tag）")
            s = pg.evaluate("""() => ({
                flags: {X1: window.__X1||0, X2: window.__X2||0},
                cards: document.querySelectorAll('.card').length,
                imgs: document.querySelectorAll('img[src="x"]').length,
                svgs: document.querySelectorAll('svg[onload]').length,
                h2: Array.from(document.querySelectorAll('.sect-h h2')).map(x=>x.textContent)
            })""")
            if s["flags"]["X1"]: fail("P0-1 分组名 payload 执行了 (__X1=1)")
            else: ok("P0-1 分组名未执行")
            if s["flags"]["X2"]: fail("P0-1 条目 tag payload 执行了 (__X2=1)")
            else: ok("P0-1 条目 tag 未执行")
            if s["imgs"]: fail(f"页面出现 {s['imgs']} 个 img[src=x] —— 有 HTML 被当标签解析")
            else: ok("没有 img[src=x] 节点")
            if s["svgs"]: fail(f"页面出现 {s['svgs']} 个 svg[onload]")
            else: ok("没有 svg[onload] 节点")
            if s["cards"] < 1: fail("正常卡片没渲染出来（可能整页被 payload 打崩）")
            else: ok(f"正常卡片 {s['cards']} 张，页面没崩")

            print("\n[2/5] 搜索建议（P0-2）")
            pg.click("#q")
            pg.type("#q", P_SEARCH, delay=8)
            pg.wait_for_timeout(900)
            s = pg.evaluate("""() => ({
                S1: window.__S1||0,
                imgs: document.querySelectorAll('.sug-item img[src="x"]').length,
                open: !!document.querySelector('.sug.open, #sug.open')
            })""")
            if s["S1"]: fail("P0-2 搜索词 payload 执行了 (__S1=1)")
            else: ok("P0-2 搜索词未执行")
            if s["imgs"]: fail("搜索建议里出现 img[src=x]")
            else: ok("搜索建议里没有 img 节点")
            pg.evaluate("() => { const q=document.getElementById('q'); q.value=''; q.dispatchEvent(new Event('input',{bubbles:true})); }")
            pg.keyboard.press("Escape")
            pg.wait_for_timeout(300)

            print("\n[3/5] 搜索引擎菜单（P0-3）")
            pg.click("#btnEngine")
            pg.wait_for_timeout(700)
            s = pg.evaluate("""() => ({
                X7: window.__X7||0, X8: window.__X8||0, X9: window.__X9||0,
                svgs: document.querySelectorAll('#engmenu svg[onload]').length
            })""")
            for k in ("X7", "X8", "X9"):
                if s[k]: fail(f"P0-3 引擎 payload 执行了 (_{k}=1)")
                else: ok(f"P0-3 引擎 {k} 未执行")
            if s["svgs"]: fail("引擎菜单里出现 svg[onload]")
            else: ok("引擎菜单里没有 svg[onload]")
            pg.keyboard.press("Escape")
            pg.wait_for_timeout(300)

            print("\n[4/5] 隐藏区（P0-4）")
            # 隐藏按钮要等 paintHidden() 跑过才显示；主题面板是目前的触发入口
            pg.evaluate("""() => {
                const b = document.getElementById('btnTheme') || document.getElementById('btnTheme2');
                if (b) b.click();
            }""")
            pg.wait_for_timeout(700)
            disp = pg.evaluate("() => getComputedStyle(document.getElementById('btnHiddenEntry')).display")
            if disp == "none":
                fail("隐藏按钮始终不显示，P0-4 这条测不到（HIDDEN 有数据但 UI 没刷新）")
            else:
                pg.click("#btnHiddenEntry")
                pg.wait_for_timeout(800)
                s = pg.evaluate("""() => ({
                    X4: window.__X4||0,
                    svgs: document.querySelectorAll('#hiddenList svg').length,
                    rows: document.querySelectorAll('.hidden-row').length
                })""")
                if s["X4"]: fail("P0-4 隐藏条目名 payload 执行了 (__X4=1)")
                else: ok("P0-4 隐藏条目名未执行")
                if s["svgs"]: fail("隐藏区里出现 svg 节点")
                else: ok("隐藏区里没有 svg 节点")
                if s["rows"] < 1: fail("隐藏区没渲染出条目")
                else: ok(f"隐藏区渲染 {s['rows']} 条")
                pg.keyboard.press("Escape")
                pg.wait_for_timeout(300)

            print("\n[5/5] 书签导入预览（P0-5）")
            pg.set_input_files("#impFile", bookmark_path)
            pg.wait_for_timeout(1500)
            s = pg.evaluate("""() => ({
                B3: window.__B3||0,
                imgs: document.querySelectorAll('#impList img[src="x"]').length,
                rows: document.querySelectorAll('.imp-row').length
            })""")
            if s["B3"]: fail("P0-5 书签标题 payload 执行了 (__B3=1)")
            else: ok("P0-5 书签标题未执行")
            if s["imgs"]: fail("书签预览里出现 img[src=x]")
            else: ok("书签预览里没有 img 节点")
            if s["rows"] < 1: fail("书签预览没渲染出条目")
            else: ok(f"书签预览渲染 {s['rows']} 条")

            if errors:
                fail(f"页面有 JS 报错: {errors[:3]}")
            else:
                ok("全程无 JS 报错")

            b.close()

    finally:
        if proc and proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                proc.kill()
        if args.keep:
            print("\n临时目录保留:", tmpdir)
        else:
            shutil.rmtree(tmpdir, ignore_errors=True)

    print("\n" + "=" * 60)
    if failures:
        print(f"❌ {len(failures)} 条失败")
        for f in failures:
            print("   -", f)
        return 1
    print("✅ 全部通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
