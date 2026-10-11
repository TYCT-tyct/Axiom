import tempfile
import unittest
from pathlib import Path

from check_docs import check


def run_check(files):
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        for name, text in files.items():
            (root / name).parent.mkdir(parents=True, exist_ok=True)
            (root / name).write_text(text, encoding="utf-8")
        return check(root)


class CheckDocsTest(unittest.TestCase):
    def test_valid_link_forms_pass(self):
        errors = run_check({
            "README.md": "",
            "docs/a.md": "",
            "docs/a b.md": "",
            "docs/README.md": (
                "[rel](a.md) [angle](<a b.md>) [root](/README.md) [anchor](a.md#x) "
                "[same](#x) [web](https://example.com) [mail](mailto:x@example.com)\n"
            ),
        })
        self.assertEqual(errors, [])

    def test_links_in_code_are_ignored(self):
        errors = run_check({
            "docs/README.md": (
                "`[a](missing.md)` and ``[b](missing.md)``\n"
                "````\n```\n[c](missing.md)\n```\n[d](missing.md)\n````\n"
                "~~~\n[e](missing.md)\n~~~\n"
            ),
        })
        self.assertEqual(errors, [])

    def test_broken_and_unmapped_docs_are_reported(self):
        errors = run_check({
            "docs/README.md": "[inline](missing.md)\n\n[ref]: gone.md\n",
            "docs/orphan.md": "",
        })
        self.assertEqual(errors, [
            "docs/README.md:1: broken link to docs/missing.md",
            "docs/README.md:3: broken link to docs/gone.md",
            "docs/orphan.md: not listed in docs/README.md",
        ])


if __name__ == "__main__":
    unittest.main()
