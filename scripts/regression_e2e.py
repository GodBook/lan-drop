#!/usr/bin/env python3
"""Full LAN Drop regression: auth/rate-limit, connection QR, WS (including
hostile frames), resumable upload, special filenames, and restart persistence."""
import base64, json, os, shutil, socket, struct, subprocess, sys, time
import urllib.request, urllib.error

HOST, PORT, PIN = "127.0.0.1", 18999, "5150"
BASE = f"http://{HOST}:{PORT}"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = os.path.join(ROOT, "dist", "landrop-windows-amd64.exe")
DATA = os.path.join(ROOT, "dist", "_reg_files")

fails = []
def check(name, ok, detail=""):
    print(f"  [{'OK' if ok else 'FAIL'}] {name}" + (f" -- {detail}" if detail and not ok else ""))
    if not ok: fails.append(name)

def req(method, path, data=None, headers=None, raw=False):
    r = urllib.request.Request(BASE + path, data=data, headers=headers or {}, method=method)
    try:
        with urllib.request.urlopen(r, timeout=5) as res:
            body = res.read()
            if raw or "json" not in res.headers.get("Content-Type", ""):
                return res.status, body, res.headers
            return res.status, (json.loads(body) if body else None), res.headers
    except urllib.error.HTTPError as e:
        body = e.read()
        try: parsed = json.loads(body)
        except Exception: parsed = body
        return e.code, parsed, e.headers

class WS:
    def __init__(self, path, origin=None):
        self.s = socket.create_connection((HOST, PORT), timeout=5)
        key = base64.b64encode(os.urandom(16)).decode()
        hdr = (f"GET {path} HTTP/1.1\r\nHost: {HOST}:{PORT}\r\nUpgrade: websocket\r\n"
               f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n")
        if origin: hdr += f"Origin: {origin}\r\n"
        self.s.sendall((hdr + "\r\n").encode())
        buf = b""
        while b"\r\n\r\n" not in buf: buf += self.s.recv(4096)
        self.status = int(buf.split(b" ", 2)[1])
        self.buf = buf.split(b"\r\n\r\n", 1)[1]
    def _exact(self, n):
        while len(self.buf) < n: self.buf += self.s.recv(4096)
        out, self.buf = self.buf[:n], self.buf[n:]
        return out
    def read_msg(self):
        h = self._exact(2); ln = h[1] & 0x7F
        if ln == 126: ln = struct.unpack(">H", self._exact(2))[0]
        elif ln == 127: ln = struct.unpack(">Q", self._exact(8))[0]
        return json.loads(self._exact(ln))
    def send_text(self, obj):
        p = json.dumps(obj).encode(); mask = os.urandom(4)
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(p))
        h = bytes([0x81]); n = len(p)
        h += bytes([0x80 | n]) if n <= 125 else bytes([0x80 | 126]) + struct.pack(">H", n)
        self.s.sendall(h + mask + masked)
    def send_raw(self, b): self.s.sendall(b)

