import copy
import hashlib
import io
import json
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

import release_image


class ReleaseArchive(unittest.TestCase):
    def setUp(self):
        self.metadata = {
            "source_commit": "abc123", "go_toolchain": "go version go1.26.2 linux/amd64",
            "node_toolchain": "v24.15.0", "frontend_version": "test-version",
            "migrations": [{"path": "internal/platform/pg/migrations/00001.sql", "sha256": "abc"}],
        }

    def archive(self, path, metadata, corrupt=None):
        blobs = {}

        def blob(value):
            data = json.dumps(value).encode()
            digest = hashlib.sha256(data).hexdigest()
            blobs["blobs/sha256/" + digest] = data
            return {"digest": "sha256:" + digest, "size": len(data)}

        config = blob({"os": "linux", "architecture": "amd64", "config": {"Labels": release_image.labels(metadata)}})
        layer = blob("layer bytes")
        manifest = blob({"config": config, "layers": [layer]})
        blobs["index.json"] = json.dumps({"manifests": [manifest]}).encode()
        if corrupt:
            blobs["blobs/sha256/" + {"manifest": manifest, "config": config, "layer": layer}[corrupt]["digest"][7:]] += b"!"
        with tarfile.open(path, "w") as archive:
            for name, data in blobs.items():
                entry = tarfile.TarInfo(name)
                entry.size = len(data)
                archive.addfile(entry, io.BytesIO(data))
        return manifest["digest"]

    def test_round_trip_and_digest_pin(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "image.tar"
            digest = self.archive(path, self.metadata)
            self.assertEqual(release_image.verify(path, self.metadata), digest)
            self.assertEqual(release_image.verify(path, {**self.metadata, "image_digest": digest}), digest)
            with self.assertRaisesRegex(ValueError, "digest mismatch"):
                release_image.verify(path, {**self.metadata, "image_digest": "sha256:" + "0" * 64})

    def test_verification_requires_the_recorded_digest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            path = root / "image.tar"
            self.archive(path, self.metadata)
            metadata = root / "release.json"
            metadata.write_text(json.dumps(self.metadata))
            script = Path(release_image.__file__)
            result = subprocess.run([sys.executable, str(script), "verify", str(metadata), str(path)],
                                    text=True, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("missing image_digest", result.stderr)
            subprocess.run([sys.executable, str(script), "seal", str(metadata), str(path)], check=True, capture_output=True)
            subprocess.run([sys.executable, str(script), "verify", str(metadata), str(path)], check=True, capture_output=True)

    def test_each_provenance_field_is_bound_to_the_image(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "image.tar"
            self.archive(path, self.metadata)
            for key in self.metadata:
                with self.subTest(field=key):
                    changed = copy.deepcopy(self.metadata)
                    changed[key] = [] if key == "migrations" else "different"
                    with self.assertRaisesRegex(ValueError, "label mismatch"):
                        release_image.verify(path, changed)

    def test_corrupted_manifest_config_and_layers_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "image.tar"
            for part in ("manifest", "config", "layer"):
                with self.subTest(part=part):
                    self.archive(path, self.metadata, corrupt=part)
                    with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                        release_image.verify(path, self.metadata)


if __name__ == "__main__":
    unittest.main()
