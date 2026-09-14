#!/usr/bin/env python3
"""Publish Vite artifacts without restarting Nginx, API, or Worker (Linux)."""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import shutil
import signal
import ssl
import sys
import tempfile
from urllib.parse import urlsplit
from urllib.request import Request, urlopen
import uuid


class ReleaseError(Exception):
    pass


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def atomic_bytes(path, data):
    fd, temporary = tempfile.mkstemp(prefix=".write-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as out:
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
            os.fchmod(out.fileno(), 0o644)
        os.replace(temporary, path)
        sync_directory(path.parent)
    finally:
        Path(temporary).unlink(missing_ok=True)


def atomic_json(path, value):
    atomic_bytes(path, (json.dumps(value, sort_keys=True) + "\n").encode())


class Entries(HTMLParser):
    def __init__(self):
        super().__init__()
        self.assets = set()

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        value = attrs.get("src") if tag == "script" else None
        if tag == "link" and attrs.get("rel") in ("stylesheet", "modulepreload"):
            value = attrs.get("href")
        if value is not None:
            if not value.startswith("/assets/"):
                raise ReleaseError("HTML must reference same-origin /assets/ build outputs")
            self.assets.add(value[1:])


def validate_build(directory):
    """Validate every emitted asset, including lazy imports in Vite's manifest."""
    directory = Path(directory)
    if not directory.is_dir() or directory.is_symlink():
        raise ReleaseError("build directory is missing or is a symlink")
    files = {}
    for path in directory.rglob("*"):
        if path.is_symlink():
            raise ReleaseError("symlinks are not permitted in build artifacts")
        if not path.is_file():
            continue
        name = path.relative_to(directory).as_posix()
        if name == "release.json":
            continue
        if name not in ("index.html", ".vite/manifest.json"):
            if not re.fullmatch(r"assets/[A-Za-z0-9_./-]+-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9.]+", name):
                raise ReleaseError("unexpected or unhashed build output: " + name)
        if path.stat().st_size == 0:
            raise ReleaseError("empty build output: " + name)
        files[name] = digest(path)
    if not {"index.html", ".vite/manifest.json"} <= files.keys():
        raise ReleaseError("index.html and Vite manifest are required; run make web-build")
    manifest = json.loads((directory / ".vite/manifest.json").read_text())
    if not isinstance(manifest, dict) or not manifest:
        raise ReleaseError("invalid Vite manifest")
    outputs = set()
    for entry in manifest.values():
        if not isinstance(entry, dict) or "file" not in entry:
            raise ReleaseError("invalid manifest entry")
        for name in [entry["file"], *entry.get("css", []), *entry.get("assets", [])]:
            if not isinstance(name, str) or not name.startswith("assets/") or name not in files:
                raise ReleaseError("missing manifest asset: " + str(name))
            outputs.add(name)
        for name in [*entry.get("imports", []), *entry.get("dynamicImports", [])]:
            if name not in manifest:
                raise ReleaseError("missing manifest import: " + str(name))
    entries = Entries()
    entries.feed((directory / "index.html").read_text())
    if not entries.assets or not entries.assets <= outputs:
        raise ReleaseError("HTML references missing or untracked assets")
    return files


def validate_release_id(value):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,95}", value or ""):
        raise ReleaseError("RELEASE is required: use 1–96 letters, digits, dots, underscores or hyphens")


def current_target(root):
    current = root / "current"
    if not current.is_symlink():
        if current.exists():
            raise ReleaseError("current must be a managed symlink")
        return None
    target = os.readlink(current)
    if not re.fullmatch(r"releases/[A-Za-z0-9][A-Za-z0-9._-]{0,95}", target):
        raise ReleaseError("current points outside managed releases")
    if not (root / target).is_dir() or (root / target).is_symlink():
        raise ReleaseError("current release is missing")
    return target


def switch(root, target):
    if target is None:
        (root / "current").unlink(missing_ok=True)
    else:
        temporary = root / (".current-" + uuid.uuid4().hex)
        try:
            temporary.symlink_to(target)
            os.replace(temporary, root / "current")
        finally:
            temporary.unlink(missing_ok=True)
    sync_directory(root)


