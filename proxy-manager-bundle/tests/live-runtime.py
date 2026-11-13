#!/usr/bin/env python3
"""Optional real-daemon check. Uses only temporary files and loopback ports.
Usage: python3 tests/live-runtime.py /path/to/xray /path/to/hysteria /path/to/vpn-manager
Requires free TCP ports 10085, 10086, 18081 and openssl, curl, jq, bash.
"""
import contextlib
import http.client
import json
import os
from pathlib import Path
import socket
import socketserver
import struct
import subprocess
import sys
import tempfile
import threading
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
XRAY, HYSTERIA, MANAGER = map(lambda s: str(Path(s).resolve()), sys.argv[1:])


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def wait_tcp(p):
    for _ in range(100):
        try:
            with socket.create_connection(('127.0.0.1', p), .1):
                return
        except OSError:
            time.sleep(.05)
    raise RuntimeError(f'listener {p} did not start')


def recv(sock, n):
    data = b''
    while len(data) < n:
        part = sock.recv(n-len(data))
        if not part:
            raise RuntimeError('connection interrupted')
        data += part
    return data


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        while True:
            b = self.request.recv(4096)
            if not b:
                break
            self.request.sendall(b)


def socks(p, dest):
    sock = socket.create_connection(('127.0.0.1', p), 5)
    sock.sendall(b'\x05\x01\x00')
    assert recv(sock, 2) == b'\x05\x00'
    sock.sendall(b'\x05\x01\x00\x01\x7f\x00\x00\x01' + struct.pack('!H', dest))
    reply = recv(sock, 4)
    assert reply[1] == 0, reply
    recv(sock, 6 if reply[3] == 1 else 18)
    return sock


