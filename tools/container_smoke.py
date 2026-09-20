#!/usr/bin/env python3
"""Exercise a supplied exact image without provider credentials or infrastructure."""
import argparse
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument('image')
args = parser.parse_args()
name = 'labcentral-image-smoke-' + uuid.uuid4().hex[:12]
def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()
def get(url):
    with urllib.request.urlopen(url, timeout=3) as response:
        return response.read()
with tempfile.TemporaryDirectory(prefix=name) as temporary:
    root = Path(temporary)
    uid, gid = (os.getuid(), os.getgid()) if os.getuid() else (65532, 65532)
    if os.getuid() == 0:
        os.chown(root, uid, gid)
    ports = []
    for _ in range(3):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            ports.append(sock.getsockname()[1])
    product, health, assets = ports
    origin = f'http://127.0.0.1:{product}'
    ready = f'http://127.0.0.1:{health}/ready'
    def wait():
        for _ in range(30):
            try:
                assert json.loads(get(ready))['status'] == 'ready'
                assert json.loads(get(origin + '/api/v1/server-info'))['apiVersion'] == 'v1'
                return
            except Exception:
                time.sleep(1)
        raise RuntimeError(docker('logs', name))
    try:
        docker('run', '-d', '--name', name, '--network', 'host', '--read-only',
               '--user', f'{uid}:{gid}', '--cap-drop', 'ALL',
               '--security-opt', 'no-new-privileges', '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777',
               '--mount', f'type=bind,src={root},dst=/var/lib/labcentral',
               '-e', f'LABCENTRAL_HEALTHCHECK_URL={ready}', args.image,
               f'--listen=127.0.0.1:{product}', f'--observability-listen=127.0.0.1:{health}',
               '--data-dir=/var/lib/labcentral', '--auth-mode=none', '--docker-plugin=',
               '--installation=/opt/labcentral/features/standard.installation.json',
               f'--extension-asset-listen=127.0.0.1:{assets}', f'--extension-asset-origin=http://localhost:{assets}')
        wait()
        catalog = json.loads(get(origin + '/api/v1/extensions/browser'))['items']
        assert {v['title'] for v in catalog} == {'Template Library', 'Teams'}
        for view in catalog:
            assert b'<html' in get(view['url'])
        assert b'<!doctype html' in get(origin).lower()
        docker('exec', name, '/usr/local/bin/labd', 'healthcheck')
        # Exercise creation as the actual non-root identity. This source process
        # creates its private state directory before refusing absent registration;
        # it performs no repository requests or control-plane work.
        temporary_probe = subprocess.run(
            ['docker', 'exec', name, '/usr/local/bin/lab-source-github',
             '--state=/tmp/labcentral-temp-smoke'], capture_output=True, text=True, timeout=10)
        assert temporary_probe.returncode == 1 and temporary_probe.stderr.strip() == 'GitHub source service stopped', temporary_probe.stderr
        # Also exercise regular-file writes using the already packaged recovery
        # command. These unused test keys disappear with the tmpfs/container.
        docker('exec', name, '/usr/local/bin/labcentral-recovery', 'keygen',
               '--directory=/tmp/labcentral-temp-smoke', '--key-id=temp-smoke')
        docker('stop', name)
        with sqlite3.connect(root / 'labcentral.db') as db:
            assert db.execute('pragma integrity_check').fetchone()[0] == 'ok'
            schema = db.execute('select name, sql from sqlite_master order by name').fetchall()
            assert len(schema) > 20
        docker('start', name)
        wait()
        assert json.loads(get(origin + '/api/v1/extensions/browser'))['items'] == catalog
        docker('stop', name)
        with sqlite3.connect(root / 'labcentral.db') as db:
            assert db.execute('select name, sql from sqlite_master order by name').fetchall() == schema
        print('PASS: exact image startup, embedded UI, independent assets, non-root read-only execution, writable temporary storage, healthcheck and persisted database restart')
    finally:
        subprocess.run(['docker', 'rm', '-fv', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
