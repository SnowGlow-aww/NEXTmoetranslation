#!/usr/bin/env python3
"""Adversarial regression tests for verify-rollback-bundle.py."""

from __future__ import annotations

import hashlib
import io
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent
VERIFIER = ROOT / "verify-rollback-bundle.py"
MTIME = 1_700_000_000


def regular(name: str, data: bytes, mode: int = 0o644) -> tarfile.TarInfo:
    member = tarfile.TarInfo(name)
    member.size = len(data)
    member.mode = mode
    member.uid = 0
    member.gid = 0
    member.mtime = MTIME
    member.type = tarfile.REGTYPE
    return member


def directory(name: str, mode: int = 0o755) -> tarfile.TarInfo:
    member = tarfile.TarInfo(name)
    member.mode = mode
    member.uid = 0
    member.gid = 0
    member.mtime = MTIME
    member.type = tarfile.DIRTYPE
    return member


def symlink(name: str, target: str) -> tarfile.TarInfo:
    member = tarfile.TarInfo(name)
    member.mode = 0o777
    member.uid = 0
    member.gid = 0
    member.mtime = MTIME
    member.type = tarfile.SYMTYPE
    member.linkname = target
    return member


def build_bundle(path: Path, *, server_kind: str = "file", web_kind: str = "directory", index_kind: str = "file") -> None:
    server_bytes = b"server"
    migrate_bytes = b"migrate"
    index_bytes = b"index"
    checksummed = {
        "moesekai-migrate": hashlib.sha256(migrate_bytes).hexdigest(),
        "moesekai-server": hashlib.sha256(server_bytes).hexdigest(),
        "web/index.html": hashlib.sha256(index_bytes).hexdigest(),
    }
    checksum_bytes = "".join(f"{digest}  {name}\n" for name, digest in sorted(checksummed.items())).encode()

    members: list[tuple[tarfile.TarInfo, bytes]] = [(directory("."), b"")]
    if web_kind == "directory":
        members.append((directory("./web"), b""))
    elif web_kind == "file":
        members.append((regular("./web", b"not-a-directory"), b"not-a-directory"))
    else:
        raise ValueError(web_kind)

    if server_kind == "file":
        members.append((regular("./moesekai-server", server_bytes, 0o755), server_bytes))
    elif server_kind == "directory":
        members.append((directory("./moesekai-server", 0o755), b""))
    else:
        raise ValueError(server_kind)

    members.append((regular("./moesekai-migrate", migrate_bytes, 0o755), migrate_bytes))

    if index_kind == "file":
        members.append((regular("./web/index.html", index_bytes), index_bytes))
    elif index_kind == "directory":
        members.append((directory("./web/index.html"), b""))
    else:
        raise ValueError(index_kind)

    members.append((regular("./SHA256SUMS", checksum_bytes), checksum_bytes))

    with tarfile.open(path, "w", format=tarfile.GNU_FORMAT) as archive:
        for member, data in members:
            if member.isreg():
                archive.addfile(member, io.BytesIO(data))
            else:
                archive.addfile(member)


def run_verifier(path: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(VERIFIER), str(path)],
        check=False,
        capture_output=True,
        text=True,
    )


class VerifyRollbackBundleTests(unittest.TestCase):
    def test_valid_bundle(self) -> None:
        with tempfile.TemporaryDirectory() as directory_path:
            path = Path(directory_path) / "valid.tar"
            build_bundle(path)
            result = run_verifier(path)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_required_executable_must_be_regular_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory_path:
            path = Path(directory_path) / "server-directory.tar"
            build_bundle(path, server_kind="directory")
            result = run_verifier(path)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("moesekai-server", result.stderr)
            self.assertIn("regular file", result.stderr)

    def test_web_root_and_index_types_are_closed(self) -> None:
        for name, kwargs, expected in [
            ("web-file.tar", {"web_kind": "file"}, "web"),
            ("index-directory.tar", {"index_kind": "directory"}, "web/index.html"),
        ]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory_path:
                path = Path(directory_path) / name
                build_bundle(path, **kwargs)
                result = run_verifier(path)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected, result.stderr)

    def test_missing_directory_ancestor_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory_path:
            path = Path(directory_path) / "missing-ancestor.tar"
            files = {
                "moesekai-migrate": (b"migrate", 0o755),
                "moesekai-server": (b"server", 0o755),
                "web/assets/app.js": (b"asset", 0o644),
                "web/index.html": (b"index", 0o644),
            }
            checksum_bytes = "".join(
                f"{hashlib.sha256(data).hexdigest()}  {name}\n"
                for name, (data, _) in sorted(files.items())
            ).encode()
            with tarfile.open(path, "w", format=tarfile.GNU_FORMAT) as archive:
                archive.addfile(directory("."))
                archive.addfile(directory("./web"))
                for name, (data, mode) in files.items():
                    archive.addfile(regular(f"./{name}", data, mode), io.BytesIO(data))
                archive.addfile(regular("./SHA256SUMS", checksum_bytes), io.BytesIO(checksum_bytes))
            result = run_verifier(path)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("missing directory ancestor", result.stderr)

    def test_file_child_conflict_and_link_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory_path:
            path = Path(directory_path) / "conflict.tar"
            checksum = hashlib.sha256(b"index").hexdigest()
            checksum_bytes = (
                f"{hashlib.sha256(b'migrate').hexdigest()}  moesekai-migrate\n"
                f"{hashlib.sha256(b'server').hexdigest()}  moesekai-server\n"
                f"{checksum}  web/index.html\n"
            ).encode()
            with tarfile.open(path, "w", format=tarfile.GNU_FORMAT) as archive:
                archive.addfile(directory("."))
                archive.addfile(regular("./web", b"file-root"), io.BytesIO(b"file-root"))
                archive.addfile(regular("./web/index.html", b"index"), io.BytesIO(b"index"))
                archive.addfile(regular("./moesekai-server", b"server", 0o755), io.BytesIO(b"server"))
                archive.addfile(regular("./moesekai-migrate", b"migrate", 0o755), io.BytesIO(b"migrate"))
                archive.addfile(regular("./SHA256SUMS", checksum_bytes), io.BytesIO(checksum_bytes))
            result = run_verifier(path)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("web", result.stderr)

            link_path = Path(directory_path) / "symlink.tar"
            build_bundle(link_path)
            with tarfile.open(link_path, "a", format=tarfile.GNU_FORMAT) as archive:
                archive.addfile(symlink("./web/assets/leak.js", "../../secret"))
            result = run_verifier(link_path)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("not a regular file or directory", result.stderr)


if __name__ == "__main__":
    unittest.main()
