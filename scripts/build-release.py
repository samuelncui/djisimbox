#!/usr/bin/env python3
"""Build checked, allowlisted Linux release packages with standard-library tools."""

import argparse
import gzip
import hashlib
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parent.parent
SOURCE_FILES = (
    "README.md",
    "README.zh-CN.md",
    "LICENSE",
    "deploy/networkd/05-djisimbox-sms-only.network",
    "deploy/polkit/50-djisimbox.rules",
    "deploy/systemd/djisimbox.service",
    "deploy/systemd/journald@djisimbox.conf",
    "deploy/systemd/journald-djisimbox-volatile.conf",
    "examples/config.json",
    "examples/secrets.json",
    "docs/install.md",
    "docs/install.zh-CN.md",
    "docs/operations.md",
    "docs/operations.zh-CN.md",
)
MODULE_LICENSES = {
    "github.com/godbus/dbus/v5": ("LICENSE",),
    "golang.org/x/sys": ("LICENSE", "PATENTS"),
}
# SemVer 2.0.0, with v as the release tag prefix. Numeric prerelease fields
# have the same no-leading-zero rule as major, minor, and patch numbers.
NUMBER = r"(?:0|[1-9][0-9]*)"
PRERELEASE = rf"(?:{NUMBER}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
VERSION = re.compile(
    rf"v{NUMBER}\.{NUMBER}\.{NUMBER}"
    rf"(?:-{PRERELEASE}(?:\.{PRERELEASE})*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
    r"|dev-[0-9a-f]{12}"
)


def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True).strip()


def notices():
    """Keep license ownership explicit; new/replaced modules require review."""
    run("go", "mod", "download")
    run("go", "mod", "verify")
    goroot = Path(run("go", "env", "GOROOT"))
    parts = ["Third-party notices for the bundled djisimbox binary.\n"]

    def append(name, directory, filenames):
        for filename in filenames:
            parts.append(f"\n=== {name}: {filename} ===\n\n")
            parts.append((directory / filename).read_text(encoding="utf-8"))
            parts.append("\n")

    append(run("go", "env", "GOVERSION"), goroot, ("LICENSE", "PATENTS"))
    for name in ("crypto", "net", "sys", "text"):
        directory = goroot / "src/vendor/golang.org/x" / name
        if directory.is_dir():
            append(f"Go bundled golang.org/x/{name}", directory, ("LICENSE", "PATENTS"))

    modules = run(
        "go", "list", "-m", "-f",
        '{{if not .Main}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}'
        '{{if .Replace}}{{"\t"}}REPLACED{{end}}{{end}}', "all",
    )
    found = set()
    for line in modules.splitlines():
        if not line:
            continue
        fields = line.split("\t")
        if len(fields) != 3 or fields[0] not in MODULE_LICENSES:
            raise ValueError("review third-party license allowlist for changed modules")
        module, version, directory = fields
        found.add(module)
        append(f"{module} {version}", Path(directory), MODULE_LICENSES[module])
    if found != MODULE_LICENSES.keys():
        raise ValueError("update third-party license allowlist for removed modules")
    return "".join(parts)


def check_binary(binary, arch, version):
    header = run("readelf", "--file-header", str(binary))
    machine = {"arm64": "AArch64", "amd64": "Advanced Micro Devices X86-64"}[arch]
    if machine not in header or "ELF64" not in header:
        raise ValueError(f"unexpected ELF architecture: {arch}")
    if "INTERP" in run("readelf", "--program-headers", str(binary)):
        raise ValueError(f"binary requires a dynamic interpreter: {arch}")
    if "(NEEDED)" in run("readelf", "--dynamic", str(binary)):
        raise ValueError(f"binary requires shared libraries: {arch}")
    if platform.system() == "Linux" and platform.machine() == "x86_64" and arch == "amd64":
        if run(str(binary), "version") != version:
            raise ValueError("packaged binary reports the wrong version")


def package(arch, version, output, temporary, third_party):
    name = f"djisimbox_{version}_linux_{arch}"
    directory = temporary / name
    directory.mkdir()
    for relative in SOURCE_FILES:
        source = ROOT / relative
        if source.is_symlink() or any(parent.is_symlink() for parent in source.parents if parent.is_relative_to(ROOT)):
            raise ValueError(f"release input must be a regular repository file: {relative}")
        target = directory / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
    (directory / "THIRD_PARTY_NOTICES").write_text(third_party, encoding="utf-8")

    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOWORK="off")
    run(
        "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false",
        f"-ldflags=-s -w -X main.version={version}",
        "-o", str(directory / "djisimbox"), "./cmd/djisimbox", env=env,
    )
    files = ("djisimbox", *SOURCE_FILES, "THIRD_PARTY_NOTICES")
    archive = output / f"{name}.tar.gz"
    with archive.open("wb") as raw, gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode="w", format=tarfile.PAX_FORMAT) as tar:
            for relative in sorted(files):
                source = directory / relative
                info = tar.gettarinfo(str(source), arcname=f"{name}/{relative}")
                info.uid = info.gid = info.mtime = 0
                info.uname = info.gname = ""
                info.mode = 0o755 if relative == "djisimbox" else 0o644
                info.pax_headers = {}
                with source.open("rb") as content:
                    tar.addfile(info, content)

    # Inspect the actual archive and its extracted binary before checksumming it.
    with tarfile.open(archive, "r:gz") as tar:
        if sorted(tar.getnames()) != sorted(f"{name}/{relative}" for relative in files):
            raise ValueError(f"archive contents differ from allowlist: {name}")
        if any(not member.isfile() for member in tar):
            raise ValueError(f"archive contains a non-regular file: {name}")
        binary = directory / "checked-djisimbox"
        with binary.open("wb") as target, tar.extractfile(f"{name}/djisimbox") as source:
            shutil.copyfileobj(source, target)
        binary.chmod(0o755)
        check_binary(binary, arch, version)
    print(archive)
    return f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", help="v-prefixed SemVer tag or dev-<12 hex commit characters>")
    parser.add_argument("--output-dir", type=Path, default=ROOT / "dist", help="empty output directory")
    args = parser.parse_args()
    if not VERSION.fullmatch(args.version):
        parser.error("version must be v-prefixed SemVer or dev-<12 lowercase hex characters>")
    output = args.output_dir.resolve()
    if output.exists() and any(output.iterdir()):
        parser.error("output directory must be empty")
    output.mkdir(parents=True, exist_ok=True)
    os.environ["LC_ALL"] = "C"
    os.environ["GOWORK"] = "off"
    third_party = notices()
    with tempfile.TemporaryDirectory(prefix="djisimbox-release-") as temporary:
        checksums = "".join(
            package(arch, args.version, output, Path(temporary), third_party)
            for arch in ("arm64", "amd64")
        )
    (output / "SHA256SUMS").write_text(checksums, encoding="utf-8")


if __name__ == "__main__":
    main()
