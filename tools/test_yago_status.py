import json
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parent))
import yago_status


class StatusHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path == "/api/admin/v1/auth/login":
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            if body != {"username": "operator", "password": "secret-value"}:
                self.send_error(401)
                return
            self.send_response(200)
            self.send_header("Set-Cookie", "yago_admin_session=session-value; Path=/")
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"username":"operator"}')
            return
        self.send_response(204)
        self.end_headers()

    def do_GET(self):
        if self.path != "/admin/index/rwi-backfill" or "session-value" not in self.headers.get("Cookie", ""):
            self.send_error(401)
            return
        body = json.dumps({
            "state": "running",
            "scannedDocuments": 120,
            "indexedDocuments": 100,
            "postingRows": 900,
            "lastError": "https://private.example/path",
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format, *_args):
        return


class YagoStatusTests(unittest.TestCase):
    def test_environment_map_keeps_values_internal(self):
        inspect = {"Config": {"Env": ["YAGO_ADMIN_PASSWORD=secret", "PATH=/usr/bin", "invalid"]}}
        self.assertEqual(
            yago_status.environment_map(inspect),
            {"YAGO_ADMIN_PASSWORD": "secret", "PATH": "/usr/bin"},
        )

    def test_operations_urls_ignore_missing_ipv6_addresses(self):
        inspect = {"NetworkSettings": {"Networks": {
            "v6": {"GlobalIPv6Address": "eb22::1"},
            "v4": {"GlobalIPv6Address": "invalid IP"},
            "none": {},
        }}}
        self.assertEqual(
            yago_status.ipv6_operations_urls(inspect, 9090),
            ["http://[eb22::1]:9090"],
        )

    def test_backfill_summary_does_not_expose_error_payload(self):
        status = yago_status.safe_backfill_status({
            "state": "failed",
            "indexedDocuments": 4,
            "lastError": "https://private.example/path?token=secret",
        })
        self.assertEqual(status["state"], "failed")
        self.assertEqual(status["indexedDocuments"], 4)
        self.assertTrue(status["lastErrorPresent"])
        self.assertNotIn("lastError", status)
        self.assertNotIn("private.example", json.dumps(status))

    def test_admin_status_uses_credentials_without_returning_them(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), StatusHandler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        try:
            base_url = f"http://127.0.0.1:{server.server_port}"
            status = yago_status.admin_backfill_status(base_url, {
                "YAGO_ADMIN_USER": "operator",
                "YAGO_ADMIN_PASSWORD": "secret-value",
            })
        finally:
            server.shutdown()
            worker.join(timeout=2)
            server.server_close()
        self.assertTrue(status["available"])
        self.assertEqual(status["state"], "running")
        self.assertTrue(status["lastErrorPresent"])
        encoded = json.dumps(status)
        self.assertNotIn("secret-value", encoded)
        self.assertNotIn("session-value", encoded)
        self.assertNotIn("private.example", encoded)


if __name__ == "__main__":
    unittest.main()
