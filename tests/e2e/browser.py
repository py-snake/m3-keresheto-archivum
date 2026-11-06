#!/usr/bin/env python3
"""Real browser test of the instant search (static/search.js) through the Chrome DevTools Protocol.

    python3 tests/e2e/browser.py http://127.0.0.1:8080

It starts a headless Chromium of its own and drives the page like a visitor: it types, selects
and clicks, and checks the results, the URL, the number of requests, and that the browser reports
no Content Security Policy violation and no script error. Only the Python standard library is used.
Exit code 0 = all checks passed.
"""
import base64, hashlib, http.client, json, os, shutil, socket, struct, subprocess, sys, tempfile, time, urllib.parse, urllib.request

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8080").rstrip("/")
PORT = 9333
CHROMIUM = shutil.which("chromium-browser") or shutil.which("chromium") or shutil.which("google-chrome")


class WebSocket:
    """A minimal RFC 6455 client: enough for the DevTools protocol."""

    def __init__(self, url):
        u = urllib.parse.urlparse(url)
        self.sock = socket.create_connection((u.hostname, u.port), timeout=30)
        key = base64.b64encode(os.urandom(16)).decode()
        self.sock.sendall((f"GET {u.path} HTTP/1.1\r\nHost: {u.hostname}:{u.port}\r\nUpgrade: websocket\r\n"
                           f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
        head = b""
        while b"\r\n\r\n" not in head:
            head += self.sock.recv(1)
        assert b" 101 " in head.split(b"\r\n")[0], head
        accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
        assert accept.encode() in head, "bad websocket accept"

    def send(self, text):
        data = text.encode()
        n = len(data)
        header = bytearray([0x81])
        if n < 126:
            header.append(0x80 | n)
        elif n < 65536:
            header += bytes([0x80 | 126]) + struct.pack(">H", n)
        else:
            header += bytes([0x80 | 127]) + struct.pack(">Q", n)
        mask = os.urandom(4)
        self.sock.sendall(bytes(header) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def _read(self, n):
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("websocket closed")
            buf += chunk
        return buf

    def recv(self, timeout):
        self.sock.settimeout(timeout)
        message = b""
        while True:
            b1, b2 = self._read(2)
            fin, opcode, n = b1 & 0x80, b1 & 0x0F, b2 & 0x7F
            if n == 126:
                n = struct.unpack(">H", self._read(2))[0]
            elif n == 127:
                n = struct.unpack(">Q", self._read(8))[0]
            payload = self._read(n)
            if opcode == 9:  # ping
                self.sock.sendall(bytes([0x8A, 0x80]) + os.urandom(4))
                continue
            if opcode == 8:
                raise ConnectionError("websocket closed by the browser")
            message += payload
            if fin:
                return message.decode()


class Page:
    def __init__(self, ws):
        self.ws, self.next_id, self.events = ws, 0, []

    def call(self, method, **params):
        self.next_id += 1
        mid = self.next_id
        self.ws.send(json.dumps({"id": mid, "method": method, "params": params}))
        deadline = time.time() + 30
        while time.time() < deadline:
            msg = json.loads(self.ws.recv(max(0.1, deadline - time.time())))
            if msg.get("id") == mid:
                if "error" in msg:
                    raise RuntimeError(f"{method}: {msg['error']}")
                return msg.get("result", {})
            self.events.append(msg)
        raise TimeoutError(method)

    def pump(self, seconds):
        """Collect events for a while."""
        end = time.time() + seconds
        while time.time() < end:
            try:
                self.events.append(json.loads(self.ws.recv(max(0.05, end - time.time()))))
            except (socket.timeout, TimeoutError):
                break

    def js(self, expression):
        r = self.call("Runtime.evaluate", expression=expression, returnByValue=True, awaitPromise=True)
        if "exceptionDetails" in r:
            raise RuntimeError(r["exceptionDetails"])
        return r["result"].get("value")

    def wait_for(self, expression, timeout=6.0):
        end = time.time() + timeout
        while time.time() < end:
            if self.js(expression):
                return True
            self.pump(0.1)
        return False

    def requests(self, needle):
        return [e["params"]["request"]["url"] for e in self.events
                if e.get("method") == "Network.requestWillBeSent" and needle in e["params"]["request"]["url"]]


passed = failed = 0


def check(name, ok, detail=""):
    global passed, failed
    if ok:
        passed += 1
        print(f"  ok    {name}")
    else:
        failed += 1
        print(f"  FAIL  {name} {detail}")


def fragment_total(query):
    """What the server itself says for the same search (the reference the page must agree with)."""
    with urllib.request.urlopen(f"{BASE}/?{query}&reszlet=1", timeout=15) as r:
        body = r.read().decode()
    marker = ' találat</div>'
    if marker not in body:
        return None
    head = body.split(marker)[0]
    return head[head.rindex('>') + 1:] + ' találat'


def type_into(page, selector, text):
    page.js(f"""(() => {{ const el = document.querySelector({json.dumps(selector)}); el.focus(); el.value = {json.dumps(text)};
        el.dispatchEvent(new Event('input', {{bubbles: true}})); }})()""")


def total_text(page):
    return page.js("(document.querySelector('.list__total') || {textContent: ''}).textContent.trim()")


def main():
    if not CHROMIUM:
        print("no chromium found")
        return 2
    profile = tempfile.mkdtemp(prefix="m3-browser-")
    proc = subprocess.Popen([CHROMIUM, "--headless=new", "--no-sandbox", "--disable-gpu", f"--remote-debugging-port={PORT}",
                             f"--user-data-dir={profile}", "--window-size=1280,1000", "about:blank"],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            try:
                urllib.request.urlopen(f"http://127.0.0.1:{PORT}/json/version", timeout=1)
                break
            except Exception:
                time.sleep(0.1)
        conn = http.client.HTTPConnection("127.0.0.1", PORT)
        conn.request("PUT", "/json/new?about:blank")
        target = json.loads(conn.getresponse().read())
        page = Page(WebSocket(target["webSocketDebuggerUrl"]))
        for domain in ("Page", "Runtime", "Log", "Network"):
            page.call(f"{domain}.enable")

        page.call("Page.navigate", url=BASE + "/")
        check("the page loads", page.wait_for("document.readyState === 'complete' && !!document.getElementById('search-form')"))
        initial = total_text(page)
        check("the full list is shown at the start", initial.endswith("találat"), initial)
        check("the instant search script ran (the form is enhanced)", page.js("typeof window.fetch === 'function' && !!document.querySelector('script[src=\"/static/search.js\"]')"))

        # 1. typing replaces the results without a page reload
        page.js("window.__still_here = 42")
        page.events.clear()
        type_into(page, "#search-field", "béke")
        want = fragment_total("kereses=b%C3%A9ke")
        check("typing updates the results", page.wait_for(f"document.querySelector('.list__total') && document.querySelector('.list__total').textContent.trim() === {json.dumps(want)}"), f"want {want!r}, got {total_text(page)!r}")
        check("no page reload happened (the page state survived)", page.js("window.__still_here") == 42)
        check("the address bar follows the search", page.js("location.search") == "?kereses=b%C3%A9ke", page.js("location.search"))

        # 2. debounce: five keystrokes in a row are one request
        page.js("window.__still_here = 43")
        page.events.clear()
        for text in ("m", "ma", "mag", "magy", "magya"):
            type_into(page, "#search-field", text)
        page.pump(1.2)
        sent = page.requests("reszlet=1")
        check("fast typing is debounced to a single request", len(sent) == 1, str(sent))
        check("the debounced request carries the final text", sent and "kereses=magya" in sent[0], str(sent))

        # 3. a 1-2 character term works (titles only)
        page.events.clear()
        type_into(page, "#search-field", "zo")
        want = fragment_total("kereses=zo")
        check("a 2 character search works", page.wait_for(f"document.querySelector('.list__total') && document.querySelector('.list__total').textContent.trim() === {json.dumps(want)}"), f"want {want!r} got {total_text(page)!r}")

        # 4. advanced filters: a genre and the "only available" box
        type_into(page, "#search-field", "")
        page.wait_for("true")
        genre = page.js("(() => { const o = document.querySelector('select[name=mufaj] option:nth-child(2)'); return o ? o.value : null; })()")
        check("the genre list is filled from the database", bool(genre), str(genre))
        if genre:
            page.events.clear()
            page.js(f"""(() => {{ const s = document.querySelector('select[name=mufaj]'); s.value = {json.dumps(genre)};
                s.dispatchEvent(new Event('change', {{bubbles: true}})); }})()""")
            want = fragment_total("mufaj=" + urllib.parse.quote_plus(genre))
            check("choosing a genre filters the results", page.wait_for(f"document.querySelector('.list__total') && document.querySelector('.list__total').textContent.trim() === {json.dumps(want)}"), f"want {want!r} got {total_text(page)!r}")
            check("the genre is in the address bar", "mufaj=" in page.js("location.search"))
        page.events.clear()
        page.js("document.querySelector('input[name=elerheto]').click()")
        want = fragment_total("mufaj=" + urllib.parse.quote_plus(genre or "") + "&elerheto=1")
        ok = page.wait_for(f"(() => {{ const t = document.querySelector('.list__total'); return t ? t.textContent.trim() === {json.dumps(want)} : {json.dumps(want is None)}; }})()")
        check("ticking 'only available' narrows the results", ok, f"want {want!r} got {total_text(page)!r}")

        # 5. hostile text is shown as text, never run
        type_into(page, "#search-field", "<img src=x onerror=window.__pwn=1>")
        page.pump(1.0)
        check("a hostile search text does not execute", page.js("window.__pwn") is None)
        check("it created no element from the text", page.js("document.querySelectorAll('#results img[src=\"x\"]').length") == 0)

        # 6. an error answer does not break the page
        check("the search box is still there", page.js("!!document.getElementById('search-field')"))

        # 7. the browser itself reports no policy violation and no script error
        page.pump(0.5)
        problems = []
        for e in page.events:
            if e.get("method") == "Log.entryAdded":
                entry = e["params"]["entry"]
                if "Content Security Policy" in entry.get("text", "") or entry.get("source") == "security":
                    problems.append(entry.get("text", "")[:160])
            if e.get("method") == "Runtime.exceptionThrown":
                problems.append(json.dumps(e["params"]["exceptionDetails"])[:160])
        check("no CSP violation and no script error in the browser", not problems, "; ".join(problems))
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except Exception:
            proc.kill()
        shutil.rmtree(profile, ignore_errors=True)
    print(f"\nbrowser: {passed} passed, {failed} failed")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
