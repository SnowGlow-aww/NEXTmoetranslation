#!/usr/bin/env python3
"""Fail-closed verifier for the deterministic NextTrans rollback tar."""

from __future__ import annotations

import hashlib
import re
import sys
import tarfile
from pathlib import PurePosixPath

MAX_MEMBERS = 100_000
MAX_FILE_BYTES = 512 << 20
MAX_TOTAL_BYTES = 1 << 30
MAX_CHECKSUM_BYTES = 16 << 20
CHECKSUM_LINE = re.compile(r"^([0-9a-f]{64})  ([A-Za-z0-9._/+-]+)$")


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"rollback bundle verification failed: {message}")


def canonical_name(raw: str) -> str:
    if raw == ".":
        return ""
    if not raw.startswith("./"):
        fail(f"noncanonical member name {raw!r}")
    name = raw[2:]
    if not name or "\\" in name or "\x00" in name:
        fail(f"unsafe member name {raw!r}")
    path = PurePosixPath(name)
    if path.is_absolute() or any(part in ("", ".", "..") for part in path.parts):
        fail(f"unsafe member path {raw!r}")
    if str(path) != name:
        fail(f"noncanonical member path {raw!r}")
    return name


def read_member(archive: tarfile.TarFile, member: tarfile.TarInfo, limit: int) -> bytes:
    if member.size > limit:
        fail(f"member {member.name!r} exceeds {limit} bytes")
    source = archive.extractfile(member)
    if source is None:
        fail(f"cannot read regular member {member.name!r}")
    data = source.read(limit + 1)
    if len(data) != member.size or len(data) > limit:
        fail(f"member {member.name!r} size changed while reading")
    if source.read(1):
        fail(f"member {member.name!r} contains trailing bytes")
    return data


def verify(archive_path: str) -> None:
    try:
        archive = tarfile.open(archive_path, mode="r:")
    except (OSError, tarfile.TarError) as error:
        fail(f"open archive: {error}")

    with archive:
        if archive.pax_headers:
            fail("global PAX headers are not allowed")
        members = archive.getmembers()
        if not members or len(members) > MAX_MEMBERS:
            fail(f"member count {len(members)} is outside 1..{MAX_MEMBERS}")

        by_name: dict[str, tarfile.TarInfo] = {}
        total_size = 0
        mtimes: set[int | float] = set()
        for member in members:
            name = canonical_name(member.name)
            if name in by_name:
                fail(f"duplicate member {name!r}")
            by_name[name] = member
            if member.pax_headers or getattr(member, "sparse", None):
                fail(f"extended or sparse member {member.name!r} is not allowed")
            if member.uid != 0 or member.gid != 0:
                fail(f"member {member.name!r} is not owned by numeric root")
            mtimes.add(member.mtime)

            if name == "":
                if not member.isdir() or member.mode & 0o7777 != 0o755:
                    fail("archive root must be a 0755 directory")
                continue
            if name not in {"SHA256SUMS", "moesekai-server", "moesekai-migrate", "web"} and not name.startswith("web/"):
                fail(f"unexpected top-level member {name!r}")
            if member.isdir():
                if member.mode & 0o7777 != 0o755:
                    fail(f"directory {name!r} mode is not 0755")
            elif member.isreg():
                wanted_mode = 0o755 if name in {"moesekai-server", "moesekai-migrate"} else 0o644
                if member.mode & 0o7777 != wanted_mode:
                    fail(f"file {name!r} mode is not {wanted_mode:04o}")
                if member.size > MAX_FILE_BYTES:
                    fail(f"file {name!r} exceeds {MAX_FILE_BYTES} bytes")
                total_size += member.size
                if total_size > MAX_TOTAL_BYTES:
                    fail(f"regular-file total exceeds {MAX_TOTAL_BYTES} bytes")
            else:
                fail(f"member {name!r} is not a regular file or directory")

        required_types = {
            "": "directory",
            "SHA256SUMS": "regular file",
            "moesekai-server": "regular file",
            "moesekai-migrate": "regular file",
            "web": "directory",
            "web/index.html": "regular file",
        }
        missing = sorted(required_types.keys() - by_name.keys())
        if missing:
            fail(f"missing required members: {', '.join(missing)}")
        for name, wanted_type in required_types.items():
            member = by_name[name]
            if wanted_type == "directory" and not member.isdir():
                fail(f"required member {name or '.'!r} is not a directory")
            if wanted_type == "regular file" and not member.isreg():
                fail(f"required member {name!r} is not a regular file")

        # Every ancestor must be explicitly inventoried as a directory. This
        # rejects omitted directory headers and file/child conflicts regardless
        # of archive member order.
        for name in by_name:
            if name == "":
                continue
            parent = PurePosixPath(name).parent
            while str(parent) != ".":
                parent_name = str(parent)
                ancestor = by_name.get(parent_name)
                if ancestor is None:
                    fail(f"member {name!r} is missing directory ancestor {parent_name!r}")
                if not ancestor.isdir():
                    fail(f"member {name!r} has non-directory ancestor {parent_name!r}")
                parent = parent.parent

        if len(mtimes) != 1:
            fail("archive members do not share one deterministic mtime")

        checksum_member = by_name["SHA256SUMS"]
        checksum_bytes = read_member(archive, checksum_member, MAX_CHECKSUM_BYTES)
        try:
            checksum_text = checksum_bytes.decode("utf-8")
        except UnicodeDecodeError as error:
            fail(f"SHA256SUMS is not UTF-8: {error}")
        if not checksum_text.endswith("\n") or "\r" in checksum_text:
            fail("SHA256SUMS must use canonical LF-terminated lines")

        expected: dict[str, str] = {}
        ordered_names: list[str] = []
        for line in checksum_text.splitlines():
            match = CHECKSUM_LINE.fullmatch(line)
            if match is None:
                fail(f"malformed SHA256SUMS line {line!r}")
            digest, name = match.groups()
            if name in expected:
                fail(f"duplicate SHA256SUMS path {name!r}")
            if canonical_name(f"./{name}") != name or name == "SHA256SUMS":
                fail(f"unsafe SHA256SUMS path {name!r}")
            expected[name] = digest
            ordered_names.append(name)
        if ordered_names != sorted(ordered_names):
            fail("SHA256SUMS paths are not sorted")

        actual_files = {name for name, member in by_name.items() if member.isreg() and name != "SHA256SUMS"}
        if set(expected) != actual_files:
            missing_checksums = sorted(actual_files - expected.keys())
            extra_checksums = sorted(expected.keys() - actual_files)
            fail(f"SHA256SUMS inventory mismatch: missing={missing_checksums} extra={extra_checksums}")

        for name in sorted(expected):
            member = by_name[name]
            source = archive.extractfile(member)
            if source is None:
                fail(f"cannot hash member {name!r}")
            digest = hashlib.sha256()
            read_size = 0
            while chunk := source.read(1 << 20):
                read_size += len(chunk)
                if read_size > member.size:
                    fail(f"member {name!r} exceeds declared size while hashing")
                digest.update(chunk)
            if read_size != member.size:
                fail(f"member {name!r} is shorter than declared size")
            if digest.hexdigest() != expected[name]:
                fail(f"SHA-256 mismatch for {name!r}")

    print("rollback bundle verified")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} <nexttrans-rollback.tar>")
    verify(sys.argv[1])
