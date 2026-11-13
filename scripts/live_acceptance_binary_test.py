"""Consume ONLY the disposable smoke fixture and exercise the CLI over its binary.

Invoked by smoke.sh after it has created its temporary session, key and TLS node.
No existing installation or authentication file is discovered by this helper.
"""
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

import live_acceptance as live


def main():
    base, jar_path, ca, trace, work = sys.argv[1:]
    live.url(base, local=True)
    live.require(live.url(trace, offline=True).hostname == "localhost", "FIXTURE_ENDPOINT_REQUIRED")
    proxy_key = live.read_key(3)
    jar = http.cookiejar.MozillaCookieJar(jar_path)
    jar.load(ignore_discard=True, ignore_expires=True)
    session = next(cookie.value for cookie in jar if cookie.name == "prism_session")
    manifest = {"schema": 1, "prism_url": base, "platform": "Default", "platform_id": "temporary",
                "node_hash": "0" * 32, "git_commit": "0000000", "expected_egress": "1.1.1.1",
                "target_url": trace + "/check", "own_cidrs": ["203.0.113.0/24"],
                **{field: "offline-generated-smoke-fixture" for field in live.REF_FIELDS}}
    runner = live.Runner(manifest, proxy_key, session, offline=True, fixture_ca=ca, admin_session=True)
    manifest["git_commit"] = runner.api("/system/info")["git_commit"]
    manifest["node_hash"] = runner.api("/nodes?limit=2")["items"][0]["node_hash"]
    manifest["platform_id"] = next(p["id"] for p in runner.api("/platforms")["items"] if p["name"] == "Default")
    manifest_path = Path(work) / "live-fixture-manifest.json"
    manifest_path.write_bytes(live.canonical(manifest))
    manifest_path.chmod(0o600)
    invalid_key = "pk_" + secrets.token_hex(16) + "_" + secrets.token_hex(32)
    for wrong_key in (False, True):
        readers = []
        try:
            for value in (invalid_key if wrong_key else proxy_key, session):
                reader, writer = os.pipe()
                readers.append(reader)
                os.write(writer, value.encode() + b"\n")
                os.close(writer)
            result = subprocess.run([sys.executable, "-B", str(Path(__file__).with_name("live_acceptance.py")),
                "--execute", "--manifest", str(manifest_path), "--offline-fixture", "--fixture-ca", ca,
                "--fixture-trace-url", trace + "/cdn-cgi/trace", "--proxy-key-fd", str(readers[0]),
                "--admin-session-fd", str(readers[1])], pass_fds=readers, capture_output=True, timeout=200)
            live.require(proxy_key.encode() not in result.stdout + result.stderr
                         and session.encode() not in result.stdout + result.stderr
                         and invalid_key.encode() not in result.stdout + result.stderr, "CLI_SECRET_DISCLOSURE")
            report = live.decode(result.stdout)
            live.require(report.get("status") == "NOT_RUN", "FIXTURE_CLAIMED_LIVE_PASS")
            if wrong_key:
                observed = report.get("code")
                observed = observed if observed in ("AUTH_REJECTED", "HTTP_CONNECT_REJECTED", "INVALID_CREDENTIAL") else "OTHER"
                print("[smoke] invalid-key CLI: exit=" + str(result.returncode) + " code=" + observed)
                live.require(result.returncode == 1 and report.get("code") == "AUTH_REJECTED", "BAD_KEY_NOT_REJECTED")
            else:
                live.require(result.returncode == 0, "BINARY_ACCEPTANCE_FAILED")
                live.require(report.get("offline_checks") == "PASS" and all(
                    p["status"] == "PASS" and len(p["evidence"]) == 3 for p in report["protocols"].values()), "BINARY_EVIDENCE_INCOMPLETE")
                (Path(work) / "live-offline-receipt.json").write_bytes(live.canonical(report))
        finally:
            for reader in readers:
                os.close(reader)
    # The CLI fails at the first rejected protocol. Exercise the same production
    # request path separately so later entrances also prove invalid-key refusal.
    runner.proxy_key = invalid_key
    for protocol in live.PROTOCOLS:
        try:
            runner.request(protocol, manifest["target_url"], "reject-" + secrets.token_hex(16), secrets.token_hex(16))
        except live.Rejected as error:
            live.require(live.classify(error) == "AUTH_REJECTED", "BAD_KEY_REJECTION_UNPROVEN")
        else:
            raise live.Rejected("BAD_KEY_NOT_REJECTED")
    print("[smoke] offline acceptance CLI: nine correlated requests; all three protocols reject a bad key; live NOT_RUN")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("[smoke] offline acceptance CLI failed: " + live.classify(error))
        sys.exit(1)
