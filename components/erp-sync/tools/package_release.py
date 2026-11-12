"""Offline deterministic source artifact; never imports the vendored project."""
from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import sys
import tarfile

ARCHIVE_SHA256 = "38a6858bde6b9e99bea5c19fd8309f5dd90058338ccee2f403792aefe16a7705"
MEMBERS = {
    "bi_agent/__init__.py": "9fc5e92cdb174ed298a2efb7e2aa50f08a3532895af07656e925a9710f63ce1d",
    "bi_agent/sync.py": "df55ce1c0b4287c28fa16a80fcab3870f0dddee0f87514cfbc83e614b5cb8a16",
    "bi_agent/config.py": "6bdfd850106607a54315ef18bf91ec1e274ffdf3c27cf8992b5c26904aeb726e",
    "bi_agent/data_quality.py": "6b903e60ca3a8251b9bc8f13231d2f9c18f24061ac2310ad0a7e129cf40b09d9",
    "bi_agent/kuaimai.py": "910fad1413ce981dea06394b3510998d1c5b8bf7c08f7e053e88115281ee0337",
    "bi_agent/metrics.py": "c2e5de6693f7533ff59c3e47f3c3364301848323ffaa0b9bde3df482a2b26eea",
    "bi_agent/sources.py": "3655326077b2982f9e02ea7cc49aa4efceff1775538237e8db3fad0e26d758a4",
    "bi_agent/catalog/__init__.py": "d41e6924ae73060acff0f70e48416c0e8adc811b301a3669cc4123b1337974d1",
    "bi_agent/catalog/models.py": "d74c9a92f573e8972d02528fecba8aa6d7d8737594a055dd72eea6d74ee079ce",
    "bi_agent/catalog/projection.py": "b6ae04b60740beaeba6ca397caf03de36de2e50108c73f3e4ae2bba055036daa",
    "bi_agent/catalog/repository.py": "c8ea5b488a96adaf5e4777d0c5b735065ce17abc8b5d3eb236064a4215755fd4",
    "pyproject.toml": "f01d438deab8a02c51cded1c560382a40378521911c56a8feed0c90ef78f4e19",
    "uv.lock": "2acc27c959019b0761726578111e7558992e8cf9632a7800b5c6e2fcf135ae43",
}


