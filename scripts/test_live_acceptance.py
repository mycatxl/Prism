"""Offline adversarial checks; no public endpoint is contacted by this suite."""

import contextlib
import copy
import io
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch

import live_acceptance as live


def credential():
    return "pk_" + secrets.token_hex(16) + "_" + secrets.token_hex(32)


def manifest():
    return {"schema": 1, "prism_url": "http://127.0.0.1:23456", "platform": "Default",
            "platform_id": "default-platform", "node_hash": "a" * 32, "git_commit": "abcdef0",
            "expected_egress": "1.1.1.1", "target_url": "https://receipt.example/check",
            "own_cidrs": ["203.0.113.0/24"], **{name: "offline-generated-reference" for name in live.REF_FIELDS}}


class AcceptanceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.ca = str(Path(cls.tmp.name) / "fixture.crt")
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                        "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost",
                        "-keyout", str(Path(cls.tmp.name) / "fixture.key"), "-out", cls.ca],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def setUp(self):
        self.proxy_key, self.admin_key = credential(), credential()
        self.runner = live.Runner(manifest(), self.proxy_key, self.admin_key,
                                  offline=True, fixture_ca=self.ca)
        now = live.stamp()
        self.api_data = {
            "/system/info": {"git_commit": "abcdef0", "started_at": now},
            "/system/config": {"egress_trace_url": live.TRACE_URL, "node_privacy": {
                "probe_status": "ready", "probed_at": now, "active_ips": ["203.0.113.1"],
                "own_cidrs": [], "detected_ips": [], "self_probe_url": "https://api64.ipify.org",
                "echo_enabled": False}},
            "/nodes/" + "a" * 32: {"node_hash": "a" * 32, "protocol": "http", "enabled": True,
                                    "has_outbound": True, "egress_ip": "1.1.1.1", "reference_latency_ms": 10,
                                    "privacy": {"status": "accepted", "reasons": [], "warnings": [],
                                                "config_check": {"state": "known"}}},
            "/intel/ip/1.1.1.1": {"ip": "1.1.1.1", "evidence": [{"ip": "1.1.1.1", "provider": "ippure",
                "profile": "ippure-via-node-v2", "status": "ok", "via_node_hash": "a" * 32,
                "observed_at_ns": time.time_ns() - 1_000_000_000, "valid_until_ns": time.time_ns() + 60_000_000_000,
                "normalized_json": json.dumps({"ip": "1.1.1.1", "provider": "ippure", "fraud_score": 30})}]}}

    def api(self, path):
        return copy.deepcopy(self.api_data[path])

    def body(self, protocol, endpoint, account, marker):
        if endpoint == self.runner.trace_url:
            return b"ip=1.1.1.1\ncolo=FIXTURE\n"
        if endpoint == self.runner.ippure_url:
            return b'{"ip":"1.1.1.1","fraudScore":30}'
        return json.dumps({"marker": marker, "marker_survived": True, "observed_address": "1.1.1.1",
                           "headers": {name: [] for name in live.IDENTITY_HEADERS}}).encode()

    def test_default_never_reads_manifest_secrets_or_network(self):
        with patch("builtins.open", side_effect=AssertionError("read attempted")), \
                patch.object(socket, "create_connection", side_effect=AssertionError("dial attempted")), \
                contextlib.redirect_stdout(io.StringIO()) as out:
            code = live.main(["--manifest", "unavailable", "--proxy-key-fd", "3"])
        report = json.loads(out.getvalue())
        self.assertEqual(code, 2)
        self.assertEqual(report["status"], "NOT_RUN")
        self.assertTrue(all(p["status"] == "NOT_RUN" for p in report["protocols"].values()))

    def test_argument_errors_do_not_echo_secrets(self):
        with contextlib.redirect_stdout(io.StringIO()) as out:
            code = live.main(["--unknown=" + self.proxy_key])
        self.assertEqual(code, 2)
        self.assertTrue(self.proxy_key not in out.getvalue())
        self.assertEqual(json.loads(out.getvalue())["code"], "INVALID_ARGUMENTS")

    def test_manifest_refuses_missing_authority_credentials_and_unsafe_endpoints(self):
        changes = [{"authority_ref": ""}, {"proxy_secret_ref": self.proxy_key},
                   {"unexpected": True}, {"schema": True}, {"prism_url": "http://localhost:23456"},
                   {"prism_url": "http://192.0.2.1:23456"}, {"target_url": "http://receipt.example/check"},
                   {"target_url": "https://localhost/check"}, {"target_url": "https://127.0.0.1/check"},
                   {"target_url": "https://receipt.example/check?secret=value"},
                   {"target_url": "https://receipt.example/../check"}, {"platform": "bad.account"},
                   {"own_cidrs": []}, {"own_cidrs": ["::ffff:1.1.1.1/128"]},
                   {"expected_egress": "2001:4860:4860::8888"}, {"expected_egress": "192.0.2.1"}]
        for change in changes:
            with self.subTest(fields=list(change)):
                with self.assertRaises(live.Rejected):
                    live.validate_manifest(manifest() | change, False)

    def test_live_never_accepts_fixture_ca_or_provider_override(self):
        for options in ({"fixture_ca": self.ca}, {"trace_url": "https://localhost/trace"},
                        {"ippure_url": "https://localhost/info"}):
            with self.subTest(fields=list(options)):
                with self.assertRaisesRegex(live.Rejected, "LIVE_ENDPOINT_OVERRIDE"):
                    live.Runner(manifest(), self.proxy_key, self.admin_key, **options)

    def test_secret_descriptors_are_bounded_and_explicit(self):
        for payload, session, success in ((self.proxy_key.encode() + b"\n", False, True),
                                         (secrets.token_urlsafe(32).encode(), True, True),
                                         (b"x" * 256, False, False), (b"not-a-key", False, False),
                                         (self.proxy_key.encode() + b"\nextra", False, False)):
            reader, writer = os.pipe()
            try:
                os.write(writer, payload)
                os.close(writer)
                writer = -1
                if success:
                    self.assertTrue(live.read_key(reader, session=session).encode() == payload.rstrip(b"\n"))
                else:
                    with self.assertRaises(live.Rejected):
                        live.read_key(reader, session=session)
            finally:
                os.close(reader)
                if writer >= 0:
                    os.close(writer)
        with self.assertRaisesRegex(live.Rejected, "SECRET_DESCRIPTOR_REQUIRED"):
            live.read_key(0)

    def test_unprotected_regular_secret_file_is_rejected(self):
        path = Path(self.tmp.name) / "unprotected-test-input"
        path.write_bytes(b"test")
        path.chmod(0o644)
        with path.open("rb") as source:
            with self.assertRaisesRegex(live.Rejected, "UNPROTECTED_SECRET_DESCRIPTOR"):
                live.read_key(source.fileno())

    def test_snapshot_rejects_stale_unbound_unscored_or_own_asset(self):
        changes = [
            ("/system/info", lambda d: d.update(git_commit="different")),
            ("/system/config", lambda d: d["node_privacy"].update(probe_status="failed")),
            ("/system/config", lambda d: d["node_privacy"].update(active_ips=[])),
            ("/system/config", lambda d: d["node_privacy"].update(detected_ips=["1.1.1.1"])),
            ("/nodes/" + "a" * 32, lambda d: d.update(protocol="direct")),
            ("/nodes/" + "a" * 32, lambda d: d.update(reference_latency_ms=0)),
            ("/nodes/" + "a" * 32, lambda d: d["privacy"].update(status="unmanaged")),
            ("/nodes/" + "a" * 32, lambda d: d.update(egress_ip="8.8.8.8")),
            ("/intel/ip/1.1.1.1", lambda d: d["evidence"][0].update(valid_until_ns=1)),
            ("/intel/ip/1.1.1.1", lambda d: d["evidence"][0].update(observed_at_ns=time.time_ns() + 10**15)),
            ("/intel/ip/1.1.1.1", lambda d: d["evidence"][0].update(via_node_hash="b" * 32)),
            ("/intel/ip/1.1.1.1", lambda d: d["evidence"][0].update(status="error")),
            ("/intel/ip/1.1.1.1", lambda d: d["evidence"][0].update(normalized_json='{"ip":"1.1.1.1","provider":"ippure"}'))]
        original = copy.deepcopy(self.api_data)
        for index, (path, mutate) in enumerate(changes):
            with self.subTest(case=index):
                self.api_data = copy.deepcopy(original)
                mutate(self.api_data[path])
                with patch.object(self.runner, "api", side_effect=self.api), self.assertRaises(live.Rejected):
                    self.runner.snapshot()

    def test_zero_is_scored_but_null_bool_and_out_of_range_are_not(self):
        for score in (0, 100, None, True, -1, 101, "0"):
            with self.subTest(score=score):
                body = json.dumps({"ip": "1.1.1.1", "fraudScore": score}).encode()
                if type(score) is int and 0 <= score <= 100:
                    self.assertEqual(self.runner.check_body("ippure", body, "", False)["purity"], 100 - score)
                else:
                    with self.assertRaisesRegex(live.Rejected, "IPPURE_UNSCORED"):
                        self.runner.check_body("ippure", body, "", False)

    def test_egress_challenge_and_identity_mismatch_fail_closed(self):
        marker = secrets.token_hex(16)
        target = json.loads(self.body("http", self.runner.m["target_url"], "account", marker))
        cases = [("trace", b"ip=8.8.8.8"), ("trace", b"ip=1.1.1.1\nip=1.1.1.1"),
                 ("ippure", b'{"ip":"8.8.8.8","fraudScore":0}'),
                 ("target", json.dumps(target | {"marker": "stale"}).encode()),
                 ("target", json.dumps(target | {"observed_address": "8.8.8.8"}).encode()),
                 ("target", json.dumps(target | {"headers": {}}).encode()),
                 ("target", json.dumps(target | {"headers": {name: ["8.8.8.8"] for name in live.IDENTITY_HEADERS}}).encode())]
        for kind, body in cases:
            with self.subTest(kind=kind), self.assertRaises(live.Rejected):
                self.runner.check_body(kind, body, marker, True)
        self.assertFalse(self.runner.check_body("target", json.dumps(target | {"headers": {}}).encode(), marker, False)["identity_checked"])

    def test_offline_success_is_never_live_pass(self):
        with patch.object(self.runner, "api", side_effect=self.api), \
                patch.object(self.runner, "request", side_effect=self.body), \
                patch.object(self.runner, "correlate", side_effect=lambda p, u, a, s: a):
            report, code = self.runner.run()
        self.assertEqual(code, 0)
        self.assertEqual(report["status"], "NOT_RUN")
        self.assertEqual(report["offline_checks"], "PASS")
        self.assertEqual([len(p["evidence"]) for p in report["protocols"].values()], [3, 3, 3])
        self.assertTrue(self.proxy_key not in json.dumps(report) and self.admin_key not in json.dumps(report))

    def test_one_protocol_failure_retains_not_run_for_unattempted_protocol(self):
        def request(protocol, *args):
            if protocol == "socks5":
                raise OSError(self.proxy_key)  # unsafe exception text must disappear
            return self.body(protocol, *args)
        with patch.object(self.runner, "api", side_effect=self.api), \
                patch.object(self.runner, "request", side_effect=request), \
                patch.object(self.runner, "correlate", return_value="log-id"):
            report, code = self.runner.run()
        self.assertEqual(code, 1)
        self.assertEqual([p["status"] for p in report["protocols"].values()], ["PASS", "FAIL", "NOT_RUN"])
        self.assertTrue(self.proxy_key not in json.dumps(report))

    def test_instance_change_after_requests_invalidates_result(self):
        with patch.object(self.runner, "snapshot", side_effect=[{"echo_enabled": False, "node_hash": "a"},
                                                              {"echo_enabled": False, "node_hash": "b"}]), \
                patch.object(self.runner, "request", side_effect=self.body), \
                patch.object(self.runner, "correlate", return_value="log-id"):
            report, code = self.runner.run()
        self.assertEqual(code, 1)
        self.assertEqual(report["code"], "INSTANCE_CHANGED")

    def test_log_correlation_rejects_wrong_node_exit_type_time_and_replay(self):
        started = live.stamp()
        row = {"id": "log-1", "account": "account", "platform_id": "default-platform", "proxy_type": 1,
               "target_host": "cloudflare.com:443", "node_hash": "a" * 32, "egress_ip": "1.1.1.1",
               "net_ok": True, "prism_error": "", "ts": started}
        for field, value in (("node_hash", "b" * 32), ("egress_ip", "8.8.8.8"), ("proxy_type", 3),
                             ("ts", "2000-01-01T00:00:00Z"), ("account", "other"),
                             ("net_ok", False), ("prism_error", "ACCOUNTING_UNAVAILABLE")):
            with self.subTest(field=field), patch.object(self.runner, "api", return_value={"items": [row | {field: value}]}):
                with self.assertRaisesRegex(live.Rejected, "LOG_CORRELATION_FAILED"):
                    self.runner.correlate("http", live.TRACE_URL, "account", started)
        with patch.object(self.runner, "api", return_value={"items": [row]}):
            self.assertEqual(self.runner.correlate("http", live.TRACE_URL, "account", started), "log-1")
            with self.assertRaisesRegex(live.Rejected, "LOG_REPLAY"):
                self.runner.correlate("http", live.TRACE_URL, "account", started)
        with patch.object(self.runner, "api", return_value={"items": [row, row]}):
            with self.assertRaisesRegex(live.Rejected, "LOG_AMBIGUOUS"):
                self.runner.correlate("http", live.TRACE_URL, "account", started)

    def test_no_log_cannot_turn_timeout_into_success(self):
        self.runner.deadline = time.monotonic() - 1
        with patch.object(self.runner, "api", return_value={"items": []}):
            with self.assertRaisesRegex(live.Rejected, "RUN_TIMEOUT"):
                self.runner.correlate("http", live.TRACE_URL, "account", live.stamp())

    def test_http_redirect_error_and_large_body_never_pass(self):
        class Response:
            status = 200
            def getheader(self, name, default):
                return default
            def read(self, limit):
                return b"x" * limit
        response = Response()
        conn = unittest.mock.Mock()
        conn.getresponse.return_value = response
        for status, code in ((302, "REDIRECT_REJECTED"), (407, "AUTH_OR_POLICY_REJECTED"),
                             (500, "HTTP_STATUS_REJECTED"), (200, "RESPONSE_TOO_LARGE")):
            response.status = status
            with self.assertRaisesRegex(live.Rejected, code):
                self.runner.response(conn)

    def test_socks_noauth_and_wrong_password_reply_are_rejected(self):
        for reply, code in ((b"\x05\x00", "SOCKS_AUTH_REQUIRED"), (b"\x05\xff", "SOCKS_AUTH_REQUIRED"),
                            (b"\x05\x02\x01\x01", "AUTH_REJECTED")):
            client, peer = socket.socketpair()
            try:
                client.settimeout(1)
                peer.sendall(reply)
                with self.assertRaisesRegex(live.Rejected, code):
                    live.socks_connect(client, "receipt.example", 443, "Default.account", self.proxy_key)
                offered = peer.recv(3)
                self.assertEqual(offered, b"\x05\x01\x02")
            finally:
                client.close()
                peer.close()

    def test_connect_rejects_unauthorized_proxy(self):
        client, peer = socket.socketpair()
        try:
            client.settimeout(1)
            peer.sendall(b"HTTP/1.1 407 Required\r\n\r\n")
            with self.assertRaisesRegex(live.Rejected, "AUTH_REJECTED"):
                live.http_connect(client, "receipt.example", 443, "Default.account", self.proxy_key)
        finally:
            client.close()
            peer.close()

    def test_tls_verification_failure_is_sanitized(self):
        err = ssl.SSLCertVerificationError(self.proxy_key)
        self.assertEqual(live.classify(err), "TLS_VERIFICATION_FAILED")

    def test_response_classifies_prism_invalid_key_without_accepting_policy_errors(self):
        conn = unittest.mock.Mock()
        response = conn.getresponse.return_value
        response.status = 403
        for header, expected in (("AUTH_FAILED", "AUTH_REJECTED"),
                                 ("PLATFORM_FORBIDDEN", "AUTH_OR_POLICY_REJECTED"),
                                 ("", "AUTH_OR_POLICY_REJECTED")):
            with self.subTest(expected=expected):
                response.getheader.return_value = header
                with self.assertRaisesRegex(live.Rejected, expected):
                    self.runner.response(conn)
                response.read.assert_not_called()

    def test_all_protocols_bind_every_receipt_and_discard_response_secrets(self):
        for protocol in live.PROTOCOLS:
            with self.subTest(protocol=protocol):
                started = live.stamp()
                row = {"id": "log-" + protocol, "account": "account", "platform_id": "default-platform",
                       "proxy_type": live.PROTOCOLS[protocol], "target_host": "receipt.example:8443",
                       "node_hash": "a" * 32, "egress_ip": "1.1.1.1", "net_ok": True, "prism_error": "", "ts": started}
                endpoint = "https://receipt.example:8443/check"
                with patch.object(self.runner, "api", return_value={"items": [row]}):
                    self.assertEqual(self.runner.correlate(protocol, endpoint, "account", started), row["id"])
                for field, value in (("platform_id", "other"), ("target_host", "other.example:8443"),
                                     ("node_hash", "b" * 32), ("egress_ip", "8.8.8.8")):
                    with patch.object(self.runner, "api", return_value={"items": [row | {field: value}]}):
                        with self.assertRaisesRegex(live.Rejected, "LOG_CORRELATION_FAILED"):
                            self.runner.correlate(protocol, endpoint, "account", started)
        marker = secrets.token_hex(16)
        body = json.loads(self.body("http", self.runner.m["target_url"], "account", marker))
        body.update(token=self.proxy_key, url="https://private.example/", exception=self.admin_key)
        result = json.dumps(self.runner.check_body("target", json.dumps(body).encode(), marker, False))
        for secret in (self.proxy_key, self.admin_key, "https://private.example/", marker):
            self.assertNotIn(secret, result)

    def test_connect_classifies_prism_invalid_key_without_accepting_policy_errors(self):
        # Prism distinguishes missing credentials (407) from invalid ones
        # (403 + AUTH_FAILED); an unrelated 403 is not proof of key rejection.
        for headers, expected in ((b"X-Prism-Error: AUTH_FAILED\r\n", "AUTH_REJECTED"),
                                  (b"", "HTTP_CONNECT_REJECTED"),
                                  (b"X-Prism-Error: PLATFORM_FORBIDDEN\r\n", "HTTP_CONNECT_REJECTED")):
            with self.subTest(expected=expected):
                client, peer = socket.socketpair()
                try:
                    client.settimeout(1)
                    peer.sendall(b"HTTP/1.1 403 Forbidden\r\n" + headers + b"\r\n")
                    with self.assertRaisesRegex(live.Rejected, expected):
                        live.http_connect(client, "receipt.example", 443, "Default.account", self.proxy_key)
                finally:
                    client.close()
                    peer.close()


if __name__ == "__main__":
    unittest.main()
