#!/usr/bin/env python3
"""Smoke-test a local image with disposable config/data; never read runtime config or .env files."""

import http.client
import json
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

import yaml

ROOT = Path(__file__).resolve().parents[1]


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def wait_http(container, path, expected):
    port = json.loads(docker("inspect", container))[0]["NetworkSettings"]["Ports"]["8080/tcp"][0]["HostPort"]
    url = f"http://127.0.0.1:{port}{path}"
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        if docker("inspect", "--format", "{{.State.Running}}", container) != "true":
            raise RuntimeError("Smoke-test container exited before becoming ready")
        try:
            with urllib.request.urlopen(url, timeout=3) as response:
                body = response.read().decode()
                if response.status == 200 and expected.lower() in body.lower():
                    return
        except (urllib.error.URLError, OSError, http.client.HTTPException):
            # Docker may accept/reset connections before the application binds.
            # Retry only until the readiness deadline; an exited container fails above.
            pass
        time.sleep(1)
    raise RuntimeError(f"Timed out checking {path}")


def main(image):
    suffix = secrets.token_hex(6)
    container = f"xmall-smoke-{suffix}"
    volume = f"xmall-smoke-db-{suffix}"
    docker("volume", "create", volume)
    try:
        with tempfile.TemporaryDirectory(prefix="xmall-docker-smoke-") as temp:
            # Only the public template is read. Test secrets live in a disposable file.
            config = yaml.safe_load((ROOT / "config.yml.example").read_text())
            for section, key in [("app", "secret_key"), ("jwt", "secret"), ("user_jwt", "secret")]:
                config[section][key] = secrets.token_hex(32)
            config["server"]["host"] = "0.0.0.0"
            config["server"]["port"] = "8080"
            config["web"]["admin_path"] = "/smoke-admin"
            config["update"]["repository"] = ""
            config["redis"]["enabled"] = False
            config["queue"]["enabled"] = False
            config["bootstrap"]["default_admin_username"] = "smoke-admin"
            config["bootstrap"]["default_admin_password"] = "Sm0ke!" + secrets.token_hex(24)
            path = Path(temp) / "smoke-config.yml"
            path.write_text(yaml.safe_dump(config))
            path.chmod(0o600)
            for attempt in range(2):
                docker("run", "-d", "--name", container, "-p", "127.0.0.1::8080",
                       "--mount", f"type=bind,src={path},dst=/app/config.yml,readonly",
                       "--mount", f"type=volume,src={volume},dst=/app/db", image)
                wait_http(container, "/health", "")
                wait_http(container, "/", "<html")
                wait_http(container, "/smoke-admin/", "<html")
                if attempt == 0:
                    docker("exec", container, "touch", "/app/db/.smoke-persistence")
                else:
                    docker("exec", container, "test", "-f", "/app/db/.smoke-persistence")
                docker("exec", container, "test", "-s", "/app/db/xmall.db")
                docker("exec", container, "test", "-s", "/app/LICENSE")
                docker("stop", "--time", "20", container)
                exit_code = docker("inspect", "--format", "{{.State.ExitCode}}", container)
                if exit_code != "0":
                    raise RuntimeError(f"Unclean shutdown: exit {exit_code}")
                docker("rm", container)
                print(f"Smoke pass {attempt + 1}: health, user/admin SPA, persistent SQLite and clean shutdown")
    except Exception:
        # Only disposable smoke-test logs; redact every generated credential
        # before emitting diagnostics to this public repository's Actions log.
        result = subprocess.run(["docker", "logs", container], capture_output=True, text=True)
        logs = result.stdout + result.stderr
        if "config" in locals():
            for section, key in [("app", "secret_key"), ("jwt", "secret"), ("user_jwt", "secret"), ("bootstrap", "default_admin_password")]:
                logs = logs.replace(config[section][key], "[REDACTED]")
        print(logs[-16000:], file=sys.stderr)
        raise
    finally:
        # Never inspect production configuration or containers.
        subprocess.run(["docker", "rm", "-f", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(["docker", "volume", "rm", "-f", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("Usage: docker-smoke.py IMAGE")
    main(sys.argv[1])
