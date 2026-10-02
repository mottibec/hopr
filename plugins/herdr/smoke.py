#!/usr/bin/env python3
"""Exercise the standard Hopr plugin and prefix+M in stock Herdr; no real agents."""
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import time


binary = Path(sys.argv[1] if len(sys.argv) > 1 else "~/.local/bin/herdr").expanduser().resolve()
hopr_binary = Path(sys.argv[2] if len(sys.argv) > 2 else "bin/hopr").resolve()
plugin = Path(__file__).resolve().parent
server_binary = binary
root = Path(tempfile.mkdtemp(prefix="hopr-popup-", dir="/private/tmp"))
config = root / "config" / "herdr"
config.mkdir(parents=True)
sock = root / "api.sock"
capture = root / "capture.json"
fixture = root / "popup.py"
fixture.write_text(
    f"#!{sys.executable}\n"
    "import json, os, sys\n"
    "if sys.argv[1:] == ['menu', '--herdr-plugin']:\n"
    f" with open({str(capture)!r}, 'w') as f:\n"
    "  json.dump({k: os.environ.get(k) for k in "
    "['HOPR_HERDR_SELECTION','HERDR_PLUGIN_ID','HERDR_PLUGIN_ENTRYPOINT_ID','HERDR_SOCKET_PATH','HOPR_CONFIG']}, f)\n"
    f"os.execv({str(hopr_binary)!r}, [{str(hopr_binary)!r}, *sys.argv[1:]])\n"
)
fixture.chmod(0o700)
(config / "config.toml").write_text(
    "onboarding = false\n[update]\nversion_check = false\nmanifest_check = false\n"
    '[terminal]\ndefault_shell = "/bin/sh"\nshell_mode = "non_login"\n'
    + (plugin.parent.parent / "examples" / "herdr-plugin.toml").read_text()
)
hopr_config = root / "hopr.json"
hopr_config.write_text(json.dumps({
    "host_id": "fixture", "backend": "herdr", "default_server": "fixture",
    "servers": {"fixture": str(sock)}, "hosts": {}, "projects": {},
    "state_dir": str(root / "hopr-state"), "workspace_root": str(root / "workspaces"),
    "codex_home": str(root / "codex"), "claude_home": str(root / "claude"),
    "executables": {"herdr": str(binary)},
}))
hopr_config.chmod(0o600)
env = {k: v for k, v in os.environ.items() if not k.startswith("HERDR_")}
env.update(
    XDG_CONFIG_HOME=str(root / "config"), XDG_STATE_HOME=str(root / "state"),
    XDG_RUNTIME_DIR=str(root), HERDR_SOCKET_PATH=str(sock), HERDR_DISABLE_SOUND="1",
    SHELL="/bin/sh", TERM="xterm-256color", HOPR_CONFIG=str(hopr_config), HOPR_BIN=str(fixture),
)
subprocess.run([str(binary), "plugin", "link", str(plugin)], env=env, check=True, capture_output=True)
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


def rendered_text():
    # Herdr redraws cells with cursor escapes, including between words.
    text = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", bytes(screen))
    return b"".join(text.split())


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
    # Use ordinary terminal bytes, with no Option/Alt or global shortcuts.
    os.write(master, b"\x02")
    drain(0.2)
    os.write(master, b"m")
    wait(lambda: capture.exists() and capture.read_bytes().endswith(b"}"),
         "plugin action opening Hopr popup")
    result = json.loads(capture.read_text())
    selection = json.loads(result["HOPR_HERDR_SELECTION"])
    assert selection["pane"] == pane, result
    assert selection["terminal"] == created["root_pane"]["terminal_id"], result
    assert selection["workspace"], result
    assert result["HERDR_SOCKET_PATH"] == str(sock), result
    assert result["HOPR_CONFIG"] == str(hopr_config), result
    assert result["HERDR_PLUGIN_ID"] == "hopr", result
    assert result["HERDR_PLUGIN_ENTRYPOINT_ID"] == "move", result
    wait(lambda: b"PressEntertoclose:" in rendered_text(), "real Hopr menu error on fixture shell")
    assert f"agenttarget{pane}notfound".encode() in rendered_text(), rendered_text()
    os.write(master, b"\r")
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
