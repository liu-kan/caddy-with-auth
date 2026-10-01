"""Replay the two Coraza CVEs against a temporary local Caddy container."""
from pathlib import Path
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest

IMAGE = os.environ.get('CADDY_TEST_IMAGE', 'caddy-with-auth:latest')
if __name__ == '__main__' and len(sys.argv) > 1 and not sys.argv[1].startswith('-'):
    IMAGE = sys.argv.pop(1)

CADDYFILE = '''{
    admin off
    auto_https off
    order coraza_waf first
}
:8080 {
    coraza_waf {
        directives `
            SecRuleEngine On
            SecRequestBodyAccess On
            SecRequestBodyLimit 4096
            SecRule REQUEST_FILENAME "@rx /bar/uploads/.*" "id:1001,phase:1,deny,status:403"
            SecRule REQBODY_ERROR "@eq 1" "id:1002,phase:2,deny,status:403"
        `
    }
    respond "ok" 200
}
'''


class CorazaCVERegressionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix='coraza-cve-tests-')
        cls.addClassCleanup(cls.directory.cleanup)
        config = Path(cls.directory.name) / 'Caddyfile'
        config.write_text(CADDYFILE)
        cls.container = subprocess.check_output([
            'docker', 'run', '--detach', '--rm',
            '--publish', '127.0.0.1::8080',
            '--mount', f'type=bind,src={config},dst=/etc/caddy/Caddyfile,readonly',
            '--entrypoint', '/usr/local/bin/caddy', IMAGE,
            'run', '--config', '/etc/caddy/Caddyfile', '--adapter', 'caddyfile',
        ], text=True).strip()
        cls.addClassCleanup(lambda: subprocess.run(
            ['docker', 'rm', '--force', cls.container],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        ))
        address = subprocess.check_output(
            ['docker', 'port', cls.container, '8080/tcp'], text=True,
        ).strip()
        cls.port = int(address.rsplit(':', 1)[1])
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                if cls.request(b'GET /health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n') == 200:
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.1)
        logs = subprocess.run(['docker', 'logs', cls.container], capture_output=True, text=True)
        raise RuntimeError('Caddy did not become healthy: ' + logs.stdout + logs.stderr)

    @classmethod
    def request(cls, request):
        with socket.create_connection(('127.0.0.1', cls.port), timeout=5) as connection:
            connection.sendall(request)
            response = b''
            while b'\r\n' not in response:
                chunk = connection.recv(4096)
                if not chunk:
                    raise OSError('Server closed without an HTTP response')
                response += chunk
            return int(response.split(b'\r\n', 1)[0].split()[1])

    def test_double_slash_uri_cannot_bypass_filename_rule(self):
        # Preserve the literal // request target, without a client's normalization.
        request = b'GET //bar/uploads/foo.php?a=b HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n'
        self.assertEqual(self.request(request), 403)

    def test_duplicate_multipart_parameter_does_not_crash_server(self):
        body = b'--b\r\nContent-Disposition: form-data; name="file"; filename="test"\r\n\r\ntest\r\n--b--\r\n'
        request = (
            b'POST /upload HTTP/1.1\r\nHost: localhost\r\n'
            b'Content-Type: multipart/form-data; boundary=b; a=1; a=2\r\n'
            + f'Content-Length: {len(body)}\r\n'.encode()
            + b'Connection: close\r\n\r\n' + body
        )
        self.request(request)
        self.assertEqual(self.request(b'GET /health HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n'), 200)


if __name__ == '__main__':
    unittest.main(verbosity=2)
