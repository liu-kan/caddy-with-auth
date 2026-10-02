"""Offline regressions for dual-stack Caddy logs and Cloudflare range refreshes.

Uses jq when installed, and Fail2ban when installed or when FAIL2BAN_SOURCE points
to an upstream checkout. No external HTTP requests or firewall changes are made.
"""
from pathlib import Path
import configparser
import ipaddress
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
EXAMPLE = ROOT / 'examples/cloudflare-real-ip-fail2ban'
if os.environ.get('FAIL2BAN_SOURCE'):
    sys.path.insert(0, os.environ['FAIL2BAN_SOURCE'])
try:
    from fail2ban.server.filter import Filter
    from fail2ban.server.ipdns import DNSUtils
except ImportError:
    Filter = None


def log_record(client_ip, *, remote_ip='104.23.166.76', uri='/api/auth/login',
               status=401, method='POST', logger='http.log.access.login'):
    return json.dumps({
        'level': 'info', 'ts': 1790948415.6081429, 'logger': logger,
        'msg': 'handled request',
        'request': {
            'remote_ip': remote_ip, 'remote_port': '11249',
            'client_ip': client_ip, 'proto': 'HTTP/2.0', 'method': method,
            'host': 'chat.example.com', 'uri': uri, 'headers': {},
        },
        'status': status,
    }, separators=(',', ':'))


class RangeRefreshTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix='caddy-ranges-test-')
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name)
        self.output = self.path / 'cloudflare-ranges.caddy'
        self.original = 'static 203.0.113.0/24\n'
        self.output.write_text(self.original)
        fake_bin = self.path / 'bin'
        fake_bin.mkdir()
        fake_curl = fake_bin / 'curl'
        fake_curl.write_text('''#!/usr/bin/env python3
import os
import sys
family = sys.argv[-1].rsplit('-', 1)[-1].upper()
if os.environ.get('CF_TEST_FAIL_FAMILY') == family:
    sys.exit(22)
sys.stdout.write(os.environ['CF_TEST_LIST_' + family])
''')
        fake_curl.chmod(0o755)
        self.environment = dict(os.environ,
            PATH=str(fake_bin) + os.pathsep + os.environ['PATH'],
            CF_TEST_LIST_V4=''.join(f'198.51.100.{i}/32\n' for i in range(1, 16)),
            CF_TEST_LIST_V6='2001:db8::/32\n',
        )

    def run_refresh(self, **overrides):
        return subprocess.run(
            ['/bin/sh', str(EXAMPLE / 'gen-cloudflare-ranges.sh'), str(self.output)],
            env=dict(self.environment, **overrides), capture_output=True, text=True,
            timeout=10,
        )

    def assert_preserved(self, result):
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.output.read_text(), self.original)
        self.assertFalse(Path(str(self.output) + '.tmp').exists())

    def test_both_families_survive_refresh_and_crlf_normalization(self):
        result = self.run_refresh(CF_TEST_LIST_V6='2001:db8::/32\r\n')
        self.assertEqual(result.returncode, 0, result.stderr)
        networks = [ipaddress.ip_network(value)
                    for value in self.output.read_text().split()[1:]]
        self.assertEqual(len(networks), 16)
        self.assertEqual({network.version for network in networks}, {4, 6})

    def test_download_failure_of_either_family_preserves_original(self):
        for family in ('V4', 'V6'):
            with self.subTest(family=family):
                self.assert_preserved(self.run_refresh(CF_TEST_FAIL_FAMILY=family))

    def test_responses_without_final_newlines_do_not_merge_cidrs(self):
        result = self.run_refresh(
            CF_TEST_LIST_V4=self.environment['CF_TEST_LIST_V4'].rstrip('\n'),
            CF_TEST_LIST_V6='2001:db8::/32',
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        networks = [ipaddress.ip_network(value)
                    for value in self.output.read_text().split()[1:]]
        self.assertEqual(len(networks), 16)
        self.assertEqual(networks[-1], ipaddress.ip_network('2001:db8::/32'))

    def test_missing_or_invalid_ipv6_preserves_original(self):
        for value in ('', '<html>error</html>\n', '198.51.100.1/32\n', 'abcd/32\n',
                      '2001:db8::/129\n'):
            with self.subTest(value=value):
                self.assert_preserved(self.run_refresh(CF_TEST_LIST_V6=value))

    def test_missing_or_invalid_ipv4_preserves_original(self):
        for value in ('', '2001:db8::/32\n', '198.51.100.1/33\n'):
            with self.subTest(value=value):
                self.assert_preserved(self.run_refresh(CF_TEST_LIST_V4=value))


@unittest.skipUnless(shutil.which('jq'), 'jq is not installed')
class LogInspectionTests(unittest.TestCase):
    def test_manual_command_preserves_ipv4_and_ipv6_strings(self):
        addresses = ['198.51.100.196', '2001:db8:45d:594f:1504:4543:f99e:890e',
                     '2001:db8::196']
        records = '\n'.join(log_record(ip, uri='/api/auth/refresh', status=200)
                            for ip in addresses)
        result = subprocess.run([
            'jq', '-c', '{status} + (.request | {remote_ip, client_ip, method, uri})',
        ], input=records, capture_output=True, text=True, check=True)
        output = [json.loads(line) for line in result.stdout.splitlines()]
        self.assertEqual([record['client_ip'] for record in output], addresses)
        self.assertTrue(all(set(record) ==
            {'status', 'remote_ip', 'client_ip', 'method', 'uri'} for record in output))


@unittest.skipUnless(Filter, 'Install Fail2ban or set FAIL2BAN_SOURCE')
class LoginFailureFilterTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        DNSUtils.setIPv6IsAllowed(True)
        config = configparser.ConfigParser(interpolation=None)
        config.read(EXAMPLE / 'fail2ban/filter.d/caddy-librechat.conf')
        cls.regex_text = config['Definition']['failregex']
        cls.date_pattern = config['Definition']['datepattern']

    def failures(self, record):
        # Exercise the real log-reader pipeline, including removal of the date
        # before regex matching. Historical fixtures must not expire by findtime.
        log_filter = Filter(None, useDns='no')
        log_filter.checkFindTime = False
        log_filter.setDatePattern(self.date_pattern)
        log_filter.addFailRegex(self.regex_text)
        return log_filter.processLine(record)

    def matches(self, record):
        failures = self.failures(record)
        return str(failures[0][1]) if failures else None

    def test_ipv4_full_ipv6_and_compressed_ipv6_failures(self):
        for ip in ('198.51.100.196', '2001:db8:45d:594f:1504:4543:f99e:890e',
                   '2001:db8::196'):
            for status in (401, 404, 429):
                with self.subTest(ip=ip, status=status):
                    self.assertEqual(self.matches(log_record(ip, status=status)), ip)

    def test_ipv6_cloudflare_peer_and_query_string(self):
        ip = '2001:db8::196'
        for status in (401, 404, 429):
            with self.subTest(status=status):
                self.assertEqual(self.matches(log_record(ip, status=status,
                    remote_ip='2606:4700::1111', uri='/api/auth/login?next=%2Fc')), ip)

    def test_pasted_log_distribution_counts_only_six_failed_logins(self):
        # Keep only the path/status distribution from the 23-line incident;
        # fixtures use documentation IPs and contain no real headers or cookies.
        requests = (
            [('/api/auth/refresh', 200)] * 13 +
            [('/api/auth/refresh', 401)] * 2 +
            [('/api/auth/login', 200), ('/api/auth/logout', 200)] +
            [('/api/auth/login', 404)] * 6
        )
        ip = '198.51.100.196'
        matches = [self.matches(log_record(ip, uri=uri, status=status))
                   for uri, status in requests]
        self.assertEqual(len(matches), 23)
        self.assertEqual([match for match in matches if match], [ip] * 6)

    def test_failed_login_keeps_parsed_epoch_timestamp(self):
        failures = self.failures(log_record('2001:db8::196', status=404))
        self.assertEqual(len(failures), 1)
        # {Epoch} can discard subsecond precision; it must retain the log's
        # epoch second instead of substituting the current processing time.
        self.assertEqual(int(failures[0][2]), 1790948415)

    def test_refresh_success_reset_and_other_methods_do_not_count(self):
        for ip in ('198.51.100.196', '2001:db8::196'):
            for overrides in (
                {'uri': '/api/auth/refresh', 'status': 200},
                {'uri': '/api/auth/refresh', 'status': 401},
                {'uri': '/api/auth/refresh', 'status': 404},
                {'uri': '/api/auth/refresh', 'status': 429},
                {'uri': '/api/auth/logout', 'status': 404},
                {'uri': '/api/auth/register', 'status': 404},
                {'uri': '/api/auth/login/extra', 'status': 404},
                {'uri': '/missing-page', 'status': 404},
                {'status': 200}, {'method': 'GET'}, {'status': 500},
                {'status': 400}, {'status': 403}, {'status': 422},
                {'uri': '/api/auth/requestPasswordReset', 'status': 404},
                {'uri': '/api/auth/requestPasswordReset', 'status': 429},
                {'uri': '/reset-password?token=REDACTED', 'method': 'GET'},
                {'logger': 'http.log.access.access', 'status': 404},
            ):
                with self.subTest(ip=ip, overrides=overrides):
                    self.assertIsNone(self.matches(log_record(ip, **overrides)))

    def test_direct_or_unrecognized_proxy_addresses_do_not_count(self):
        for ip in ('198.51.100.196', '2001:db8::196'):
            for status in (401, 404, 429):
                with self.subTest(ip=ip, status=status):
                    self.assertIsNone(self.matches(log_record(ip,
                        remote_ip=ip, status=status)))


if __name__ == '__main__':
    unittest.main(verbosity=2)
