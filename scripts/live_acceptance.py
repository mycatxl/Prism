#!/usr/bin/env python3
"""Explicit, bounded Prism data-plane acceptance. No deployment or secret discovery.

Default: NOT_RUN (exit 2). See docs/LIVE_ACCEPTANCE.md for the operator contract.
Only allowlisted evidence is emitted; response bodies, URLs, credentials and
exception messages are never serialized. Offline runs can never emit live PASS.
"""

import argparse
import base64
import datetime as dt
import hashlib
import http.client
import ipaddress
import json
import os
import re
import secrets
import select
import socket
import ssl
import stat
import struct
import sys
import time
from urllib.parse import urlencode, urlsplit


TRACE_URL = "https://cloudflare.com/cdn-cgi/trace"
IPPURE_URL = "https://my.ippure.com/v1/info"
PROTOCOLS = {"http": 1, "socks5": 3, "reverse": 2}
IDENTITY_HEADERS = ("X-Forwarded-For", "Via", "Forwarded", "X-Real-IP",
                    "Client-IP", "X-Client-IP", "True-Client-IP",
                    "CF-Connecting-IP", "X-ProxyUser-IP")
MAX_BODY = 65536
REF_FIELDS = ("authority_ref", "upstream_ref", "target_ref", "asset_inventory_ref",
              "proxy_secret_ref", "admin_secret_ref")
MANIFEST_FIELDS = {"schema", "prism_url", "platform", "platform_id", "node_hash",
                   "expected_egress", "git_commit", "target_url", "own_cidrs", *REF_FIELDS}


class Rejected(Exception):
    """The code is static, never taken from a peer or a secret-bearing exception."""


def require(ok, code):
    if not ok:
        raise Rejected(code)


def digest(value):
    return hashlib.sha256(value).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def object_pairs(pairs):
    out = {}
    for key, value in pairs:
        require(key not in out, "DUPLICATE_JSON_FIELD")
        out[key] = value
    return out


def decode(body):
    try:
        return json.loads(body, object_pairs_hook=object_pairs,
                          parse_constant=lambda _: (_ for _ in ()).throw(Rejected("INVALID_JSON")))
    except (ValueError, UnicodeError):
        raise Rejected("INVALID_JSON") from None


def stamp():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def timestamp(value):
    try:
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        require(parsed.tzinfo is not None, "INVALID_TIMESTAMP")
        return parsed.timestamp()
    except (ValueError, TypeError, AttributeError):
        raise Rejected("INVALID_TIMESTAMP") from None


def address(value):
    try:
        ip = ipaddress.ip_address(value)
        require("%" not in str(ip), "INVALID_ADDRESS")
        return ip.ipv4_mapped if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped else ip
    except ValueError:
        raise Rejected("INVALID_ADDRESS") from None


def networks(values):
    require(isinstance(values, list) and len(values) <= 4096, "INVALID_ASSET_INVENTORY")
    try:
        out = []
        for value in values:
            net = ipaddress.ip_network(value, strict=False)
            if isinstance(net, ipaddress.IPv6Network) and net.network_address.ipv4_mapped:
                require(net.prefixlen >= 96, "INVALID_ASSET_INVENTORY")
                net = ipaddress.ip_network(f"{net.network_address.ipv4_mapped}/{net.prefixlen - 96}")
            out.append(net)
        return out
    except (ValueError, TypeError):
        raise Rejected("INVALID_ASSET_INVENTORY") from None


def url(value, *, local=False, offline=False):
    require(isinstance(value, str) and value.isascii() and len(value) <= 2048
            and not any(c.isspace() or ord(c) < 32 for c in value), "INVALID_ENDPOINT")
    try:
        parsed = urlsplit(value)
        require(parsed.hostname and parsed.username is None and parsed.password is None
                and not parsed.fragment and not parsed.query and parsed.port != 0,
                "INVALID_ENDPOINT")
        if local:
            require(parsed.scheme == "http" and parsed.path in ("", "/")
                    and address(parsed.hostname).is_loopback and parsed.port is not None,
                    "PRISM_REQUIRES_EXPLICIT_LOOPBACK")
        else:
            require(parsed.scheme == "https", "HTTPS_REQUIRED")
            require(not any(part in (".", "..") for part in parsed.path.split("/"))
                    and "%" not in parsed.path, "INVALID_ENDPOINT")
            if not offline:
                require(parsed.hostname.lower() != "localhost" and "." in parsed.hostname,
                        "PUBLIC_TARGET_REQUIRED")
                try:
                    require(address(parsed.hostname).is_global, "PUBLIC_TARGET_REQUIRED")
                except Rejected as err:
                    if str(err) != "INVALID_ADDRESS":
                        raise
        return parsed
    except ValueError:
        raise Rejected("INVALID_ENDPOINT") from None