@contextmanager
def release_lock(root):
    root.mkdir(parents=True, exist_ok=True)
    for name in ("releases", "assets"):
        path = root / name
        if path.is_symlink():
            raise ReleaseError(name + " must not be a symlink")
        path.mkdir(exist_ok=True)
    with (root / ".publish.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ReleaseError("another publish or rollback is in progress") from error
        # Recover an interrupted transaction before accepting another release.
        journal = root / ".pending.json"
        if journal.exists():
            previous = json.loads(journal.read_text())["previous"]
            if previous is not None:
                if not re.fullmatch(r"releases/[A-Za-z0-9][A-Za-z0-9._-]{0,95}", previous):
                    raise ReleaseError("invalid recovery journal")
                if not (root / previous).is_dir():
                    raise ReleaseError("previous release needed for recovery is missing")
            switch(root, previous)
            journal.unlink()
            sync_directory(root)
        yield


def prepare(root, source, release):
    destination = root / "releases" / release
    if destination.exists():
        raise ReleaseError("release already exists; use a new RELEASE or web-rollback")
    validate_build(source)
    staging = Path(tempfile.mkdtemp(prefix=".stage-", dir=root / "releases"))
    try:
        shutil.copytree(source, staging, dirs_exist_ok=True)
        files = validate_build(staging)
        for path in staging.rglob("*"):
            path.chmod(0o755 if path.is_dir() else 0o644)
        staging.chmod(0o755)
        atomic_json(staging / "release.json", {"release": release, "files": files})
        # Shared assets are append-only. No old browser loses a chunk on release.
        for name, expected in files.items():
            if not name.startswith("assets/"):
                continue
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            if target.exists():
                if target.is_symlink() or digest(target) != expected:
                    raise ReleaseError("immutable asset collision: " + name)
            else:
                atomic_bytes(target, (staging / name).read_bytes())
        os.rename(staging, destination)
        sync_directory(destination.parent)
    finally:
        if staging.exists():
            shutil.rmtree(staging)
    return files


def read_release(root, release):
    directory = root / "releases" / release
    files = validate_build(directory)
    metadata = json.loads((directory / "release.json").read_text())
    if metadata != {"release": release, "files": files}:
        raise ReleaseError("release integrity check failed")
    for name, expected in files.items():
        if name.startswith("assets/"):
            path = root / name
            if not path.is_file() or path.is_symlink() or digest(path) != expected:
                raise ReleaseError("shared asset integrity check failed: " + name)
    return files


def check_site(origin, files, ca_file=None):
    context = ssl.create_default_context(cafile=ca_file or None)
    for name, expected in files.items():
        if name != "index.html" and not name.startswith("assets/"):
            continue
        url = origin.rstrip("/") + ("/" if name == "index.html" else "/" + name)
        request = Request(url, headers={"Cache-Control": "no-cache", "Accept-Encoding": "identity"})
        with urlopen(request, timeout=10, context=context) as response:
            if response.status != 200 or hashlib.sha256(response.read()).hexdigest() != expected:
                raise ReleaseError("published content verification failed: " + name)


def release(action, root, source, version, origin, ca_file=None, checker=check_site):
    validate_release_id(version)
    parsed = urlsplit(origin)
    if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
        raise ReleaseError("WEB_CHECK_URL must be the public http(s) origin without credentials or a path")
    root = Path(root).absolute()
    with release_lock(root):
        previous = current_target(root)
        files = prepare(root, Path(source), version) if action == "publish" else read_release(root, version)
        journal = root / ".pending.json"
        atomic_json(journal, {"previous": previous, "next": "releases/" + version})
        try:
            switch(root, "releases/" + version)
            checker(origin, files, ca_file)
            with (root / "history.jsonl").open("a") as history:
                history.write(json.dumps({"at": datetime.now(timezone.utc).isoformat(), "action": action, "release": version, "previous": previous}) + "\n")
                history.flush()
                os.fsync(history.fileno())
        except BaseException:
            # Keep the journal if restoring the symlink itself fails. A later
            # invocation must recover before it can accept another release.
            switch(root, previous)
            journal.unlink(missing_ok=True)
            sync_directory(root)
            raise
        else:
            journal.unlink(missing_ok=True)
            sync_directory(root)


def interrupted(signum, frame):
    raise InterruptedError("release interrupted; restoring previous version")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("publish", "rollback"))
    parser.add_argument("--root", default=os.getenv("WEB_ROOT", ".deploy/web"))
    parser.add_argument("--dist", default="web/dist")
    parser.add_argument("--release", default=os.getenv("RELEASE", ""))
    parser.add_argument("--check-url", default=os.getenv("WEB_CHECK_URL", "https://localhost"))
    parser.add_argument("--ca-file", default=os.getenv("WEB_CA_FILE"))
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, interrupted)
    try:
        release(args.action, args.root, args.dist, args.release, args.check_url, args.ca_file)
    except (Exception, KeyboardInterrupt) as error:
        print("web release failed: " + str(error), file=sys.stderr)
        return 1
    print(args.action + " complete: " + args.release)
    return 0


if __name__ == "__main__":
    sys.exit(main())