with tempfile.TemporaryDirectory(prefix='live-vpn-') as tmp, contextlib.ExitStack() as stack:
    d = Path(tmp)
    # Refuse to collide with any existing local installation.
    for p in (10085, 10086, 18081):
        probe = socket.socket(); probe.bind(('127.0.0.1', p)); probe.close()
    echo = socketserver.ThreadingTCPServer(('127.0.0.1', 0), Echo)
    echo.daemon_threads = True
    stack.callback(echo.server_close)
    threading.Thread(target=echo.serve_forever, daemon=True).start()
    stack.callback(echo.shutdown)
    ep = echo.server_address[1]
    xp, hp, sp = port(), port(), port()
    alice, bob, charlie = (str(uuid.uuid4()) for _ in range(3))
    keyout = subprocess.check_output([XRAY, 'x25519'], text=True)
    private = keyout.split('PrivateKey: ')[1].splitlines()[0]
    accounts = {'alice': {'name': 'alice', 'enabled': True, 'xray_uuid': alice, 'hysteria_password': 'old'},
                'bob': {'name': 'bob', 'enabled': True, 'xray_uuid': bob, 'hysteria_password': 'unchanged'}}
    state = {'version': 2, 'accounts': accounts, 'server': {
        'port': xp, 'public_ip': '127.0.0.1', 'xray_sni': 'localhost', 'hy_sni': 'localhost',
        'reality_private_key': private, 'reality_public_key': 'unused', 'reality_short_id': '1234',
        'sentinel_uuid': str(uuid.uuid4()), 'sentinel_hy_password': 'sentinel'}}
    for name in ('state', 'hy', 'work'):
        (d/name).mkdir()
    statefile = d/'state/state.json'
    authfile = d/'hy/auth-users.json'
    statefile.write_text(json.dumps(state))
    authfile.write_text(json.dumps({'alice': 'old', 'bob': 'unchanged'}))
    (d/'state/live-users').touch()
    (d/'hy/config.yaml').write_text('url: http://127.0.0.1:18081/auth\n')
    cfg = {'log': {'loglevel': 'debug'}, 'api': {'tag': 'api', 'listen': '127.0.0.1:10085', 'services': ['HandlerService']},
           'inbounds': [{'tag': 'vless-reality', 'listen': '127.0.0.1', 'port': xp, 'protocol': 'vless',
                         'settings': {'decryption': 'none', 'clients': [{'email': 'alice', 'id': alice}, {'email': 'bob', 'id': bob}]}}],
           'outbounds': [{'protocol': 'freedom', 'settings': {'finalRules': [{'action': 'allow', 'ip': ['127.0.0.1/32']}]}}]}
    (d/'xray-start.json').write_text(json.dumps(cfg))
    (d/'xray.json').write_text(json.dumps(cfg))
    subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                    '-subj', '/CN=localhost', '-keyout', str(d/'key.pem'), '-out', str(d/'cert.pem')],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    (d/'hy-server.yaml').write_text(f'''listen: 127.0.0.1:{hp}
tls:
  cert: {d}/cert.pem
  key: {d}/key.pem
auth:
  type: http
  http:
    url: http://127.0.0.1:18081/auth
trafficStats:
  listen: 127.0.0.1:10086
''')
    (d/'hy-client.yaml').write_text(f'''server: 127.0.0.1:{hp}
auth: bob:unchanged
tls:
  sni: localhost
  insecure: true
socks5:
  listen: 127.0.0.1:{sp}
''')
    processes = []
    def start(args):
        log = stack.enter_context(open(d/f'process-{len(processes)}.log', 'w+'))
        proc = subprocess.Popen(args, stdout=log, stderr=log)
        processes.append(proc)
        def cleanup():
            if sys.exc_info()[0] is not None or (proc.poll() is not None and proc.returncode != 0):
                log.flush(); log.seek(0); print(log.read(), file=sys.stderr)
            proc.terminate()
            try: proc.wait(5)
            except subprocess.TimeoutExpired: proc.kill(); proc.wait()
        stack.callback(cleanup)
    start([XRAY, 'run', '-config', str(d/'xray-start.json')]); wait_tcp(10085)
    start([MANAGER, 'live-auth', '--users', str(authfile)]); wait_tcp(18081)
    start([HYSTERIA, 'server', '-c', str(d/'hy-server.yaml')]); wait_tcp(10086)
    start([HYSTERIA, 'client', '-c', str(d/'hy-client.yaml')]); wait_tcp(sp)
    # Exercise a persistent, authenticated VLESS TCP connection for Bob.
    vs = stack.enter_context(socket.create_connection(('127.0.0.1', xp), 5))
    vs.sendall(b'\x00'+uuid.UUID(bob).bytes+b'\x00\x01'+struct.pack('!H', ep)+b'\x01\x7f\x00\x00\x01'+b'first')
    assert recv(vs, 2) == b'\x00\x00'
    assert recv(vs, 5) == b'first'
    hs = stack.enter_context(socks(sp, ep))
    hs.sendall(b'first'); assert recv(hs, 5) == b'first'
    (d/'xray').write_text('#!/bin/bash\n"'+XRAY+'" "$@" 2>&1 | tee -a "'+str(d/'api.log')+'"\nexit ${PIPESTATUS[0]}\n')
    (d/'xray').chmod(0o700)
    (d/'sync').write_text('#!/bin/sh\nexit 0\n'); (d/'sync').chmod(0o700)
    core = (ROOT/'proxy-manager.sh').read_text()
    lib = core[core.index('valid_account_name()'):core.index('# ---------- main ----------')]
    (d/'functions.sh').write_text(lib)
    script = '''set -Eeuo pipefail
source "$TEST_DIR/functions.sh"
STATE_DIR="$TEST_DIR/state"; STATE_FILE="$STATE_DIR/state.json"
HY_DIR="$TEST_DIR/hy"; HY_CONF="$HY_DIR/config.yaml"
WORK="$TEST_DIR/work"; XRAY_CONF="$TEST_DIR/xray.json"
VPNM_BIN="$TEST_DIR/sync"; NGINX_DEST_PORT=8443
log(){ :; }; warn(){ :; }; die(){ echo "$*" >&2; exit 1; }
chown(){ :; }
systemctl(){ [[ $1 == show ]] || { echo 'UNEXPECTED SERVICE ACTION' >&2; return 90; }; }
load_state
apply_live_account "$1"
'''
    (d/'apply.sh').write_text(script)
    env = dict(os.environ, TEST_DIR=tmp, PATH=tmp+':'+os.environ['PATH'])
    errors = []
    stop = threading.Event()
    ticks = [0]
    def continuous():
        try:
            while not stop.is_set():
                for conn in (vs, hs):
                    conn.sendall(b'uninterrupted'); assert recv(conn, 13) == b'uninterrupted'
                ticks[0] += 1
                time.sleep(.01)
        except Exception as e: errors.append(e)
    worker = threading.Thread(target=continuous); worker.start()
    def apply(name):
        statefile.with_suffix('.tmp').write_text(json.dumps(state))
        statefile.with_suffix('.tmp').replace(statefile)
        result = subprocess.run(['bash', str(d/'apply.sh'), name], env=env)
        if result.returncode:
            print((d/'api.log').read_text(), file=sys.stderr)
            result.check_returncode()
        assert not errors, errors
        assert all(p.poll() is None for p in processes), 'daemon exited'
    def auth(payload):
        c = http.client.HTTPConnection('127.0.0.1',18081,timeout=3)
        c.request('POST','/auth',json.dumps({'auth':payload}),{'Content-Type':'application/json'})
        result=json.loads(c.getresponse().read())['ok'];c.close();return result
    try:
        accounts['alice']['xray_uuid'] = str(uuid.uuid4()); accounts['alice']['hysteria_password']='new'
        apply('alice'); assert not auth('alice:old') and auth('alice:new') and auth('bob:unchanged')
        live = subprocess.check_output([XRAY,'api','inbounduser','--server=127.0.0.1:10085','-tag=vless-reality'],text=True)
        assert accounts['alice']['xray_uuid'] in live and alice not in live and bob in live
        accounts['alice']['enabled']=False; apply('alice'); assert not auth('alice:new')
        accounts['alice']['enabled']=True; apply('alice'); assert auth('alice:new')
        accounts['charlie']={'name':'charlie','enabled':True,'xray_uuid':charlie,'hysteria_password':'added'}
        apply('charlie'); assert auth('charlie:added')
        del accounts['charlie']; apply('charlie'); assert not auth('charlie:added')
        del accounts['alice']; apply('alice'); assert not auth('alice:new')
    finally:
        stop.set(); worker.join(6)
    assert not errors and ticks[0] > 0, (errors,ticks)
    print(f'Real Xray/Hysteria: {ticks[0]} continuous echo rounds for Bob survived rotation, disable, enable, add and delete; daemon PIDs unchanged.')