def validate_manifest(data, offline):
    require(isinstance(data, dict) and set(data) == MANIFEST_FIELDS
            and type(data["schema"]) is int and data["schema"] == 1, "INVALID_MANIFEST")
    for field in REF_FIELDS:
        require(isinstance(data[field], str)
                and re.fullmatch(r"[A-Za-z0-9_./:-]{1,160}", data[field])
                and "pk_" not in data[field], "AUTHORITY_REFERENCE_REQUIRED")
    for field, pattern in (("node_hash", r"[0-9a-f]{32}"),
                           ("git_commit", r"[0-9a-f]{7,40}"),
                           ("platform_id", r"[A-Za-z0-9_-]{1,64}"),
                           ("platform", r"[A-Za-z0-9_-]{1,64}")):
        require(isinstance(data[field], str) and re.fullmatch(pattern, data[field]), "INVALID_MANIFEST")
    url(data["prism_url"], local=True)
    target = url(data["target_url"], offline=offline)
    require(target.path == "/check", "CONTROLLED_TARGET_REQUIRED")
    ip = address(data["expected_egress"])
    require(ip.version == 4 and (offline or ip.is_global), "SCORED_IPV4_REQUIRED")
    own = networks(data["own_cidrs"])
    require(bool(own), "ASSET_INVENTORY_REQUIRED")
    require(not any(ip in net for net in own), "OWN_ASSET_EGRESS")


def read_key(fd, *, session=False):
    require(type(fd) is int and fd >= 3, "SECRET_DESCRIPTOR_REQUIRED")
    try:
        info = os.fstat(fd)
        require(stat.S_ISFIFO(info.st_mode) or
                (stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
                 and info.st_mode & 0o077 == 0), "UNPROTECTED_SECRET_DESCRIPTOR")
        body = b""
        end = time.monotonic() + 5
        while b"\n" not in body:
            remaining = end - time.monotonic()
            require(remaining > 0 and select.select([fd], [], [], remaining)[0], "SECRET_READ_TIMEOUT")
            chunk = os.read(fd, 256 - len(body))
            if not chunk:
                break
            body += chunk
            require(len(body) < 256, "INVALID_CREDENTIAL")
        pattern = rb"[A-Za-z0-9_-]{43}\n?" if session else rb"pk_[0-9a-f]{32}_[0-9a-f]{64}\n?"
        require(re.fullmatch(pattern, body), "INVALID_CREDENTIAL")
        return body.rstrip(b"\n").decode("ascii")
    except OSError:
        raise Rejected("SECRET_READ_FAILED") from None


def recv_exact(conn, length):
    out = b""
    while len(out) < length:
        part = conn.recv(length - len(out))
        require(part, "PROTOCOL_TRUNCATED")
        out += part
    return out


def socks_connect(conn, host, port, identity, key):
    conn.sendall(b"\x05\x01\x02")  # offer only RFC1929, never NOAUTH
    require(recv_exact(conn, 2) == b"\x05\x02", "SOCKS_AUTH_REQUIRED")
    username, password = identity.encode(), key.encode()
    require(len(username) <= 255 and len(password) <= 255, "SOCKS_IDENTITY_TOO_LONG")
    conn.sendall(b"\x01" + bytes([len(username)]) + username + bytes([len(password)]) + password)
    require(recv_exact(conn, 2) == b"\x01\x00", "AUTH_REJECTED")
    domain = host.encode("idna")
    require(len(domain) <= 255, "INVALID_ENDPOINT")
    conn.sendall(b"\x05\x01\x00\x03" + bytes([len(domain)]) + domain + struct.pack("!H", port))
    version, code, reserved, kind = recv_exact(conn, 4)
    require(version == 5 and code == 0 and reserved == 0, "SOCKS_CONNECT_REJECTED")
    require(kind in (1, 3, 4), "SOCKS_INVALID_REPLY")
    length = {1: 4, 4: 16}.get(kind)
    if length is None:
        length = recv_exact(conn, 1)[0]
    recv_exact(conn, length + 2)


