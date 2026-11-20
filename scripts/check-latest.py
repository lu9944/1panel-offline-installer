#!/usr/bin/env python3
"""Resolve the latest upstream versions and decide whether a build is needed."""

from __future__ import annotations

import importlib.util
import json
import os
import shutil
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BUILD_SCRIPT = REPO_ROOT / "scripts" / "build-offline-package.py"


def load_build_module():
    spec = importlib.util.spec_from_file_location("build_offline_package", BUILD_SCRIPT)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load build script")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def github_json(url: str, token: str) -> dict:
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "1panel-offline-installer"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(req, timeout=30) as response:
        return json.loads(response.read().decode("utf-8"))


def release_fingerprint(repo: str, tag: str, token: str) -> tuple[bool, str]:
    if not repo:
        return False, ""
    try:
        release = github_json(f"https://api.github.com/repos/{repo}/releases/tags/{tag}", token)
    except urllib.error.HTTPError as exc:
        if exc.code == 404:
            return False, ""
        raise
    body = release.get("body") or ""
    marker = "<!-- offline-fingerprint:"
    if marker in body:
        start = body.index(marker) + len(marker)
        end = body.find("-->", start)
        if end > start:
            return True, body[start:end].strip()
    return True, ""


def emit(key: str, value: str) -> None:
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as handle:
            handle.write(f"{key}={value}\n")
    print(f"{key}={value}")


def main() -> int:
    build = load_build_module()
    force = os.environ.get("FORCE_REBUILD", "").lower() in ("1", "true", "yes")
    version = build.resolve_panel_version(os.environ.get("REQUESTED_VERSION", "latest"))
    compose_version = build.resolve_compose_version()
    docker_amd64 = build.resolve_docker_version("amd64")
    docker_arm64 = build.resolve_docker_version("arm64")
    docker_version = docker_amd64 if docker_amd64 == docker_arm64 else min([docker_amd64, docker_arm64], key=build.version_key)

    work = Path(tempfile.mkdtemp(prefix="1panel-offline-check-", dir=os.environ.get("RUNNER_TEMP") or None))
    try:
        appstore, commit = build.clone_appstore(work)
        versions = build.resolve_app_versions(appstore, {})
    finally:
        shutil.rmtree(work, ignore_errors=True)

    meta = {
        "bundleVersion": version,
        "apps": [{"key": key, "version": versions[key]} for key in build.APP_KEYS],
    }
    fp = build.fingerprint(meta, docker_version, compose_version, commit)
    tag = f"offline-{version}"
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    exists, existing_fp = release_fingerprint(repo, tag, os.environ.get("GITHUB_TOKEN", ""))
    should_build = force or not exists or existing_fp != fp

    emit("version", version)
    emit("tag", tag)
    emit("apps", ",".join(f"{key}:{versions[key]}" for key in build.APP_KEYS))
    emit("mysql_version", versions["mysql"])
    emit("redis_version", versions["redis"])
    emit("openresty_version", versions["openresty"])
    emit("docker_version", docker_version)
    emit("compose_version", compose_version)
    emit("appstore_commit", commit)
    emit("fingerprint", fp)
    emit("release_exists", "true" if exists else "false")
    emit("existing_fingerprint", existing_fp)
    emit("should_build", "true" if should_build else "false")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except SystemExit:
        raise
    except Exception as exc:  # noqa: BLE001
        print(f"ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
