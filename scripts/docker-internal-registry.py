#!/usr/bin/env python3
"""Verify the exact private registry archive; credentials exist only in temp directories."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


def require_denied(result):
    # A generic failure (DNS, TLS, timeout, 404, malformed response) is not proof
    # of privacy. Require an explicit registry HTTP authentication/authorization status.
    if result.returncode == 0:
        raise RuntimeError("Anonymous registry read succeeded")
    if not re.search(r"(?:status(?: code)?:?\s*|HTTP(?:/\d(?:\.\d)?)?\s+)(?:401|403)\b|\b(?:401\s*\(?Unauthorized|403\s*\(?Forbidden)\b",
                     result.stderr, re.IGNORECASE):
        raise RuntimeError("Anonymous read did not return an explicit HTTP 401/403: " + result.stderr)


def check_manifest(raw, local):
    if raw != local:
        raise RuntimeError("Registry index differs from the tested OCI archive")
    manifest = json.loads(raw)
    platforms = {(item.get("platform", {}).get("os"), item.get("platform", {}).get("architecture"))
                 for item in manifest.get("manifests", [])}
    if not {("linux", "amd64"), ("linux", "arm64")} <= platforms:
        raise RuntimeError("Registry manifest must contain both linux/amd64 and linux/arm64")
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def main(image, tag, authfile, archive):
    local = subprocess.check_output(["skopeo", "inspect", "--raw", f"oci-archive:{archive}"])
    remote = subprocess.check_output(["skopeo", "inspect", "--raw", "--authfile", authfile,
                                      f"docker://{image}:{tag}"])
    digest = check_manifest(remote, local)
    reference = f"{image}@{digest}"
    with tempfile.TemporaryDirectory(prefix="xmall-anonymous-") as temp:
        empty_auth = Path(temp) / "config.json"
        empty_auth.write_text('{"auths":{}}')
        env = {**os.environ, "HOME": temp, "DOCKER_CONFIG": temp, "XDG_RUNTIME_DIR": temp,
               "XDG_CONFIG_HOME": temp, "REGISTRY_AUTH_FILE": str(empty_auth)}
        for target in [f"{image}:{tag}", reference]:
            result = subprocess.run(["skopeo", "inspect", "--raw", "--authfile", str(empty_auth),
                                     f"docker://{target}"], env=env, capture_output=True, text=True)
            require_denied(result)
    with tempfile.TemporaryDirectory(prefix="xmall-authenticated-") as temp:
        config = Path(temp) / "config.json"
        shutil.copyfile(authfile, config)
        config.chmod(0o600)
        for arch in ["amd64", "arm64"]:
            env = {**os.environ, "DOCKER_CONFIG": temp, "DOCKER_DEFAULT_PLATFORM": f"linux/{arch}"}
            subprocess.run(["docker", "pull", "--platform", f"linux/{arch}", reference], env=env, check=True)
            actual = subprocess.check_output(["docker", "image", "inspect", "--format", "{{.Architecture}}", reference], env=env, text=True).strip()
            if actual != arch:
                raise RuntimeError(f"Expected {arch}, pulled {actual}")
            subprocess.run([sys.executable, str(Path(__file__).with_name("docker-smoke.py")), reference], env=env, check=True)
    print(f"Verified private registry index {digest}, anonymous denial, and both architecture runtime smokes")


if __name__ == "__main__":
    if len(sys.argv) != 5:
        raise SystemExit("Usage: docker-internal-registry.py IMAGE TAG AUTHFILE OCI_ARCHIVE")
    main(*sys.argv[1:])