def http_connect(conn, host, port, identity, key):
    auth = base64.b64encode((identity + ":" + key).encode()).decode()
    authority = f"{host}:{port}"
    conn.sendall((f"CONNECT {authority} HTTP/1.1\r\nHost: {authority}\r\n"
                  f"Proxy-Authorization: Basic {auth}\r\n\r\n").encode())
    head = b""
    while not head.endswith(b"\r\n\r\n"):
        require(len(head) < 16384, "PROTOCOL_HEADERS_TOO_LARGE")
        head += recv_exact(conn, 1)
    status = head.split(b"\r\n", 1)[0].split(b" ")
    require(len(status) >= 2 and status[0] in (b"HTTP/1.1", b"HTTP/1.0"), "HTTP_INVALID_REPLY")
    errors = [value.strip() for line in head.split(b"\r\n")[1:]
              for name, separator, value in [line.partition(b":")]
              if separator and name.lower() == b"x-prism-error"]
    # Invalid AccessKeys receive 403 AUTH_FAILED; 407 requests credentials.
    # Other 403 responses remain connect/policy failures, never successes.
    require(status[1] != b"407" and not (status[1] == b"403" and errors == [b"AUTH_FAILED"]),
            "AUTH_REJECTED")
    require(status[1] == b"200", "HTTP_CONNECT_REJECTED")


