#!/usr/bin/env python3
"""Exercise the patched right-click UI in a disposable server; no real agents."""
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import shlex
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time


binary = Path(sys.argv[1] if len(sys.argv) > 1 else "bin/herdr-hopr").resolve()
server_binary = Path(sys.argv[2]).resolve() if len(sys.argv) > 2 else binary
root = Path(tempfile.mkdtemp(prefix="hopr-popup-", dir="/private/tmp"))
config = root / "config" / "herdr"
config.mkdir(parents=True)
sock = root / "api.sock"
capture = root / "capture.json"
fixture = root / "popup.py"
fixture.write_text(
    "import json, os\n"
    f"with open({str(capture)!r}, 'w') as f:\n"
    " json.dump({k: os.environ.get(k) for k in "
    "['HERDR_ACTIVE_PANE_ID','HERDR_ACTIVE_WORKSPACE_ID','HERDR_SOCKET_PATH']}, f)\n"
    "input('Disposable Hopr popup; Enter closes: ')\n"
)
command = shlex.join([sys.executable, str(fixture)])
(config / "config.toml").write_text(
    "onboarding = false\n[update]\nversion_check = false\nmanifest_check = false\n"
    '[terminal]\ndefault_shell = "/bin/sh"\nshell_mode = "non_login"\n'
    '[[keys.command]]\nkey = "prefix+alt+m"\ntype = "popup"\n'
    f"command = {json.dumps(command)}\n"
    'description = "Move agent to another Mac"\n'
)
env = {k: v for k, v in os.environ.items() if not k.startswith("HERDR_")}
env.update(
    XDG_CONFIG_HOME=str(root / "config"), XDG_STATE_HOME=str(root / "state"),
    XDG_RUNTIME_DIR=str(root), HERDR_SOCKET_PATH=str(sock), HERDR_DISABLE_SOUND="1",
    SHELL="/bin/sh", TERM="xterm-256color",
)
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 106, 0, 0))
server_log = (root / "server.log").open("wb")
server = subprocess.Popen([str(server_binary), "server"], cwd=root, env=env,
                          stdin=subprocess.DEVNULL, stdout=server_log, stderr=server_log)
client = None
screen = bytearray()


def drain(seconds=0.1):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if select.select([master], [], [], 0.02)[0]:
            try:
                screen.extend(os.read(master, 65536))
            except OSError:
                break


def wait(check, description, seconds=15):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if check():
            return
        drain()
    raise AssertionError(f"Timed out: {description}; fixture retained at {root}")


def api(method, params):
    with socket.socket(socket.AF_UNIX) as s:
        s.settimeout(5)
        s.connect(str(sock))
        s.sendall(json.dumps(dict(id="smoke", method=method, params=params)).encode() + b"\n")
        result = json.loads(s.makefile("rb").readline())
        if "error" in result:
            raise AssertionError(result)
        return result["result"]


try:
    wait(sock.exists, "disposable server")
    created = api("workspace.create", {"cwd": str(root), "label": "Hopr popup fixture", "focus": True})
    pane = created["root_pane"]["pane_id"]
    client = subprocess.Popen([str(binary), "client"], cwd=root, env=env,
                              stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
    os.close(slave)
    wait(lambda: b"Hopr popup fixture" in screen, "client frame")
    # Right-click the terminal area, then select the last menu item.
    screen.clear()
    os.write(master, b"\x1b[<2;70;10M\x1b[<2;70;10m")
    wait(lambda: b"Move to another Mac" in screen, "right-click action")
    os.write(master, b"\x1b[B" * 20 + b"\r")
    wait(capture.exists, "configured popup launch")
    result = json.loads(capture.read_text())
    assert result["HERDR_ACTIVE_PANE_ID"] == pane, result
    assert result["HERDR_SOCKET_PATH"] == str(sock), result
    assert result["HERDR_ACTIVE_WORKSPACE_ID"], result
    print(json.dumps(dict(ok=True, fixture=str(root), popup_context=result), indent=2))
finally:
    (root / "client.ansi").write_bytes(screen)
    if client is not None:
        client.terminate()
        try:
            client.wait(timeout=5)
        except subprocess.TimeoutExpired:
            client.kill()
            client.wait()
    # Stop only the disposable server at the unique socket owned by this test.
    if sock.exists() and server.poll() is None:
        try:
            api("server.stop", {})
        except (OSError, AssertionError, ValueError):
            pass
    if server.poll() is None:
        server.terminate()
    try:
        server.wait(timeout=5)
    except subprocess.TimeoutExpired:
        server.kill()
        server.wait()
    os.close(master)
    server_log.close()
