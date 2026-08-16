#!/usr/bin/env python3
"""
LAN Drop - Lightweight Python Development Runner (DEPRECATED)
Implements a subset of the API to preview the Web UI without Go installed.

NOTE: This dev-only preview server no longer tracks the Go implementation
(session auth, rate limiting, upload resume, feed persistence are missing).
It will be removed in a future release — build the single Go binary instead:
    go build -ldflags="-s -w" -o landrop .
"""
import os
import sys

print("!! [DEPRECATED] run_dev_server.py is a dev-only preview and lags behind the Go server.")
print("!! Prefer building the real binary:  go build -ldflags=\"-s -w\" -o landrop .")

import json
import time
import socket
import shutil
import cgi
from http.server import HTTPServer, SimpleHTTPRequestHandler
from urllib.parse import parse_qs, urlparse

HOST = "0.0.0.0"
PORT = 8087
BASE_DIR = os.path.dirname(os.path.abspath(__file__))
WEB_DIR = os.path.join(BASE_DIR, "web")
UPLOAD_DIR = os.path.join(BASE_DIR, "LAN_Drop_Files")
os.makedirs(UPLOAD_DIR, exist_ok=True)

# Shared in-memory text feed
TEXT_FEED = []

def get_local_ip():
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
        return ip
    except Exception:
        return "127.0.0.1"

class LANDropHandler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=WEB_DIR, **kwargs)

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path

        if path == "/api/info":
            self.send_json({
                "hostname": socket.gethostname(),
                "host_ip": get_local_ip(),
                "port": PORT,
                "upload_dir": UPLOAD_DIR
            })
            return

        if path == "/api/files":
            files = []
            for name in os.listdir(UPLOAD_DIR):
                if name.startswith("."):
                    continue
                p = os.path.join(UPLOAD_DIR, name)
                if os.path.isfile(p):
                    stat = os.stat(p)
                    files.append({
                        "name": name,
                        "size": stat.st_size,
                        "mod_time": time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime(stat.st_mtime)),
                        "type": "application/octet-stream",
                        "url": f"/api/download/{name}"
                    })
            files.sort(key=lambda x: x["mod_time"], reverse=True)
            self.send_json({"status": "ok", "files": files})
            return

        if path.startswith("/api/download/"):
            filename = path[len("/api/download/"):]
            filepath = os.path.join(UPLOAD_DIR, filename)
            if not os.path.exists(filepath):
                self.send_error(404, "File not found")
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Disposition", f'attachment; filename="{filename}"')
            self.send_header("Content-Length", str(os.path.getsize(filepath)))
            self.end_headers()
            with open(filepath, "rb") as f:
                shutil.copyfileobj(f, self.wfile)
            return

        if path == "/api/text/feed":
            self.send_json({"status": "ok", "feed": TEXT_FEED})
            return

        # Fallback to static web assets
        super().do_GET()

    def do_POST(self):
        parsed = urlparse(self.path)
        path = parsed.path

        if path == "/api/upload/chunk":
            form = cgi.FieldStorage(
                fp=self.rfile,
                headers=self.headers,
                environ={'REQUEST_METHOD': 'POST', 'CONTENT_TYPE': self.headers['Content-Type']}
            )
            file_id = form.getvalue("file_id")
            chunk_index = int(form.getvalue("chunk_index", 0))
            chunk_data = form["chunk"].file.read()

            temp_dir = os.path.join(UPLOAD_DIR, f".tmp_{file_id}")
            os.makedirs(temp_dir, exist_ok=True)
            chunk_file = os.path.join(temp_dir, f"chunk_{chunk_index:05d}")
            with open(chunk_file, "wb") as f:
                f.write(chunk_data)

            self.send_json({"status": "ok", "chunk_index": chunk_index})
            return

        if path == "/api/upload/complete":
            content_len = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(content_len))
            file_id = body["file_id"]
            filename = os.path.basename(body["filename"])
            total_chunks = body["total_chunks"]

            temp_dir = os.path.join(UPLOAD_DIR, f".tmp_{file_id}")
            dest_path = os.path.join(UPLOAD_DIR, filename)

            # Atomic assembly
            with open(dest_path, "wb") as dest:
                for i in range(total_chunks):
                    chunk_path = os.path.join(temp_dir, f"chunk_{i:05d}")
                    if os.path.exists(chunk_path):
                        with open(chunk_path, "rb") as chunk_f:
                            shutil.copyfileobj(chunk_f, dest)
            shutil.rmtree(temp_dir, ignore_errors=True)

            self.send_json({"status": "success", "filename": filename})
            return

        if path == "/api/text/send":
            content_len = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(content_len))
            item = {
                "id": str(time.time()),
                "content": body.get("content", ""),
                "sender": body.get("sender", "Device"),
                "timestamp": time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
            }
            TEXT_FEED.insert(0, item)
            self.send_json({"status": "ok", "data": item})
            return

        if path == "/api/files/delete":
            content_len = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(content_len))
            filename = os.path.basename(body.get("filename", ""))
            filepath = os.path.join(UPLOAD_DIR, filename)
            if os.path.exists(filepath):
                os.remove(filepath)
            self.send_json({"status": "ok"})
            return

        self.send_error(404)

    def send_json(self, data):
        payload = json.dumps(data).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

def main():
    ip = get_local_ip()
    print("=" * 65)
    print("   ⚡ LAN Drop (Python Dev Runner) 正在运行")
    print("=" * 65)
    print(f" 🌐 本地访问地址 : http://{ip}:{PORT}")
    print(f" 📂 文件保存路径 : {UPLOAD_DIR}")
    print("=" * 65)
    server = HTTPServer((HOST, PORT), LANDropHandler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n正在停止服务...")
        server.server_close()

if __name__ == "__main__":
    main()
