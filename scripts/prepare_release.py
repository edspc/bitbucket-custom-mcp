#!/usr/bin/env python3
"""Update the source version before the manual release workflow commits it."""
import pathlib
import re
import sys


def prepare(version, root):
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version):
        raise ValueError("version must be MAJOR.MINOR.PATCH without v or leading zeroes")
    cli = root / "cmd/server/cli.go"
    text = cli.read_text()
    match = re.search(r'^const version = "([0-9]+\.[0-9]+\.[0-9]+)"$', text, re.M)
    if match is None:
        raise ValueError("source version not found")
    old = match[1]
    if tuple(map(int, version.split('.'))) <= tuple(map(int, old.split('.'))):
        raise ValueError("new version must be greater than the source version")
    readme = root / "README.md"
    documentation = readme.read_text()
    cli.write_text(text[:match.start(1)] + version + text[match.end(1):])
    readme.write_text(documentation.replace(old, version))


if __name__ == '__main__':
    try:
        if len(sys.argv) != 2:
            raise ValueError("usage: prepare-release.py MAJOR.MINOR.PATCH")
        prepare(sys.argv[1], pathlib.Path(__file__).resolve().parent.parent)
    except ValueError as error:
        sys.exit(str(error))