class Runner:
    def __init__(self, manifest, proxy_key, admin_key, *, offline=False,
                 fixture_ca=None, trace_url=TRACE_URL, ippure_url=IPPURE_URL, timeout=180,
                 admin_session=False):
        validate_manifest(manifest, offline)
        require(30 <= timeout <= 600, "INVALID_TIMEOUT")
        require(offline or (fixture_ca is None and trace_url == TRACE_URL and ippure_url == IPPURE_URL),
                "LIVE_ENDPOINT_OVERRIDE")
        self.m = manifest
        self.offline = offline
        self.proxy_key, self.admin_key = proxy_key, admin_key
        self.admin_session = admin_session
        self.base = url(manifest["prism_url"], local=True)
        self.expected = address(manifest["expected_egress"])
        self.deadline = time.monotonic() + timeout
        self.trace_url, self.ippure_url = trace_url, ippure_url
        for endpoint in (trace_url, ippure_url):
            url(endpoint, offline=offline)
        self.tls = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        self.tls.minimum_version = ssl.TLSVersion.TLSv1_2
        if offline:
            require(fixture_ca is not None, "FIXTURE_CA_REQUIRED")
            self.tls.load_verify_locations(cafile=fixture_ca)
        else:
            # OpenSSL's compiled paths ignore SSL_CERT_FILE/SSL_CERT_DIR.
            paths = ssl.get_default_verify_paths()
            self.tls.load_verify_locations(cafile=paths.openssl_cafile, capath=paths.openssl_capath)
        self.report = {"schema": 1, "status": "NOT_RUN", "mode": "offline_fixture" if offline else "live",
                       "run_id": secrets.token_hex(16), "started_at": stamp(),
                       "manifest_sha256": digest(canonical(manifest)),
                       "protocols": {name: {"status": "NOT_RUN", "evidence": []} for name in PROTOCOLS}}
        self.used_logs = set()

    def remaining(self):
        value = self.deadline - time.monotonic()
        require(value > 0, "RUN_TIMEOUT")
        return min(15, value)

    def response(self, conn):
        response = conn.getresponse()
        require(not (response.status == 403 and response.getheader("X-Prism-Error", "") == "AUTH_FAILED"),
                "AUTH_REJECTED")
        require(response.status not in (401, 403, 407), "AUTH_OR_POLICY_REJECTED")
        require(not 300 <= response.status < 400, "REDIRECT_REJECTED")
        require(response.status == 200, "HTTP_STATUS_REJECTED")
        require(response.getheader("Content-Encoding", "identity") == "identity", "ENCODING_REJECTED")
        body = response.read(MAX_BODY + 1)
        require(len(body) <= MAX_BODY, "RESPONSE_TOO_LARGE")
        return body

    def api(self, path):
        conn = http.client.HTTPConnection(self.base.hostname, self.base.port, timeout=self.remaining())
        try:
            headers = {"Connection": "close"}
            if self.admin_session:
                headers["Cookie"] = "prism_session=" + self.admin_key
            else:
                headers["Authorization"] = "Bearer " + self.admin_key
            conn.request("GET", "/api/v1" + path, headers=headers)
            data = decode(self.response(conn))
            require(isinstance(data, dict), "INVALID_API_RESPONSE")
            return data
        finally:
            conn.close()

    def request(self, protocol, endpoint, account, marker):
        target = url(endpoint, offline=self.offline)
        port = target.port or 443
        identity = self.m["platform"] + "." + account
        conn = http.client.HTTPConnection(self.base.hostname, self.base.port, timeout=self.remaining())
        try:
            conn.connect()  # always Prism, never an environment proxy or direct origin
            if protocol == "reverse":
                path = f"/{self.proxy_key}/{identity}/https/{target.netloc}{target.path or '/'}"
            else:
                if protocol == "http":
                    http_connect(conn.sock, target.hostname, port, identity, self.proxy_key)
                else:
                    socks_connect(conn.sock, target.hostname, port, identity, self.proxy_key)
                conn.sock = self.tls.wrap_socket(conn.sock, server_hostname=target.hostname)
                path = target.path or "/"
            conn.request("GET", path, headers={"Host": target.netloc,
                         "Connection": "close", "Cache-Control": "no-cache, no-store",
                         "X-Prism-Privacy-Marker": marker, "Accept-Encoding": "identity"})
            return self.response(conn)
        finally:
            conn.close()

    def snapshot(self):
        info = self.api("/system/info")
        require(info.get("git_commit") == self.m["git_commit"], "BUILD_MISMATCH")
        require(timestamp(info.get("started_at")) <= time.time(), "INSTANCE_CLOCK_INVALID")
        cfg = self.api("/system/config")
        privacy = cfg.get("node_privacy", {})
        require(privacy.get("probe_status") == "ready" and privacy.get("active_ips"), "OWN_ASSET_DISCOVERY_INCOMPLETE")
        require(timestamp(privacy.get("probed_at")) <= time.time(), "INSTANCE_CLOCK_INVALID")
        if not self.offline:
            require(cfg.get("egress_trace_url") == TRACE_URL, "LIVE_TRACE_OVERRIDE")
            url(privacy.get("self_probe_url"), offline=False)
        own = networks(self.m["own_cidrs"])
        for field in ("own_cidrs", "detected_ips", "active_ips"):
            own.extend(networks(privacy.get(field) or []))
        require(not any(self.expected in net for net in own), "OWN_ASSET_EGRESS")
        node = self.api("/nodes/" + self.m["node_hash"])
        decision = node.get("privacy", {})
        require(node.get("node_hash") == self.m["node_hash"] and node.get("protocol") not in (None, "", "direct")
                and node.get("enabled") is True and node.get("has_outbound") is True
                and node.get("circuit_open_since") is None, "NODE_UNAVAILABLE")
        require(decision.get("status") == "accepted" and not decision.get("reasons")
                and decision.get("config_check", {}).get("state") == "known", "NODE_PRIVACY_REJECTED")
        require("OWN_ASSET_DISCOVERY_FAILED" not in (decision.get("warnings") or []), "OWN_ASSET_DISCOVERY_INCOMPLETE")
        require(address(node.get("egress_ip", "")) == self.expected, "EGRESS_MISMATCH")
        latency = node.get("reference_latency_ms")
        require(type(latency) in (int, float) and latency > 0, "LATENCY_UNMEASURED")
        # Check the actual stored provider identity, IP, node binding and expiry.
        detail = self.api("/intel/ip/" + str(self.expected))
        require(detail.get("ip") == str(self.expected), "EVIDENCE_MISMATCH")
        accepted = []
        for evidence in detail.get("evidence", []):
            if evidence.get("provider") != "ippure" or evidence.get("profile") != "ippure-via-node-v2":
                continue
            normalized = decode(evidence.get("normalized_json", ""))
            if (evidence.get("ip") == str(self.expected) and evidence.get("via_node_hash") == self.m["node_hash"]
                    and evidence.get("status") == "ok" and not evidence.get("error_code")
                    and 0 < evidence.get("observed_at_ns", 0) <= time.time_ns() < evidence.get("valid_until_ns", 0)
                    and normalized.get("ip") == str(self.expected)
                    and normalized.get("provider") == "ippure"
                    and type(normalized.get("fraud_score")) is int and 0 <= normalized["fraud_score"] <= 100):
                accepted.append(evidence)
        require(len(accepted) == 1, "FRESH_SCORED_EVIDENCE_REQUIRED")
        echo = privacy.get("echo_enabled") is True
        if echo:
            require(privacy.get("echo_url") == self.m["target_url"]
                    and decision.get("identity_check") == "measured", "ECHO_EVIDENCE_REQUIRED")
        return {"git_commit": info["git_commit"], "started_at": info["started_at"],
                "node_hash": node["node_hash"], "egress_ip": str(self.expected),
                "assets_sha256": digest(canonical(sorted(str(net) for net in own))),
                "runtime_privacy_sha256": digest(canonical(privacy)), "echo_enabled": echo,
                "tls_verification_warning": "TLS_VERIFICATION_DISABLED" in (decision.get("warnings") or []),
                "stored_evidence_sha256": digest(canonical(accepted[0]))}

    def correlate(self, protocol, endpoint, account, started):
        target = url(endpoint, offline=self.offline)
        # All three paths log the requested authority, including explicit port.
        host = target.netloc if protocol == "reverse" else f"{target.hostname}:{target.port or 443}"
        query = urlencode({"account": account, "platform_id": self.m["platform_id"],
                           "proxy_type": PROTOCOLS[protocol], "target_host": host,
                           "from": started, "limit": 2})
        while True:
            page = self.api("/request-logs?" + query)
            rows = page.get("items")
            require(isinstance(rows, list) and not page.get("has_more") and len(rows) <= 1, "LOG_AMBIGUOUS")
            if rows:
                row = rows[0]
                require(row.get("account") == account and row.get("platform_id") == self.m["platform_id"]
                        and row.get("proxy_type") == PROTOCOLS[protocol] and row.get("target_host") == host
                        and row.get("node_hash") == self.m["node_hash"] and row.get("egress_ip") == str(self.expected)
                        and row.get("net_ok") is True and not row.get("prism_error")
                        and timestamp(started) <= timestamp(row.get("ts")) <= time.time(), "LOG_CORRELATION_FAILED")
                ident = row.get("id")
                require(isinstance(ident, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,80}", ident)
                        and ident not in self.used_logs, "LOG_REPLAY")
                self.used_logs.add(ident)
                return ident
            time.sleep(min(0.2, self.remaining()))

    def check_body(self, kind, body, marker, echo):
        if kind == "trace":
            pairs = [line.split("=", 1) for line in body.decode("ascii").splitlines() if "=" in line]
            values = object_pairs(pairs)
            require(address(values.get("ip", "")) == self.expected, "TRACE_EGRESS_MISMATCH")
            return {"egress_ip": str(self.expected)}
        data = decode(body)
        require(isinstance(data, dict), "INVALID_RESPONSE")
        if kind == "ippure":
            require(address(data.get("ip", "")) == self.expected, "IPPURE_EGRESS_MISMATCH")
            score = data.get("fraudScore")
            require(type(score) is int and 0 <= score <= 100, "IPPURE_UNSCORED")
            return {"egress_ip": str(self.expected), "fraud_score": score, "purity": 100 - score}
        require(data.get("marker") == marker and data.get("marker_survived") is True, "TARGET_CHALLENGE_MISMATCH")
        require(address(data.get("observed_address", "")) == self.expected, "TARGET_EGRESS_MISMATCH")
        if echo:
            headers = data.get("headers")
            require(isinstance(headers, dict), "HEADER_OBSERVATION_MISSING")
            for name in IDENTITY_HEADERS:
                values = headers.get(name)
                require(isinstance(values, list), "HEADER_OBSERVATION_MISSING")
                for value in values:
                    require(isinstance(value, str), "IDENTITY_ECHO_LEAK")
                    if value.strip():
                        require(address(value.strip()) == self.expected, "IDENTITY_ECHO_LEAK")
        return {"egress_ip": str(self.expected), "challenge_sha256": digest(marker.encode()),
                "identity_checked": echo}

    def run(self):
        active = None
        try:
            before = self.snapshot()
            self.report["instance"] = before
            for protocol in PROTOCOLS:
                active = self.report["protocols"][protocol]
                for kind, endpoint in (("trace", self.trace_url), ("ippure", self.ippure_url),
                                       ("target", self.m["target_url"])):
                    account = "accept-" + secrets.token_hex(16)
                    marker = secrets.token_hex(16)
                    started = stamp()
                    body = self.request(protocol, endpoint, account, marker)
                    observed = self.check_body(kind, body, marker, before["echo_enabled"])
                    log_id = self.correlate(protocol, endpoint, account, started)
                    active["evidence"].append({"kind": kind, "request_id": account, "log_id": log_id,
                                               "started_at": started, "finished_at": stamp(),
                                               "response_sha256": digest(body), **observed})
                active["status"] = "PASS"
                active = None
            after = self.snapshot()
            # A refreshed score is allowed. A different instance, asset guard,
            # upstream, echo contract or exit cannot inherit this run's results.
            require({k: v for k, v in before.items() if k != "stored_evidence_sha256"}
                    == {k: v for k, v in after.items() if k != "stored_evidence_sha256"}, "INSTANCE_CHANGED")
            self.report["final_stored_evidence_sha256"] = after["stored_evidence_sha256"]
            self.report["status"] = "NOT_RUN" if self.offline else "PASS"
            self.report["code"] = "OFFLINE_FIXTURE_ONLY" if self.offline else "LIVE_THREE_PROTOCOLS_VERIFIED"
            if self.offline:
                self.report["offline_checks"] = "PASS"
            code = 0
        except Exception as err:
            failure = classify(err)
            if active is not None:
                active.update(status="FAIL", code=failure)
            self.report.update(status="NOT_RUN" if self.offline else "FAIL", code=failure)
            if self.offline:
                self.report["offline_checks"] = "FAIL"
            code = 1
        self.report["finished_at"] = stamp()
        return self.report, code


