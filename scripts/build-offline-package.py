#!/usr/bin/env python3
"""Build the 1Panel offline installer package.

The script is intended to run on a Linux builder with Docker, git, curl, tar,
gzip and Go installed. It is also the exact script used by GitHub Actions.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - exercised only on a broken builder
    print("ERROR: PyYAML is required: pip install -r scripts/requirements-build.txt", file=sys.stderr)
    raise

REPO_ROOT = Path(__file__).resolve().parent.parent
APPSTORE_REPO = "https://github.com/1Panel-dev/appstore.git"
APPSTORE_BRANCH = "dev"
APP_KEYS = ("mysql", "redis", "openresty")
PANEL_BASE_URLS = (
    "https://resource.1panel.pro/v2",
    "https://resource.fit2cloud.com/1panel/package/v2",
)
DOCKER_STATIC_BASE = "https://download.docker.com/linux/static/stable"
COMPOSE_RELEASES_API = "https://api.github.com/repos/docker/compose/releases/latest"
GITHUB_API = "https://api.github.com/repos/1Panel-dev/appstore/contents/apps"
USER_AGENT = "1panel-offline-installer-builder"


def log(message: str) -> None:
    print(f"==> {message}", flush=True)


def die(message: str) -> None:
    print(f"ERROR: {message}", file=sys.stderr, flush=True)
    raise SystemExit(1)


def run(cmd: list[str], *, cwd: Path | None = None, env: dict[str, str] | None = None, check: bool = True) -> subprocess.CompletedProcess:
    printable = " ".join(cmd)
    log(printable)
    result = subprocess.run(cmd, cwd=str(cwd) if cwd else None, env=env)
    if check and result.returncode != 0:
        die(f"command failed ({result.returncode}): {printable}")
    return result


def capture(cmd: list[str], *, cwd: Path | None = None) -> str:
    result = subprocess.run(cmd, cwd=str(cwd) if cwd else None, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if result.returncode != 0:
        die(f"command failed: {' '.join(cmd)}\n{result.stderr.strip()}")
    return result.stdout.strip()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(4 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def download(url: str, dest: Path, *, expected_sha256: str = "", headers: dict[str, str] | None = None, retries: int = 3) -> None:
    dest.parent.mkdir(parents=True, exist_ok=True)
    last_error = ""
    for attempt in range(1, retries + 1):
        try:
            log(f"下载 {url}")
            req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT, **(headers or {})})
            with urllib.request.urlopen(req, timeout=120) as response, dest.open("wb") as output:
                shutil.copyfileobj(response, output, length=1 << 20)
            if expected_sha256:
                actual = sha256_file(dest)
                if actual.lower() != expected_sha256.lower():
                    dest.unlink(missing_ok=True)
                    die(f"SHA256 mismatch for {url}: expected {expected_sha256}, got {actual}")
            return
        except SystemExit:
            raise
        except Exception as exc:  # noqa: BLE001
            last_error = str(exc)
            dest.unlink(missing_ok=True)
            log(f"下载失败（第 {attempt}/{retries} 次）: {exc}")
            time.sleep(2 * attempt)
    die(f"下载失败: {url}: {last_error}")


def fetch_text(url: str, *, headers: dict[str, str] | None = None, retries: int = 3) -> str:
    last_error = ""
    for attempt in range(1, retries + 1):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT, **(headers or {})})
            with urllib.request.urlopen(req, timeout=60) as response:
                return response.read().decode("utf-8", errors="replace")
        except Exception as exc:  # noqa: BLE001
            last_error = str(exc)
            time.sleep(2 * attempt)
    die(f"获取失败: {url}: {last_error}")


def github_headers() -> dict[str, str]:
    token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if token:
        return {"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json"}
    return {"Accept": "application/vnd.github+json"}


def normalize_version(value: str) -> str:
    value = value.strip()
    if value.startswith("v"):
        value = value[1:]
    if not re.match(r"^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$", value):
        die(f"非法版本号: {value}")
    return value


def version_key(value: str) -> tuple[int, ...]:
    return tuple(int(part) for part in re.findall(r"\d+", value))


def resolve_panel_version(requested: str) -> str:
    if requested.strip().lower() in ("", "latest"):
        for base in PANEL_BASE_URLS:
            try:
                version = fetch_text(f"{base}/stable/latest").strip()
                if version:
                    return "v" + normalize_version(version)
            except SystemExit:
                continue
        die("无法解析 1Panel 最新版本")
    return "v" + normalize_version(requested)


def resolve_docker_version(arch: str) -> str:
    platform = "x86_64" if arch == "amd64" else "aarch64"
    html = fetch_text(f"{DOCKER_STATIC_BASE}/{platform}/")
    versions = sorted({m.group(1) for m in re.finditer(r"docker-([0-9]+\.[0-9]+\.[0-9]+)\.tgz", html)}, key=version_key)
    if not versions:
        die(f"无法解析 Docker static 最新版本（{platform}）")
    return versions[-1]


def resolve_compose_version() -> str:
    data = json.loads(fetch_text(COMPOSE_RELEASES_API, headers=github_headers()))
    tag = data.get("tag_name", "")
    return tag.lstrip("v")


def clone_appstore(work_dir: Path) -> tuple[Path, str]:
    dest = work_dir / "appstore"
    if dest.exists():
        shutil.rmtree(dest)
    log("获取 1Panel 官方应用商店（sparse checkout）")
    try:
        run([
            "git", "clone", "--depth=1", "--filter=blob:none", "--sparse",
            "--branch", APPSTORE_BRANCH, APPSTORE_REPO, str(dest),
        ])
        run(["git", "-C", str(dest), "sparse-checkout", "set", *[f"apps/{key}" for key in APP_KEYS]])
        commit = capture(["git", "-C", str(dest), "rev-parse", "HEAD"])
        return dest, commit
    except SystemExit:
        log("sparse checkout 失败，回退到完整 tarball")
        if dest.exists():
            shutil.rmtree(dest)
        archive = work_dir / "appstore.tar.gz"
        download(f"https://codeload.github.com/1Panel-dev/appstore/tar.gz/refs/heads/{APPSTORE_BRANCH}", archive)
        dest.mkdir(parents=True)
        with tarfile.open(archive, "r:gz") as tar:
            for member in tar.getmembers():
                parts = Path(member.name).parts
                if len(parts) >= 3 and parts[1] == "apps" and parts[2] in APP_KEYS:
                    tar.extract(member, dest)
        extracted = next(dest.iterdir())
        # codeload names the root appstore-<branch>; move apps/ up one level.
        shutil.move(str(extracted / "apps"), str(dest / "apps"))
        return dest, "unknown"


def list_versions(app_dir: Path) -> list[str]:
    versions = []
    for entry in app_dir.iterdir():
        if entry.is_dir() and (entry / "docker-compose.yml").is_file() and (entry / "data.yml").is_file():
            versions.append(entry.name)
    return versions


def pick_app_version(key: str, versions: list[str], override: str) -> str:
    if override:
        override = normalize_version(override)
        if override not in versions:
            die(f"应用 {key} 不存在版本 {override}（可用: {', '.join(sorted(versions, key=version_key))}）")
        return override
    if key == "mysql":
        candidates = [v for v in versions if re.match(r"^8\.0\.[0-9]+$", v)]
        if not candidates:
            die("官方应用商店中没有 MySQL 8.0.x 版本")
        return max(candidates, key=version_key)
    if key == "redis":
        candidates = [v for v in versions if re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", v)]
        if not candidates:
            die("官方应用商店中没有可用的 Redis 版本")
        return max(candidates, key=version_key)
    candidates = [v for v in versions if re.match(r"^[0-9]", v)]
    if not candidates:
        die(f"官方应用商店中没有可用的 {key} 版本")
    return max(candidates, key=lambda v: (version_key(v), v))


def resolve_app_versions(appstore: Path, overrides: dict[str, str]) -> dict[str, str]:
    resolved = {}
    for key in APP_KEYS:
        app_dir = appstore / "apps" / key
        if not app_dir.is_dir():
            die(f"应用商店中缺少 apps/{key}")
        versions = list_versions(app_dir)
        if not versions:
            die(f"应用商店中 apps/{key} 没有可用版本")
        resolved[key] = pick_app_version(key, versions, overrides.get(key, ""))
        log(f"{key}: {resolved[key]}")
    return resolved


def load_yaml(path: Path) -> dict:
    with path.open("r", encoding="utf-8") as handle:
        data = yaml.safe_load(handle) or {}
    if not isinstance(data, dict):
        die(f"YAML 格式不正确: {path}")
    return data


def read_text(path: Path) -> str:
    if path.is_file():
        return path.read_text(encoding="utf-8", errors="replace")
    return ""


def first_image(compose: str) -> str:
    match = re.search(r"^\s*image:\s*['\"]?([^'\"\s]+)", compose, re.MULTILINE)
    if not match:
        die("docker-compose.yml 中没有找到 image")
    return match.group(1)


def normalize_description(value) -> dict[str, str]:
    if isinstance(value, dict):
        result = {str(k): str(v) for k, v in value.items() if v}
    elif isinstance(value, str) and value.strip():
        result = {"zh": value, "en": value}
    else:
        result = {}
    result.setdefault("zh", result.get("en", ""))
    result.setdefault("en", result.get("zh", ""))
    return result


def build_apps_metadata(appstore: Path, versions: dict[str, str], built_at: str, store_version: int) -> tuple[dict, list[dict]]:
    apps = []
    copied = []
    for key in [key for key in APP_KEYS if key in versions]:
        version = versions[key]
        app_dir = appstore / "apps" / key
        version_dir = app_dir / version
        app_props = load_yaml(app_dir / "data.yml").get("additionalProperties") or {}
        if not isinstance(app_props, dict):
            die(f"apps/{key}/data.yml additionalProperties 格式不正确")
        version_props = load_yaml(version_dir / "data.yml").get("additionalProperties") or {}
        if not isinstance(version_props, dict):
            die(f"apps/{key}/{version}/data.yml additionalProperties 格式不正确")
        compose = read_text(version_dir / "docker-compose.yml")
        if not compose.strip():
            die(f"apps/{key}/{version}/docker-compose.yml 为空")
        readme = read_text(app_dir / "README.md") or read_text(app_dir / "README_en.md")
        logo = app_dir / "logo.png"
        if not logo.is_file():
            die(f"apps/{key}/logo.png 不存在")
        icon = base64.b64encode(logo.read_bytes()).decode("ascii")
        tags = [str(tag) for tag in (app_props.get("tags") or [])]
        architectures = [str(a) for a in (app_props.get("architectures") or ["amd64", "arm64"])]
        short_zh = str(app_props.get("shortDescZh") or app_props.get("name") or key)
        short_en = str(app_props.get("shortDescEn") or short_zh)
        description = normalize_description(app_props.get("description"))
        if not description.get("zh"):
            description["zh"] = short_zh
        if not description.get("en"):
            description["en"] = short_en
        apps.append({
            "key": key,
            "name": str(app_props.get("name") or key),
            "version": version,
            "image": first_image(compose),
            "type": str(app_props.get("type") or "runtime"),
            "shortDescZh": short_zh,
            "shortDescEn": short_en,
            "description": description,
            "iconBase64": icon,
            "tags": tags,
            "architectures": architectures,
            "website": str(app_props.get("website") or ""),
            "github": str(app_props.get("github") or ""),
            "document": str(app_props.get("document") or ""),
            "recommend": int(app_props.get("recommend") or 9999),
            "limit": int(app_props.get("limit") or 0),
            "crossVersionUpdate": bool(app_props.get("crossVersionUpdate") or False),
            "batchInstallSupport": bool(app_props.get("batchInstallSupport") or False),
            "readMe": readme,
            "params": version_props,
            "dockerCompose": compose,
        })
        copied.append({"key": key, "source": app_dir, "version": version})
    meta = {
        "bundleVersion": "",
        "appStoreCommit": "",
        "appStoreVersion": store_version,
        "builtAt": built_at,
        "apps": apps,
    }
    return meta, copied


def copy_app_dirs(copied: list[dict], dest: Path) -> None:
    dest.mkdir(parents=True, exist_ok=True)
    for item in copied:
        source: Path = item["source"]
        version: str = item["version"]
        target = dest / item["key"]
        if target.exists():
            shutil.rmtree(target)
        target.mkdir(parents=True)
        # Copy app-level metadata/icon/readme only; do not ship every version
        # from the upstream repository.
        for entry in source.iterdir():
            if entry.is_file():
                shutil.copy2(entry, target / entry.name)
        version_source = source / version
        if not version_source.is_dir():
            die(f"应用 {item['key']} 缺少版本目录 {version}")
        version_target = target / version
        shutil.copytree(version_source, version_target, symlinks=True)
        # The OpenResty app directory contains a build cache (source tarballs)
        # that is only needed when building the image from source. The offline
        # package ships the prebuilt image, so drop it to keep the bundle small.
        build_tmp = version_target / "build" / "tmp"
        if build_tmp.exists():
            shutil.rmtree(build_tmp)


def prepare_panel(version: str, arch: str, work_dir: Path, dest: Path) -> None:
    filename = f"1panel-{version}-linux-{arch}.tar.gz"
    archive = work_dir / filename
    last_error = ""
    for base in PANEL_BASE_URLS:
        try:
            checksums = fetch_text(f"{base}/stable/{version}/release/checksums.txt")
            expected = ""
            for line in checksums.splitlines():
                parts = line.split()
                if len(parts) >= 2 and parts[-1] == filename:
                    expected = parts[0]
                    break
            if not expected:
                raise RuntimeError(f"checksums.txt 中没有 {filename}")
            download(f"{base}/stable/{version}/release/{filename}", archive, expected_sha256=expected)
            last_error = ""
            break
        except SystemExit:
            last_error = f"{base} 下载失败"
            continue
    if last_error:
        die(last_error)

    dest.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, "r:gz") as tar:
        members = tar.getmembers()
        if not members:
            die(f"{filename} 为空")
        root = Path(members[0].name).parts[0]
        for member in members:
            parts = Path(member.name).parts
            if not parts or parts[0] != root:
                continue
            member.name = str(Path(*parts[1:])) if len(parts) > 1 else ""
            if not member.name:
                continue
            tar.extract(member, dest, filter="data")

    installer = dest / "install.sh"
    if not installer.is_file():
        die("1Panel 安装包中缺少 install.sh")
    text = installer.read_text(encoding="utf-8", errors="replace")
    patched = re.sub(r'PUBLIC_IP=\$\(curl[^)]*ipify[^)]*\)', 'PUBLIC_IP="N/A"', text)
    if patched == text:
        log("警告: 未找到 Get_Ip 中的 ipify 调用，可能上游安装脚本已变化")
    installer.write_text(patched, encoding="utf-8")

    # Flip the embedded offline setting in both panel binaries. In the default
    # online mode the agent resets AppStoreLastModified to 0 whenever it cannot
    # reach the remote app store, which breaks installation from our seeded
    # entries. "false" -> "true " keeps the byte length identical.
    for binary_name in ("1panel-agent", "1panel-core"):
        binary = dest / binary_name
        if not binary.is_file():
            die(f"1Panel 安装包中缺少 {binary_name}")
        data = binary.read_bytes()
        old = b"is_offline: false"
        new = b"is_offline: true "
        count = data.count(old)
        if count == 0:
            die(f"{binary_name} 中未找到 is_offline 配置，无法启用离线模式")
        binary.write_bytes(data.replace(old, new))
        log(f"{binary_name}: 已启用离线模式（{count} 处）")


def prepare_docker(arch: str, work_dir: Path, dest: Path, docker_version: str, compose_version: str) -> str:
    platform = "x86_64" if arch == "amd64" else "aarch64"
    if not docker_version:
        docker_version = resolve_docker_version(arch)
    tarball = work_dir / f"docker-{docker_version}-{platform}.tgz"
    if not tarball.exists():
        sha_text = fetch_text(f"{DOCKER_STATIC_BASE}/{platform}/docker-{docker_version}.tgz.sha256")
        expected = sha_text.split()[0] if sha_text.split() else ""
        download(f"{DOCKER_STATIC_BASE}/{platform}/docker-{docker_version}.tgz", tarball, expected_sha256=expected)

    if dest.exists():
        shutil.rmtree(dest)
    bin_dir = dest / "bin"
    bin_dir.mkdir(parents=True)
    extract_dir = work_dir / f"docker-extract-{arch}"
    if extract_dir.exists():
        shutil.rmtree(extract_dir)
    extract_dir.mkdir()
    with tarfile.open(tarball, "r:gz") as tar:
        tar.extractall(extract_dir, filter="data")
    source = extract_dir / "docker"
    if not source.is_dir():
        die("Docker 静态包结构异常")
    for entry in source.iterdir():
        if entry.is_file():
            shutil.copy2(entry, bin_dir / entry.name)
    (dest / "service").mkdir()
    shutil.copy2(REPO_ROOT / "installer" / "docker" / "docker.service", dest / "service" / "docker.service")
    (dest / "conf").mkdir()
    shutil.copy2(REPO_ROOT / "installer" / "docker" / "daemon.json", dest / "conf" / "daemon.json")
    (dest / "compose").mkdir()
    compose_name = f"docker-compose-linux-{platform}"
    compose_file = work_dir / compose_name
    if not compose_file.exists():
        base = f"https://github.com/docker/compose/releases/download/v{compose_version}"
        sha_text = fetch_text(f"{base}/{compose_name}.sha256")
        expected = sha_text.split()[0] if sha_text.split() else ""
        download(f"{base}/{compose_name}", compose_file, expected_sha256=expected)
    shutil.copy2(compose_file, dest / "compose" / "docker-compose")
    os.chmod(dest / "compose" / "docker-compose", 0o755)
    return docker_version


def prepare_bootstrap(arch: str, dest: Path, version: str) -> Path:
    go = os.environ.get("GO_BIN", "").strip() or shutil.which("go")
    if not go:
        for candidate in (Path("/usr/local/go/bin/go"), Path.home() / "tools" / "go" / "bin" / "go"):
            if candidate.is_file():
                go = str(candidate)
                break
    if not go:
        die("未找到 go，请安装 Go 1.24+ 后重试（或设置 GO_BIN=/path/to/go）")
    dest.mkdir(parents=True, exist_ok=True)
    output = dest / "1panel-offline-bootstrap"
    env = os.environ.copy()
    env.update({
        "GOOS": "linux",
        "GOARCH": arch,
        "CGO_ENABLED": "0",
        "GOTOOLCHAIN": "auto",
    })
    run([
        go, "build", "-trimpath",
        "-ldflags", f"-s -w -X main.programVersion={version}",
        "-o", str(output), "./cmd/bootstrap",
    ], cwd=REPO_ROOT, env=env)
    os.chmod(output, 0o755)
    return output


def image_digest(arch: str, image: str) -> str:
    if not shutil.which("skopeo"):
        return ""
    result = subprocess.run([
        "skopeo", "inspect", "--override-os", "linux", "--override-arch", arch,
        "--format", "{{.Digest}}", f"docker://{image}",
    ], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    return result.stdout.strip() if result.returncode == 0 else ""


def prepare_images(arch: str, apps: list[dict], dest: Path, work_dir: Path) -> dict[str, str]:
    dest.mkdir(parents=True, exist_ok=True)
    manifest = ["# key\timage\tfile"]
    digests = {}
    skopeo = shutil.which("skopeo") is not None
    for app in apps:
        key = app["key"]
        image = app["image"]
        filename = f"{key}-{app['version']}.tar.gz"
        target = dest / filename
        digest = image_digest(arch, image)
        if digest:
            digests[key] = digest
        if target.exists() and target.stat().st_size > 0:
            log(f"复用已存在的镜像包: {target.name}")
        else:
            if skopeo:
                tmp_tar = work_dir / f"{key}-{arch}.tar"
                tmp_tar.unlink(missing_ok=True)
                log(f"导出镜像 {image} ({arch})")
                run([
                    "skopeo", "copy", "--override-os", "linux", "--override-arch", arch,
                    f"docker://{image}", f"docker-archive:{tmp_tar}:{image}",
                ])
                with tmp_tar.open("rb") as source, target.open("wb") as raw, \
                        subprocess.Popen(["gzip", "-1", "-c"], stdin=subprocess.PIPE, stdout=raw) as gzip_proc:
                    shutil.copyfileobj(source, gzip_proc.stdin, length=4 << 20)
                    gzip_proc.stdin.close()
                    if gzip_proc.wait() != 0:
                        die(f"压缩镜像失败: {image}")
                tmp_tar.unlink(missing_ok=True)
            else:
                log(f"未安装 skopeo，改用 Docker 导出 {image} ({arch})")
                run(["docker", "pull", "--platform", f"linux/{arch}", image])
                with target.open("wb") as raw, \
                        subprocess.Popen(["gzip", "-1", "-c"], stdin=subprocess.PIPE, stdout=raw) as gzip_proc:
                    save = subprocess.Popen(["docker", "save", image], stdout=gzip_proc.stdin)
                    if save.wait() != 0:
                        die(f"docker save 失败: {image}")
                    gzip_proc.stdin.close()
                    if gzip_proc.wait() != 0:
                        die(f"压缩镜像失败: {image}")
                run(["docker", "image", "rm", "-f", image], check=False)
        if not target.is_file() or target.stat().st_size == 0:
            die(f"镜像包为空: {target}")
        manifest.append(f"{key}\t{image}\t{filename}")
    (dest / "manifest.txt").write_text("\n".join(manifest) + "\n", encoding="utf-8")
    return digests


def repo_revision() -> str:
    revision = os.environ.get("GITHUB_SHA", "").strip()
    if revision:
        return revision
    result = subprocess.run(
        ["git", "-C", str(REPO_ROOT), "rev-parse", "HEAD"],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    )
    return result.stdout.strip() if result.returncode == 0 else "local"


def fingerprint(meta: dict, docker_version: str, compose_version: str, appstore_commit: str) -> str:
    # Only versions that affect the produced package participate in the
    # fingerprint. The app-store commit is recorded separately; using it here
    # would rebuild the 1.6 GB package every time any unrelated upstream app
    # changes.
    app_versions = ",".join(f"{app['key']}:{app['version']}" for app in meta["apps"])
    material = "|".join([
        meta["bundleVersion"], app_versions, docker_version, compose_version, repo_revision(),
    ])
    return hashlib.sha256(material.encode()).hexdigest()


def copy_tree(source: Path, dest: Path) -> None:
    if dest.exists():
        shutil.rmtree(dest)
    shutil.copytree(source, dest, symlinks=True)


def write_checksums(bundle: Path) -> None:
    lines = []
    for path in sorted(bundle.rglob("*")):
        if not path.is_file() or path.name == "SHA256SUMS":
            continue
        rel = path.relative_to(bundle).as_posix()
        lines.append(f"{sha256_file(path)}  {rel}")
    (bundle / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="utf-8")


def build_arch(
    arch: str,
    version: str,
    meta: dict,
    copied_apps: list[dict],
    work_dir: Path,
    out_dir: Path,
    docker_version: str,
    compose_version: str,
    skip_images: bool,
) -> Path:
    stage_root = work_dir / "stage"
    bundle_name = f"1panel-offline-{version}-linux-{arch}"
    bundle = stage_root / bundle_name
    if bundle.exists():
        shutil.rmtree(bundle)
    bundle.mkdir(parents=True)

    log(f"准备 {arch} 平台 1Panel 安装文件")
    prepare_panel(version, arch, work_dir, bundle / "1panel")

    log(f"复制应用商店应用（{arch}）")
    copy_app_dirs(copied_apps, bundle / "apps")

    meta_dir = bundle / "meta"
    meta_dir.mkdir()
    (meta_dir / "apps.json").write_text(json.dumps(meta, ensure_ascii=False, indent=2), encoding="utf-8")

    log(f"准备 Docker 离线负载（{arch}）")
    actual_docker = prepare_docker(arch, work_dir, bundle / "docker", docker_version, compose_version)
    actual_version = prepare_bootstrap(arch, bundle / "bootstrap", meta["bundleVersion"])

    if skip_images:
        log("跳过 Docker 镜像导出")
        images_dir = bundle / "images"
        images_dir.mkdir()
        (images_dir / "manifest.txt").write_text("# images intentionally skipped\n", encoding="utf-8")
        digests = {}
    else:
        log(f"导出 Docker 镜像（{arch}）")
        digests = prepare_images(arch, meta["apps"], bundle / "images", work_dir)

    shutil.copy2(REPO_ROOT / "installer" / "install.sh", bundle / "install.sh")
    os.chmod(bundle / "install.sh", 0o755)
    shutil.copy2(REPO_ROOT / "installer" / "README.md", bundle / "README.md")
    (bundle / "VERSION").write_text(version + "\n", encoding="utf-8")

    app_versions = ",".join(f"{app['key']}:{app['version']}" for app in meta["apps"])
    build_fp = fingerprint(meta, actual_docker, compose_version, meta["appStoreCommit"])
    build_info = [
        f"version={version}",
        f"arch={arch}",
        f"bundle={bundle_name}.tar.gz",
        f"built_at={meta['builtAt']}",
        f"appstore_commit={meta['appStoreCommit']}",
        f"appstore_version={meta['appStoreVersion']}",
        f"apps={app_versions}",
        f"docker_version={actual_docker}",
        f"compose_version={compose_version}",
        f"image_digests={json.dumps(digests, sort_keys=True)}",
        f"fingerprint={build_fp}",
    ]
    (bundle / "BUILD_INFO").write_text("\n".join(build_info) + "\n", encoding="utf-8")
    (out_dir / f"BUILD_INFO-{arch}").write_text("\n".join(build_info) + "\n", encoding="utf-8")

    log(f"生成校验和（{arch}）")
    write_checksums(bundle)

    out_dir.mkdir(parents=True, exist_ok=True)
    archive = out_dir / f"{bundle_name}.tar.gz"
    log(f"打包 {archive}")
    run([
        "tar", "--sort=name", "--owner=0", "--group=0", "--numeric-owner",
        "-C", str(stage_root), "-czf", str(archive), bundle_name,
    ])
    (out_dir / f"{bundle_name}.tar.gz.sha256").write_text(
        f"{sha256_file(archive)}  {archive.name}\n", encoding="utf-8"
    )
    (out_dir / "SHA256SUMS").open("a", encoding="utf-8").write(
        f"{sha256_file(archive)}  {archive.name}\n"
    )
    shutil.rmtree(bundle)
    log(f"{arch} 完成: {archive} ({archive.stat().st_size / (1 << 20):.1f} MiB)")
    return archive


def main() -> int:
    parser = argparse.ArgumentParser(description="Build 1Panel offline installer packages")
    parser.add_argument("--version", default="latest", help="1Panel version (latest or v2.x.y)")
    parser.add_argument("--arch", default="amd64,arm64", help="comma-separated: amd64,arm64")
    parser.add_argument("--mysql-version", default="", help="MySQL version (default: latest 8.0.x)")
    parser.add_argument("--redis-version", default="", help="Redis version (default: latest)")
    parser.add_argument("--openresty-version", default="", help="OpenResty version (default: latest)")
    parser.add_argument("--docker-version", default="", help="Docker static version (default: latest)")
    parser.add_argument("--compose-version", default="", help="Compose version (default: latest)")
    parser.add_argument("--out-dir", default="dist")
    parser.add_argument("--work-dir", default=".build")
    parser.add_argument("--skip-images", action="store_true", help="build metadata-only package without images")
    parser.add_argument("--keep-work", action="store_true")
    args = parser.parse_args()

    out_dir = (REPO_ROOT / args.out_dir).resolve()
    work_dir = (REPO_ROOT / args.work_dir).resolve()
    if work_dir.exists() and not args.keep_work:
        shutil.rmtree(work_dir)
    work_dir.mkdir(parents=True, exist_ok=True)
    out_dir.mkdir(parents=True, exist_ok=True)
    for stale in list(out_dir.glob("1panel-offline-*.tar.gz")) + list(out_dir.glob("1panel-offline-*.tar.gz.sha256")) + list(out_dir.glob("BUILD_INFO-*")):
        stale.unlink(missing_ok=True)
    (out_dir / "SHA256SUMS").write_text("", encoding="utf-8")

    version = resolve_panel_version(args.version)
    log(f"1Panel 版本: {version}")
    archs = [a.strip() for a in args.arch.split(",") if a.strip()]
    for arch in archs:
        if arch not in ("amd64", "arm64"):
            die(f"不支持的架构: {arch}")

    compose_version = args.compose_version.lstrip("v") if args.compose_version else resolve_compose_version()
    log(f"Compose 版本: {compose_version}")

    appstore, commit = clone_appstore(work_dir)
    versions = resolve_app_versions(appstore, {
        "mysql": args.mysql_version,
        "redis": args.redis_version,
        "openresty": args.openresty_version,
    })
    store_version = int(time.time())
    built_at = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    meta, copied = build_apps_metadata(appstore, versions, built_at, store_version)
    meta["bundleVersion"] = version
    meta["appStoreCommit"] = commit

    for arch in archs:
        docker_version = args.docker_version or resolve_docker_version(arch)
        build_arch(
            arch, version, meta, copied, work_dir, out_dir,
            docker_version, compose_version, args.skip_images,
        )

    if not args.keep_work:
        shutil.rmtree(work_dir, ignore_errors=True)
    print()
    log("构建完成")
    for archive in sorted(out_dir.glob("*.tar.gz")):
        print(f"  {archive.name}  {archive.stat().st_size / (1 << 20):.1f} MiB")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
