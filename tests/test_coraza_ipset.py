"""Check the coraza-ipset plugin in a temporary local Caddy container.

The plugin replaces Coraza's @ipMatchFromFile with an equivalent lookup, so
requests alone behave the same with or without it: the build info proves
that the plugin is linked, the requests prove that it matches like Coraza
inside a real Caddy.
"""
import ipaddress
import os
import random
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

IMAGE = os.environ.get('CADDY_TEST_IMAGE', 'caddy-with-auth:latest')
if __name__ == '__main__' and len(sys.argv) > 1 and not sys.argv[1].startswith('-'):
    IMAGE = sys.argv.pop(1)

PLUGIN = 'github.com/liu-kan/caddy-with-auth/plugins/coraza-ipset'

# Requests reach the container through the Docker bridge, a private
# address, so X-Forwarded-For sets the client address Coraza sees.
CADDYFILE = '''{
    admin off
    auto_https off
    order coraza_waf first
    servers {
        trusted_proxies static private_ranges
        client_ip_headers X-Forwarded-For
    }
}
:8080 {
    coraza_waf {
        directives `
            SecRuleEngine On
            SecRule REMOTE_ADDR "@ipMatchFromFile /etc/caddy/ipset.txt" "id:1001,phase:1,deny,status:403"
            SecRule REMOTE_ADDR "!@ipMatchF /etc/caddy/ipset.txt" "id:1002,phase:1,pass,nolog,setvar:tx.outside=1"
            SecRule TX:OUTSIDE "@eq 1" "id:1003,phase:1,deny,status:401"
        `
    }
    respond "ok" 200
}
'''

LIST = '''# documentation and test ranges
203.0.113.0/24
198.51.100.7
2001:db8::/32
::ffff:192.0.2.0/120
not-an-address
'''


def network(index):
    """Return the index-th IPv4 /24 network of 10.0.0.0/8."""
    return ipaddress.ip_network(f'10.{index >> 8}.{index & 255}.0/24')


class CorazaIPSetTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix='coraza-ipset-tests-')
        cls.addClassCleanup(cls.directory.cleanup)
        directory = Path(cls.directory.name)
        cls.indexes = sorted(random.Random(42).sample(range(1 << 16), 20000))
        config, ipset = directory / 'Caddyfile', directory / 'ipset.txt'
        config.write_text(CADDYFILE)
        ipset.write_text(LIST + ''.join(f'{network(index)}\n' for index in cls.indexes))
        for path in (config, ipset):
            path.chmod(0o644)  # the runtime user is 65532
        cls.container = subprocess.check_output([
            'docker', 'run', '--detach', '--rm',
            '--publish', '127.0.0.1::8080',
            '--mount', f'type=bind,src={config},dst=/etc/caddy/Caddyfile,readonly',
            '--mount', f'type=bind,src={ipset},dst=/etc/caddy/ipset.txt,readonly',
            '--entrypoint', '/usr/local/bin/caddy', IMAGE,
            'run', '--config', '/etc/caddy/Caddyfile', '--adapter', 'caddyfile',
        ], text=True).strip()
        cls.addClassCleanup(lambda: subprocess.run(
            ['docker', 'rm', '--force', cls.container],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False,
        ))
        address = subprocess.check_output(
            ['docker', 'port', cls.container, '8080/tcp'], text=True,
        ).strip()
        cls.port = int(address.rsplit(':', 1)[1])
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                if cls.status('203.0.113.1') == 403:
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.1)
        logs = subprocess.run(['docker', 'logs', cls.container], capture_output=True, text=True, check=False)
        raise RuntimeError('Caddy did not become ready: ' + logs.stdout + logs.stderr)

    @classmethod
    def status(cls, client):
        request = (
            b'GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n'
            + f'X-Forwarded-For: {client}\r\n\r\n'.encode()
        )
        with socket.create_connection(('127.0.0.1', cls.port), timeout=5) as connection:
            connection.sendall(request)
            response = b''
            while b'\r\n' not in response:
                chunk = connection.recv(4096)
                if not chunk:
                    raise OSError('Server closed without an HTTP response')
                response += chunk
            return int(response.split(b'\r\n', 1)[0].split()[1])

    def test_plugin_is_linked(self):
        info = subprocess.check_output(
            ['docker', 'run', '--rm', '--entrypoint', '/usr/local/bin/caddy', IMAGE, 'build-info'], text=True,
        )
        self.assertTrue(PLUGIN in info, f'{IMAGE} does not contain {PLUGIN}')

    def test_list_members_are_denied(self):
        # The second rule (@ipMatchF, negated) never denies a member.
        for client in ('203.0.113.0', '203.0.113.255', '198.51.100.7', '2001:db8::5',
                       '2001:db8:ffff:ffff:ffff:ffff:ffff:ffff', '192.0.2.7', '::ffff:203.0.113.9'):
            with self.subTest(client=client):
                self.assertEqual(self.status(client), 403)

    def test_other_addresses_pass_the_first_rule(self):
        # 401 comes from the negated @ipMatchF rule: the client is outside.
        for client in ('203.0.114.0', '198.51.100.8', '2001:db9::1', '192.0.3.1', '8.8.8.8'):
            with self.subTest(client=client):
                self.assertEqual(self.status(client), 401)

    def test_large_list(self):
        picked = set(self.indexes)
        for index in (self.indexes[0], self.indexes[len(self.indexes) // 2], self.indexes[-1]):
            listed = network(index)
            with self.subTest(network=str(listed)):
                self.assertEqual(self.status(str(listed[0])), 403)
                self.assertEqual(self.status(str(listed[-1])), 403)
                # The next address starts the next /24, listed or not.
                self.assertEqual(self.status(str(listed[-1] + 1)), 403 if index + 1 in picked else 401)


if __name__ == '__main__':
    unittest.main(verbosity=2)
