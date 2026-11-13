"""Seed only a newly initialized disposable test store before its first start.

All discovery/probe URLs point at the caller's local fixture. This is offline
test configuration, never evidence of an independent or anonymous exit.
"""
import json
import sqlite3
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit

database, base = sys.argv[1:]
url = urlsplit(base)
if url.scheme not in ("http", "https") or url.hostname not in ("localhost", "127.0.0.1", "::1"):
    raise SystemExit("test discovery endpoint must be loopback")
cfg = dict(
    max_consecutive_failures=3, max_latency_test_interval="1h",
    max_authority_latency_test_interval="3h", max_egress_test_interval="24h",
    # Runtime patches validate the whole config: latency probes require HTTPS
    # even when self-discovery is a permitted loopback HTTP fixture.
    latency_test_url="https://"+url.netloc+"/cdn-cgi/trace", egress_trace_url=base+"/cdn-cgi/trace",
    latency_authorities=["localhost"], p2c_latency_window="10m", latency_decay_window="10m",
    cache_flush_interval="5s", cache_flush_dirty_threshold=1, intel_enabled=False,
    intel_node_workers=1, intel_check_concurrency_per_check=1, intel_max_running_jobs=1,
    intel_refresh_schedule="0 4 * * *", alert_usage_ratio=0.3,
    alert_window_minutes=60, alert_retention_days=90,
    node_privacy={"self_probe_url": base+"/self"},
)
with sqlite3.connect(Path(database).resolve().as_uri()+"?mode=rw", uri=True) as db:
    # Refuse existing runtime configuration; a test helper must not overwrite an
    # installation that has already run.
    db.execute("INSERT INTO system_config(id,config_json,version,updated_at_ns) VALUES(1,?,1,?)",
               (json.dumps(cfg), time.time_ns()))
