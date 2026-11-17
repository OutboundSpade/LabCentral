#!/usr/bin/env python3
"""Export completed rounds from a Quotient scoring engine instance to LabCentral ScoreSnapshot v1 format.

Runs inside the hidden scorer VM alongside Quotient (https://github.com/dbaseqp/Quotient).
Queries Quotient's PostgreSQL database or REST API for the latest completed round
and atomically writes /var/lib/scoring/labcentral.json for serve.py.

Usage:
    python3 export_quotient.py --output /var/lib/scoring/labcentral.json --interval 15
"""

import argparse
import datetime
import json
import os
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


def fetch_from_api(api_url: str, token: str = "") -> dict:
    """Fetch latest round status from Quotient HTTP API."""
    req = urllib.request.Request(f"{api_url.rstrip('/')}/api/v1/scoreboard")
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    with urllib.request.urlopen(req, timeout=5) as response:
        return json.loads(response.read().decode("utf-8"))


def fetch_from_postgres(conn_str: str) -> dict:
    """Query PostgreSQL database for latest completed round and service checks."""
    try:
        import psycopg2
    except ImportError:
        raise RuntimeError("psycopg2 package is required for direct PostgreSQL querying")

    with psycopg2.connect(conn_str) as conn:
        with conn.cursor() as cur:
            # Query the latest completed round
            cur.execute("SELECT round_number, completed_at FROM rounds WHERE status = 'completed' ORDER BY round_number DESC LIMIT 1;")
            round_row = cur.fetchone()
            if not round_row:
                return {}

            round_num, completed_at = round_row

            # Query service check statuses for this round
            cur.execute("""
                SELECT s.slug, s.name, c.status, s.points
                FROM service_checks c
                JOIN services s ON s.id = c.service_id
                WHERE c.round_number = %s;
            """, (round_num,))

            checks = cur.fetchall()
            return {
                "round": round_num,
                "completed_at": completed_at.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "checks": checks,
            }


def build_snapshot(raw_data: dict, service_definitions: list = None) -> dict:
    """Translate Quotient check data into LabCentral ScoreSnapshot v1 format."""
    round_num = raw_data.get("round", 1)
    timestamp = raw_data.get("completed_at") or datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    services = []
    checks = raw_data.get("checks", [])

    if checks:
        for check in checks:
            slug, name, status, points = check[0], check[1], check[2], check[3]
            state = "up" if status in ("passed", "success", "up", 0) else "down"
            earned = points if state == "up" else 0
            services.append({
                "id": slug.lower().replace("_", "-"),
                "name": name,
                "state": state,
                "earned": earned,
                "possible": points,
            })
    elif "services" in raw_data:
        for s in raw_data["services"]:
            slug = s.get("id", s.get("slug", "")).lower().replace("_", "-")
            name = s.get("name", slug)
            state = s.get("state", "unknown")
            earned = int(s.get("earned", 0))
            possible = int(s.get("possible", 0))
            services.append({
                "id": slug,
                "name": name,
                "state": state,
                "earned": earned,
                "possible": possible,
            })

    return {
        "version": 1,
        "round": round_num,
        "generatedAt": timestamp,
        "services": services,
    }


def write_atomic(snapshot: dict, target_file: Path) -> None:
    """Write snapshot atomically via tempfile rename on the same filesystem."""
    target_dir = target_file.parent
    target_dir.mkdir(parents=True, exist_ok=True)

    data = json.dumps(snapshot, indent=2).encode("utf-8")
    with tempfile.NamedTemporaryFile("wb", dir=target_dir, delete=False) as tf:
        tf.write(data)
        temp_path = Path(tf.name)

    os.replace(temp_path, target_file)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=Path("/var/lib/scoring/labcentral.json"),
                        help="Path to output JSON file served by serve.py")
    parser.add_argument("--api-url", type=str, default="",
                        help="Quotient REST API URL (e.g. http://127.0.0.1:8000)")
    parser.add_argument("--api-token", type=str, default="",
                        help="Optional API bearer token")
    parser.add_argument("--pg-conn", type=str, default="",
                        help="PostgreSQL connection string (e.g. postgresql://user:pass@127.0.0.1:5432/quotient)")
    parser.add_argument("--interval", type=int, default=15,
                        help="Poll interval in seconds (default: 15)")
    parser.add_argument("--once", action="store_true",
                        help="Run once and exit instead of polling continuously")

    args = parser.parse_args()

    if not args.api_url and not args.pg_conn:
        # Default to localhost Quotient API if neither specified
        args.api_url = "http://127.0.0.1:8000"

    while True:
        try:
            if args.pg_conn:
                data = fetch_from_postgres(args.pg_conn)
            else:
                data = fetch_from_api(args.api_url, args.api_token)

            if data:
                snapshot = build_snapshot(data)
                write_atomic(snapshot, args.output)
        except Exception as e:
            print(f"[export_quotient] Warning: {e}", file=sys.stderr)

        if args.once:
            break
        time.sleep(args.interval)


if __name__ == "__main__":
    main()