class InputError(Exception):
    def __init__(self, code, path):
        self.code, self.path = code, path
        super().__init__(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def json_bytes(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")


def read_regular(root, relative):
    path = root
    try:
        for part in Path(relative).parts:
            if part in ("..", ".") or Path(relative).is_absolute():
                raise InputError("SOURCE_INPUT_UNSAFE", relative)
            path = path / part
            info = path.lstat()
            if stat.S_ISLNK(info.st_mode):
                raise InputError("SOURCE_INPUT_UNSAFE", relative)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 2 * 1024 * 1024:
            raise InputError("SOURCE_INPUT_UNSAFE", relative)
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "rb") as stream:
            opened = os.fstat(stream.fileno())
            if (opened.st_dev, opened.st_ino, opened.st_nlink) != (info.st_dev, info.st_ino, 1):
                raise InputError("SOURCE_INPUT_UNSAFE", relative)
            data = stream.read(2 * 1024 * 1024 + 1)
            if len(data) > 2 * 1024 * 1024:
                raise InputError("SOURCE_INPUT_UNSAFE", relative)
            return data
    except FileNotFoundError:
        raise InputError("SOURCE_INPUT_MISSING", relative) from None
    except OSError:
        raise InputError("SOURCE_INPUT_UNSAFE", relative) from None


def collect_inputs(root):
    # All directories, including empty ones, must belong to the fixed tree.
    try:
        info = root.lstat()
    except FileNotFoundError:
        raise InputError("SOURCE_INPUT_MISSING", "component-root") from None
    if not stat.S_ISDIR(info.st_mode):
        raise InputError("SOURCE_INPUT_UNSAFE", "component-root")
    expected = set(MEMBERS)
    directories = {str(parent) for name in MEMBERS for parent in Path(name).parents if str(parent) != "."}
    vendor = root / "vendor"
    if vendor.is_symlink():
        raise InputError("SOURCE_INPUT_UNSAFE", "vendor")
    if not vendor.exists():
        raise InputError("SOURCE_INPUT_MISSING", "vendor")
    def walk_error(error):
        raise InputError("SOURCE_INPUT_UNSAFE", "vendor")
    for current, dirs, files in os.walk(vendor, followlinks=False, onerror=walk_error):
        for name in dirs + files:
            path = Path(current) / name
            rel = path.relative_to(vendor).as_posix()
            if path.is_symlink() or (rel not in expected and rel not in directories):
                raise InputError("SOURCE_INPUT_UNSAFE", "vendor/" + rel)
            if rel in directories and not path.is_dir():
                raise InputError("SOURCE_INPUT_UNSAFE", "vendor/" + rel)
    source_raw = read_regular(root, "source-input.json")
    try:
        source = json.loads(source_raw)
    except (ValueError, UnicodeError):
        raise InputError("SOURCE_INPUT_UNSAFE", "source-input.json") from None
    if not isinstance(source, dict) or not isinstance(source.get("members"), list):
        raise InputError("SOURCE_INPUT_UNSAFE", "source-input.json")
    if (source.get("source_archive_sha256") != ARCHIVE_SHA256 or
            source.get("source_archive_tracked") is not False or
            len(source["members"]) != len(MEMBERS)):
        raise InputError("SOURCE_INPUT_HASH_MISMATCH", "source-input.json")
    recorded = {}
    for member in source["members"]:
        if not isinstance(member, dict):
            raise InputError("SOURCE_INPUT_UNSAFE", "source-input.json")
        name = member.get("source_member_path")
        if name not in MEMBERS or name in recorded:
            raise InputError("SOURCE_INPUT_UNSAFE", "source-input.json")
        recorded[name] = member
        if (member.get("source_member_sha256") != MEMBERS[name] or
                member.get("input_sha256") != MEMBERS[name] or
                member.get("input_path") != "components/erp-sync/vendor/" + name or
                member.get("transformation") != "none"):
            raise InputError("SOURCE_INPUT_HASH_MISMATCH", "source-input.json")
    files, hashes = {}, {}
    for name, expected_hash in sorted(MEMBERS.items()):
        content = read_regular(root, "vendor/" + name)
        if digest(content) != expected_hash:
            raise InputError("SOURCE_INPUT_HASH_MISMATCH", "vendor/" + name)
        destination = name if name.startswith("bi_agent/") else "dependency-recipe/" + name
        files[destination] = content
        hashes["vendor/" + name] = digest(content)
    for name, destination in (("bi_agent/yanfa_bridge.py", "bi_agent/yanfa_bridge.py"),
                              ("tools/package_release.py", "dependency-recipe/package_release.py"),
                              ("README.md", "README.md")):
        content = read_regular(root, name)
        files[destination] = content
        hashes[name] = digest(content)
    files["source-input.json"] = source_raw
    hashes["source-input.json"] = digest(source_raw)
    return files, hashes


def build(root, release_ref):
    if not re.fullmatch(r"[0-9a-f]{40}", release_ref) or release_ref == "0" * 40:
        raise InputError("RELEASE_REF_INVALID", "release-ref")
    files, hashes = collect_inputs(root)
    recipe = {"schema_version": 1, "release_ref": release_ref,
              "candidate_version": "yanfa-bridge/0.1.0", "bridge_protocol": "yanfa-erp-bridge/1",
              "source_archive_sha256": ARCHIVE_SHA256, "inputs": hashes,
              "source_only": True, "formal_release_approved": False,
              "dependency_tool_version": "0.13.7",
              "dependency_export_argv": ["<approved_uv_absolute_path>", "export", "--frozen", "--no-dev",
                  "--no-emit-project", "--no-header", "--no-annotate"],
              "wheel_install_flags": ["--require-hashes", "--only-binary=:all:", "--no-deps", "--no-index"],
              "original_project_install": False}
    recipe_raw = json_bytes(recipe)
    files["build-recipe.json"] = recipe_raw
    buffer = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=buffer, compresslevel=9, mtime=0) as gz:
        with tarfile.open(fileobj=gz, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            dirs = {parent.as_posix() for name in files for parent in Path(name).parents
                    if parent.as_posix() != "."}
            for name in sorted(set(files) | dirs):
                info = tarfile.TarInfo(name + "/" if name in dirs else name)
                info.uid = info.gid = info.mtime = 0
                info.uname = info.gname = ""
                info.mode = 0o755 if name in dirs else 0o644
                if name in dirs:
                    info.type = tarfile.DIRTYPE
                    archive.addfile(info)
                else:
                    info.size = len(files[name])
                    archive.addfile(info, io.BytesIO(files[name]))
    artifact = buffer.getvalue()
    receipt = {"schema_version": 1, "release_ref": release_ref,
               "artifact_file": "yanfa-erp-bridge-source.tar.gz", "artifact_sha256": digest(artifact),
               "recipe_sha256": digest(recipe_raw), "source_archive_sha256": ARCHIVE_SHA256,
               "source_inputs": hashes, "formal_release_approved": False,
               "runtime_installed": False, "wheel_hashes_verified": False}
    return artifact, receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-ref", required=True)
    parser.add_argument("--output", type=Path, required=True, help="new or empty output directory")
    args = parser.parse_args()
    try:
        root = Path(__file__).absolute().parent.parent
        artifact, receipt = build(root, args.release_ref)
        args.output.mkdir(parents=True, exist_ok=True)
        with (args.output / receipt["artifact_file"]).open("xb") as stream:
            stream.write(artifact)
        with (args.output / "build-receipt.json").open("xb") as stream:
            stream.write(json_bytes(receipt))
        print(json.dumps(receipt, sort_keys=True))
        return 0
    except InputError as exc:
        print(exc.code + ": " + exc.path, file=sys.stderr)
    except (OSError, ValueError, TypeError, AttributeError):
        print("SOURCE_INPUT_UNSAFE", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
