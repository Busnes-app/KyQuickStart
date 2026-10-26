"""Exercise the release publication boundary without calling GitHub or publishing."""
import runpy
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

CHECKER = Path(__file__).with_name("check-release-notes.py")
require_summary = runpy.run_path(str(CHECKER))["require_summary"]


class ReleaseNotesTests(unittest.TestCase):
    def test_refuses_missing_change_summaries(self):
        for body in ["", "  \n", "<!--\nTODO: write notes\n-->", "## What's Changed\n", "**Full Changelog**: https://example.invalid/compare/v1...v2",
                     "https://example.invalid/compare/v1...v2", "[Full Changelog](https://example.invalid/compare/v1...v2)",
                     "## What's Changed\n\n## New Contributors\n* First contribution by @example\n"]:
            with self.subTest(body=body), self.assertRaises(ValueError):
                require_summary(body)

    def test_accepts_generated_pr_summaries_and_authored_notes(self):
        for body in ["## What's Changed\n* Fix credential redaction by @example in https://example.invalid/pull/1\n",
                     "Corrects reverse-proxy trust. Review your edge address before applying.\n",
                     "修复凭据日志泄漏；升级前检查配置。"]:
            with self.subTest(body=body):
                require_summary(body)

    def test_cli_refuses_before_publication_and_accepts_the_repair(self):
        with tempfile.TemporaryDirectory() as directory:
            notes = Path(directory, "notes.md")
            notes.write_text("**Full Changelog**: https://example.invalid/compare/v1...v2", encoding="utf-8")
            result = subprocess.run([sys.executable, str(CHECKER), str(notes)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("changes summary", result.stderr)
            notes.write_text("Fixes owner-only volume permissions; review before applying.\n", encoding="utf-8")
            result = subprocess.run([sys.executable, str(CHECKER), str(notes)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