def classify(err):
    if isinstance(err, Rejected):
        return str(err)  # only our own static constants enter Rejected
    if isinstance(err, ssl.SSLCertVerificationError):
        return "TLS_VERIFICATION_FAILED"
    if isinstance(err, (TimeoutError, socket.timeout)):
        return "NETWORK_TIMEOUT"
    if isinstance(err, (OSError, http.client.HTTPException, ssl.SSLError)):
        return "TRANSPORT_FAILED"
    return "INVALID_RESPONSE_OR_INTERNAL_ERROR"


class SafeParser(argparse.ArgumentParser):
    def error(self, _message):
        raise Rejected("INVALID_ARGUMENTS")


def main(argv=None):
    report = {"schema": 1, "status": "NOT_RUN", "code": "EXPLICIT_EXECUTION_REQUIRED",
              "protocols": {name: {"status": "NOT_RUN"} for name in PROTOCOLS}}
    code = 2
    try:
        parser = SafeParser(description=__doc__)
        parser.add_argument("--manifest")
        parser.add_argument("--execute", action="store_true")
        parser.add_argument("--proxy-key-fd", type=int)
        parser.add_argument("--admin-key-fd", type=int)
        parser.add_argument("--admin-session-fd", type=int)
        parser.add_argument("--timeout", type=int, default=180)
        parser.add_argument("--offline-fixture", action="store_true")
        parser.add_argument("--fixture-ca")
        parser.add_argument("--fixture-trace-url")
        args = parser.parse_args(argv)
        if args.execute:
            require(args.manifest is not None, "MANIFEST_REQUIRED")
            require(args.offline_fixture or (args.fixture_ca is None and args.fixture_trace_url is None), "LIVE_ENDPOINT_OVERRIDE")
            with open(args.manifest, "rb") as source:
                body = source.read(MAX_BODY + 1)
            require(len(body) <= MAX_BODY, "MANIFEST_TOO_LARGE")
            manifest = decode(body)
            validate_manifest(manifest, args.offline_fixture)
            require((args.admin_key_fd is None) != (args.admin_session_fd is None), "MANAGEMENT_DESCRIPTOR_REQUIRED")
            admin_fd = args.admin_key_fd if args.admin_key_fd is not None else args.admin_session_fd
            require(args.proxy_key_fd != admin_fd, "DISTINCT_SECRET_DESCRIPTORS_REQUIRED")
            proxy_key = read_key(args.proxy_key_fd)
            admin_key = read_key(admin_fd, session=args.admin_session_fd is not None)
            runner = Runner(manifest, proxy_key, admin_key, offline=args.offline_fixture,
                            fixture_ca=args.fixture_ca, trace_url=args.fixture_trace_url or TRACE_URL,
                            timeout=args.timeout, admin_session=args.admin_session_fd is not None)
            report, code = runner.run()
    except Exception as err:
        report["code"] = classify(err)
    print(json.dumps(report, sort_keys=True))
    return code


if __name__ == "__main__":
    sys.exit(main())
