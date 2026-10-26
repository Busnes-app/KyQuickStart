#!/usr/bin/env python3
"""Reject empty or compare-link-only release notes; accuracy still needs review."""
import re
import sys
from pathlib import Path


def require_summary(notes):
    # Contributor credits and the compare URL are useful, but are not a change summary.
    visible = re.sub(r"<!--.*?(?:-->|$)", "", notes, flags=re.DOTALL)
    changes = re.split(r"(?mi)^## New Contributors\b", visible)[0]
    for line in changes.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if re.match(r"(?:\*\*)?Full Changelog(?:\*\*)?:", line, re.IGNORECASE):
            continue
        if re.fullmatch(r"https?://\S+|\[[^\]]+\]\([^)]+\)", line):
            continue
        if any(char.isalpha() for char in line):
            return
    raise ValueError("add a human-readable changes summary; headers, contributor credits and compare links alone are insufficient")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: check-release-notes.py NOTES_FILE")
    try:
        require_summary(Path(sys.argv[1]).read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        sys.exit(f"release notes: {error}")
