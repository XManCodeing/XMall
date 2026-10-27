#!/usr/bin/env python3
"""Check release inputs; inspect tracked filenames, never read runtime config/env files."""

import argparse
import fnmatch
from pathlib import Path
import subprocess
import sys

import yaml

ROOT = Path(__file__).resolve().parents[1]


def check(skip_git=False):
    required = [
        "LICENSE", "README.md", "config.yml.example", "go.mod", "go.sum",
        "frontend/admin/package.json", "frontend/admin/pnpm-lock.yaml",
        "frontend/user/package.json", "frontend/user/pnpm-lock.yaml",
        "scripts/dujiao-next-manager.sh", "Dockerfile", ".goreleaser.yaml",
    ]
    for name in required:
        if not (ROOT / name).is_file():
            raise ValueError(f"Missing release input: {name}")

    # Read only the public template, never config.yml or any .env* file.
    config = yaml.safe_load((ROOT / "config.yml.example").read_text())
    for section, key in [
        ("app", "secret_key"), ("jwt", "secret"), ("user_jwt", "secret"),
        ("bootstrap", "default_admin_username"), ("bootstrap", "default_admin_password"),
        ("telegram_auth", "bot_token"), ("telegram_auth", "client_secret"),
        ("redis", "password"), ("queue", "password"), ("email", "password"),
        ("captcha", "turnstile"),
    ]:
        value = config[section][key]
        if section == "captcha":
            value = value["secret_key"]
        if value != "":
            raise ValueError(f"Public template must have empty {section}.{key}")
    if config["update"]["repository"] != "":
        raise ValueError("Public template must not enable an implicit update repository")
    release = yaml.safe_load((ROOT / ".goreleaser.yaml").read_text())
    files = release["archives"][0]["files"]
    for name in ["LICENSE", "README.md", "config.yml.example", "scripts/dujiao-next-manager.sh"]:
        if f"./{name}" not in files:
            raise ValueError(f"Release archive must include {name}")
    if "go mod tidy -diff" not in release["before"]["hooks"]:
        raise ValueError("Release must verify dependency consistency without rewriting files")
    if release["release"].get("draft") is not True:
        raise ValueError("Release must be created as a draft for inspection")
    dockerfile = (ROOT / "Dockerfile").read_text()
    if "COPY LICENSE /app/LICENSE" not in dockerfile:
        raise ValueError("Final Docker image must include LICENSE")

    if skip_git:
        print("Release inputs passed; Git tracked-file checks explicitly skipped.")
        return
    tracked = subprocess.check_output(
        ["git", "ls-files", "-z"], cwd=ROOT
    ).decode().split("\0")
    forbidden = []
    for name in filter(None, tracked):
        parts = Path(name).parts
        base = parts[-1]
        sensitive = (
            base.startswith(".env")
            or base == "config.yml"
            or (base.startswith("config.yml.") and name != "config.yml.example")
            or any(part in {"node_modules", "db", "logs", "uploads", "backups"} for part in parts)
            or any(fnmatch.fnmatch(base, pattern) for pattern in (
                "*.db", "*.db-wal", "*.db-shm", "*.sqlite", "*.sqlite3",
                "*.sql", "*.sql.gz", "*.log", "*.key", "*.p12", "*.pfx",
            ))
        )
        if sensitive:
            forbidden.append(name)
    if forbidden:
        raise ValueError("Remove sensitive/runtime paths from Git tracking: " + ", ".join(forbidden))
    for name in required:
        if name not in tracked:
            raise ValueError(f"Release input is not committed/tracked: {name}")
    print("Release inputs and tracked filenames passed (not a content/history secret scan).")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--skip-git", action="store_true", help="Local source archives only; never use in CI")
    args = parser.parse_args()
    try:
        check(args.skip_git)
    except (ValueError, KeyError, TypeError, OSError, subprocess.CalledProcessError) as exc:
        print(f"Release input check failed: {exc}", file=sys.stderr)
        sys.exit(1)