proc = None
def start(fresh=True):
    global proc
    subprocess.run(["taskkill", "/IM", "landrop-windows-amd64.exe", "/F"],
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(0.5)
    if fresh:
        shutil.rmtree(DATA, ignore_errors=True)
    proc = subprocess.Popen([EXE, "-p", str(PORT), "-pin", PIN, "-d", DATA, "-no-browser"],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(40):
        try:
            req("GET", "/api/info"); return
        except Exception: time.sleep(0.25)
    raise RuntimeError("server did not start")

def stop():
    proc.terminate()
    try: proc.wait(timeout=10)
    except Exception: proc.kill()

print("== [1] auth: session + rate limit ==")
start()
st, _, hdrs = req("GET", f"/?pin={PIN}")
check("page 200", st == 200)
cookie = hdrs.get("Set-Cookie", "")
check("session cookie minted via ?pin=", "landrop_session=" in cookie, cookie)

st, qr_svg, qr_headers = req("GET", f"/api/qr?pin={PIN}", raw=True)
check("connection QR SVG", st == 200 and b"<svg" in qr_svg and
      "image/svg+xml" in qr_headers.get("Content-Type", ""))
st, qr_details, _ = req("GET", f"/api/qr?format=json&pin={PIN}")
check("connection QR details", st == 200 and qr_details.get("pin") == PIN and
      qr_details.get("url", "").endswith(f":{PORT}/?pin={PIN}"), str(qr_details))

st, _, _ = req("POST", "/api/auth", json.dumps({"pin": "0000"}).encode(), {"Content-Type": "application/json"})
check("wrong pin 401", st == 401)
st, _, _ = req("POST", "/api/auth", json.dumps({"pin": PIN}).encode(), {"Content-Type": "application/json"})
check("correct pin 200", st == 200)
for _ in range(5):
    req("POST", "/api/auth", json.dumps({"pin": "9999"}).encode(), {"Content-Type": "application/json"})
st, body, _ = req("POST", "/api/auth", json.dumps({"pin": PIN}).encode(), {"Content-Type": "application/json"})
check("locked out after 5 fails (429)", st == 429, f"got {st} {body}")

print("== [2] websocket: connect / broadcast / hostile frames ==")
phone = WS(f"/api/ws?pin={PIN}")
check("handshake 101", phone.status == 101)
init = phone.read_msg()
check("init_feed received", init.get("type") == "init_feed")
pc = WS(f"/api/ws?pin={PIN}", origin=f"http://{HOST}:{PORT}")
check("same-origin accepted", pc.status == 101)
evil = WS(f"/api/ws?pin={PIN}", origin="http://evil.example.com")
check("foreign origin rejected 403", evil.status == 403)
pc.read_msg()

phone.send_text({"type": "send_text", "content": "persistence-check-42", "sender": "phone"})
got = pc.read_msg()
check("cross-device broadcast", got.get("type") == "new_text" and got["data"]["content"] == "persistence-check-42")
phone.read_msg()

hostile = WS(f"/api/ws?pin={PIN}")
hostile.read_msg()
hostile.send_raw(bytes([0x81, 0x7F]) + struct.pack(">Q", 2**63) + b"junk")
time.sleep(0.5)
st, _, _ = req("GET", "/api/info")
check("server survives hostile frame", st == 200)

print("== [3] upload: resume + special filename + merge rollback ==")
B = "----rb"
FNAME = "\u62a5\u544a #final v2.pdf"  # 报告 #final v2.pdf
def chunk(fid, idx, total, fname, payload):
    body = (f"--{B}\r\nContent-Disposition: form-data; name=\"file_id\"\r\n\r\n{fid}\r\n"
            f"--{B}\r\nContent-Disposition: form-data; name=\"chunk_index\"\r\n\r\n{idx}\r\n"
            f"--{B}\r\nContent-Disposition: form-data; name=\"total_chunks\"\r\n\r\n{total}\r\n"
            f"--{B}\r\nContent-Disposition: form-data; name=\"filename\"\r\n\r\n{fname}\r\n"
            f"--{B}\r\nContent-Disposition: form-data; name=\"chunk\"; filename=\"b\"\r\n"
            f"Content-Type: application/octet-stream\r\n\r\n").encode("utf-8") + payload + f"\r\n--{B}--\r\n".encode()
    return req("POST", f"/api/upload/chunk?pin={PIN}", body,
               {"Content-Type": f"multipart/form-data; boundary={B}"})

partA = b"A" * 700
st, _, _ = chunk("resume123", 0, 2, FNAME, partA)
check("chunk 0 uploaded", st == 200)
st, body, _ = req("GET", "/api/upload/status?file_id=resume123&pin=" + PIN)
check("status shows [0]", body.get("chunks") == [0], str(body))

st, _, _ = req("POST", "/api/upload/complete?pin=" + PIN,
               json.dumps({"file_id": "resume123", "filename": FNAME, "total_chunks": 2}).encode("utf-8"),
               {"Content-Type": "application/json"})
check("complete w/ missing chunk rejected", st == 400)
check("no partial file left", not any(f.startswith("\u62a5\u544a") for f in os.listdir(DATA)), str(os.listdir(DATA)))

partB = b"B" * 300
st, _, _ = chunk("resume123", 1, 2, FNAME, partB)
st, body, _ = req("POST", "/api/upload/complete?pin=" + PIN,
                  json.dumps({"file_id": "resume123", "filename": FNAME, "total_chunks": 2}).encode("utf-8"),
                  {"Content-Type": "application/json"})
check("merge success", st == 200 and body["file"]["name"] == FNAME)
url = body["file"]["url"]
check("url escaped", "%20" in url, url)
st, content, hdrs = req("GET", url + "?pin=" + PIN, raw=True)
check("download roundtrip", st == 200 and content == partA + partB)
check("RFC5987 disposition", "UTF-8''" in hdrs.get("Content-Disposition", ""), hdrs.get("Content-Disposition"))
st, body, _ = req("GET", "/api/files?pin=" + PIN)
check("file listed once", len([f for f in body["files"] if f["name"] == FNAME]) == 1)

print("== [4] tmp cleanup + feed persistence across restart ==")
st, body, _ = req("GET", "/api/text/feed?pin=" + PIN)
check("feed has persisted text", any("persistence-check-42" in m["content"] for m in body["feed"]))
stop()

start(fresh=False)
time.sleep(1.5)
st, body, _ = req("GET", f"/api/text/feed?pin={PIN}")
check("feed survives restart", any("persistence-check-42" in m["content"] for m in body["feed"]))
st, body, _ = req("GET", "/api/files?pin=" + PIN)
check("files survive restart", len([f for f in body["files"] if f["name"] == FNAME]) == 1)

print("== [5] static ETag caching ==")
st, _, h1 = req("GET", "/app.js")
etag = h1.get("ETag", "")
check("etag present", bool(etag))
r = urllib.request.Request(BASE + "/app.js", headers={"If-None-Match": etag})
try:
    urllib.request.urlopen(r, timeout=5); check("304 revalidation", False)
except urllib.error.HTTPError as e:
    check("304 revalidation", e.code == 304)

stop()
shutil.rmtree(DATA, ignore_errors=True)

print()
if fails:
    print(f"RESULT: {len(fails)} FAILED: {fails}"); sys.exit(1)
print("RESULT: ALL REGRESSION CHECKS PASSED")
