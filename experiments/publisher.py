#!/usr/bin/env python3
"""A publisher that writes to a quorum queue over the management API.

It is deliberately not an AMQP client. The management API publish endpoint is
enough to measure the client-visible outage window, and it keeps the
experiment free of a Python AMQP dependency.

Usage: publisher.py <seconds> <user:password>

Every attempt is recorded with its timestamp and its outcome, so the outage
window is the gap between the last success before the fault and the first
success after it.
"""
import json
import sys
import time
import urllib.error
import urllib.request

API = "http://localhost:15672/api"
VHOST = "orders"
EXCHANGE = "orders-service"
ROUTING_KEY = "order.created"


def publish(n, auth):
    body = json.dumps({
        "properties": {"delivery_mode": 2},
        "routing_key": ROUTING_KEY,
        "payload": f"message-{n}",
        "payload_encoding": "string",
    }).encode()
    req = urllib.request.Request(
        f"{API}/exchanges/{VHOST}/{EXCHANGE}/publish",
        data=body, method="POST",
        headers={"Content-Type": "application/json"},
    )
    import base64
    req.add_header("Authorization", "Basic " +
                   base64.b64encode(auth.encode()).decode())
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.load(resp).get("routed", False)


def main():
    duration = float(sys.argv[1]) if len(sys.argv) > 1 else 60.0
    # The operator generated these and wrote them into a Kubernetes Secret.
    # Publishing with them proves the credentials the operator issues really
    # work, which ties the operator back to the broker it provisioned.
    auth = sys.argv[2]
    interval = 0.2
    out = []
    end = time.time() + duration
    n = 0
    while time.time() < end:
        n += 1
        t = time.time()
        try:
            ok = publish(n, auth)
            out.append({"t": t, "ok": bool(ok)})
        except Exception as exc:  # noqa: BLE001 - any failure is a failed publish
            out.append({"t": t, "ok": False, "error": type(exc).__name__})
        time.sleep(max(0.0, interval - (time.time() - t)))

    ok = sum(1 for r in out if r["ok"])
    print(json.dumps({
        "attempts": len(out),
        "ok": ok,
        "failed": len(out) - ok,
        "samples": out,
    }))


if __name__ == "__main__":
    main()
