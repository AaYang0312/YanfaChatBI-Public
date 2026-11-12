from __future__ import annotations

import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).absolute().parent.parent
SPEC = importlib.util.spec_from_file_location("release_packager", ROOT / "tools/package_release.py")
packager = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(packager)
CANDIDATE = "e8519b1b590b6160085c0cbae9f3d3a3e13ed9e5"  # test identity only, never a released artifact


class ReleaseInputTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="yanfa-release-input-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.root = self.directory / "checkout"
        self.root.mkdir()
        shutil.copytree(ROOT / "vendor", self.root / "vendor")
        for name in ("source-input.json", "bi_agent/yanfa_bridge.py", "tools/package_release.py", "README.md"):
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)

    def test_repeated_artifact_bytes_ignore_filesystem_metadata(self):
        first, receipt = packager.build(self.root, CANDIDATE)
        for name in packager.MEMBERS:
            os.utime(self.root / "vendor" / name, (123456789, 123456789))
        second, other = packager.build(self.root, CANDIDATE)
        self.assertEqual(first, second)
        self.assertEqual(receipt, other)
        self.assertEqual(receipt["artifact_sha256"], hashlib.sha256(first).hexdigest())
        self.assertNotEqual(receipt["artifact_sha256"], packager.ARCHIVE_SHA256)
        self.assertNotEqual(receipt["recipe_sha256"], packager.ARCHIVE_SHA256)
        with tarfile.open(fileobj=io.BytesIO(first), mode="r:gz") as archive:
            names = archive.getnames()
            self.assertEqual(names, sorted(names))
            self.assertNotIn("pyproject.toml", names)
            self.assertIn("dependency-recipe/uv.lock", names)
            self.assertNotIn("bi_agent/monitoring/cli.py", names)
            recipe = archive.extractfile("build-recipe.json").read()
            self.assertEqual(hashlib.sha256(recipe).hexdigest(), receipt["recipe_sha256"])
            for entry in archive.getmembers():
                self.assertEqual((entry.uid, entry.gid, entry.mtime, entry.uname, entry.gname), (0, 0, 0, "", ""))
                self.assertEqual(entry.mode, 0o755 if entry.isdir() else 0o644)
        self.assertEqual(first[4:8], b"\0\0\0\0")
        changed, _ = packager.build(self.root, "b" * 40)
        self.assertNotEqual(first, changed)

    def test_missing_changed_extra_and_linked_inputs_fail_explicitly(self):
        target = self.root / "vendor/bi_agent/sync.py"
        original = target.read_bytes()
        target.unlink()
        with self.assertRaises(packager.InputError) as error:
            packager.build(self.root, CANDIDATE)
        self.assertEqual(error.exception.code, "SOURCE_INPUT_MISSING")
        target.write_bytes(original + b"\n")
        with self.assertRaises(packager.InputError) as error:
            packager.build(self.root, CANDIDATE)
        self.assertEqual(error.exception.code, "SOURCE_INPUT_HASH_MISMATCH")
        target.unlink()
        external = self.directory / "outside.py"
        external.write_bytes(original)
        target.symlink_to(external)
        with self.assertRaises(packager.InputError) as error:
            packager.build(self.root, CANDIDATE)
        self.assertEqual(error.exception.code, "SOURCE_INPUT_UNSAFE")
        target.unlink()
        os.link(external, target)
        with self.assertRaises(packager.InputError) as error:
            packager.build(self.root, CANDIDATE)
        self.assertEqual(error.exception.code, "SOURCE_INPUT_UNSAFE")
        target.unlink()
        target.write_bytes(original)
        extra = self.root / "vendor/unapproved.py"
        extra.write_bytes(b"raise RuntimeError('must never import')\n")
        with self.assertRaises(packager.InputError) as error:
            packager.build(self.root, CANDIDATE)
        self.assertEqual(error.exception.code, "SOURCE_INPUT_UNSAFE")

    def test_manifest_cannot_authorize_new_source_bytes(self):
        target = self.root / "vendor/bi_agent/config.py"
        target.write_bytes(target.read_bytes() + b"\n")
        manifest_path = self.root / "source-input.json"
        manifest = json.loads(manifest_path.read_text())
        member = next(item for item in manifest["members"] if item["source_member_path"] == "bi_agent/config.py")
        member["input_sha256"] = hashlib.sha256(target.read_bytes()).hexdigest()
        manifest_path.write_text(json.dumps(manifest))
        with self.assertRaises(packager.InputError) as error:
            packager.build(self.root, CANDIDATE)
        self.assertEqual(error.exception.code, "SOURCE_INPUT_HASH_MISMATCH")

    def test_minimal_artifact_imports_from_clean_cwd_without_source_fallback(self):
        artifact, _ = packager.build(self.root, CANDIDATE)
        install = self.directory / "minimal-site-packages"
        install.mkdir()
        with tarfile.open(fileobj=io.BytesIO(artifact), mode="r:gz") as archive:
            archive.extractall(install, filter="data")
        scratch = self.directory / "clean-cwd"
        scratch.mkdir()
        code = r'''
import importlib.util, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
sys.path.insert(0, str(root))
from bi_agent import sync, yanfa_bridge, kuaimai
from zoneinfo import ZoneInfo
import psycopg
expected = ("bi_agent", "bi_agent.sync", "bi_agent.config", "bi_agent.data_quality",
            "bi_agent.kuaimai", "bi_agent.metrics", "bi_agent.sources", "bi_agent.catalog",
            "bi_agent.catalog.models", "bi_agent.catalog.projection", "bi_agent.catalog.repository",
            "bi_agent.yanfa_bridge")
for name in expected:
    origin = pathlib.Path(sys.modules[name].__file__)
    if not origin.is_relative_to(root):
        raise RuntimeError("source fallback: " + name)
for name in ("bi_agent.monitoring", "bi_agent.business_query", "bi_agent.agent"):
    if importlib.util.find_spec(name) is not None:
        raise RuntimeError("unexpected full application module")
assert ZoneInfo("Asia/Shanghai").key == "Asia/Shanghai"
assert psycopg.pq.version() >= 100000
page = kuaimai.parse_page({"list": [], "total": 0})
assert page.verified_empty
print(json.dumps({"origins": list(expected), "candidate": yanfa_bridge.VERSION, "complete": True}))
'''
        env = {key: value for key, value in os.environ.items() if key not in ("PYTHONPATH", "PYTHONHOME")}
        result = subprocess.run([sys.executable, "-I", "-B", "-c", code, str(install)],
                                cwd=scratch, env=env, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace"))
        self.assertTrue(json.loads(result.stdout)["complete"])

    def test_cli_produces_artifact_and_separate_real_receipt(self):
        output = self.directory / "output"
        result = subprocess.run([sys.executable, "-I", "-B", str(self.root / "tools/package_release.py"),
                                 "--release-ref", CANDIDATE, "--output", str(output)],
                                cwd=self.directory, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr.decode())
        receipt = json.loads((output / "build-receipt.json").read_bytes())
        artifact = (output / receipt["artifact_file"]).read_bytes()
        self.assertEqual(receipt["artifact_sha256"], hashlib.sha256(artifact).hexdigest())
        self.assertFalse(receipt["runtime_installed"])
        self.assertFalse(receipt["wheel_hashes_verified"])
        missing = self.root / "vendor/uv.lock"
        missing.unlink()
        failed = subprocess.run([sys.executable, "-I", "-B", str(self.root / "tools/package_release.py"),
                                 "--release-ref", CANDIDATE, "--output", str(self.directory / "missing-output")],
                                cwd=self.directory, capture_output=True, timeout=30)
        self.assertNotEqual(failed.returncode, 0)
        self.assertIn(b"SOURCE_INPUT_MISSING", failed.stderr)
        self.assertFalse((self.directory / "missing-output").exists())


if __name__ == "__main__":
    unittest.main()
