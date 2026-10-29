"""Record release inputs and verify the OCI archive before retaining it in CI."""

import argparse
import hashlib
import json
import re
import subprocess
import tarfile
from pathlib import Path


LABELS = {
    "source_commit": "org.opencontainers.image.revision",
    "go_toolchain": "io.axiom.go-toolchain",
    "node_toolchain": "io.axiom.node-toolchain",
    "frontend_version": "org.opencontainers.image.version",
    "migrations": "io.axiom.migrations",
}


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def inputs(root):
    return {
        "source_commit": command("git", "rev-parse", "HEAD"),
        "go_toolchain": command("go", "version"),
        "node_toolchain": command("node", "--version"),
        "frontend_version": command("just", "axiom-version"),
        "migrations": [
            {"path": p.relative_to(root).as_posix(), "sha256": hashlib.sha256(p.read_bytes()).hexdigest()}
            for p in sorted(root.glob("internal/**/migrations/*.sql"))
        ],
    }


def labels(metadata):
    return {label: json.dumps(metadata[key], separators=(",", ":")) if key == "migrations" else metadata[key]
            for key, label in LABELS.items()}


def verify(archive, metadata):
    with tarfile.open(archive) as image:
        def read(name):
            return image.extractfile(name).read()

        def blob(descriptor):
            digest = descriptor["digest"]
            if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
                raise ValueError("invalid OCI digest")
            data = read("blobs/sha256/" + digest.split(":")[1])
            if len(data) != descriptor["size"] or hashlib.sha256(data).hexdigest() != digest.split(":")[1]:
                raise ValueError("OCI blob checksum mismatch")
            return data

        index = json.loads(read("index.json"))
        if len(index["manifests"]) != 1:
            raise ValueError("expected a single release manifest")
        descriptor = index["manifests"][0]
        manifest = json.loads(blob(descriptor))
        config = json.loads(blob(manifest["config"]))
        for layer in manifest["layers"]:
            blob(layer)
        for name, expected in labels(metadata).items():
            if config["config"]["Labels"].get(name) != expected:
                raise ValueError("release label mismatch: " + name)
        if (config["os"], config["architecture"]) != ("linux", "amd64"):
            raise ValueError("expected linux/amd64 release")
        digest = descriptor["digest"]
        if metadata.get("image_digest", digest) != digest:
            raise ValueError("release digest mismatch")
        return digest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("inputs", "labels", "seal", "verify"))
    parser.add_argument("metadata", type=Path)
    parser.add_argument("archive", nargs="?", type=Path)
    args = parser.parse_args()
    if args.action == "inputs":
        args.metadata.write_text(json.dumps(inputs(Path.cwd()), indent=2) + "\n")
        return
    metadata = json.loads(args.metadata.read_text())
    if args.action == "labels":
        for name, value in labels(metadata).items():
            print(name + "=" + value)
        return
    if not args.archive:
        parser.error("archive is required")
    if args.action == "verify" and "image_digest" not in metadata:
        parser.error("release metadata is missing image_digest")
    digest = verify(args.archive, metadata)
    if args.action == "seal":
        metadata["image_digest"] = digest
        metadata["platform"] = "linux/amd64"
        args.metadata.write_text(json.dumps(metadata, indent=2) + "\n")
    print(digest)


if __name__ == "__main__":
    main()
