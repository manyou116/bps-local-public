#!/usr/bin/env python3
"""Check that a standalone source checkout contains no private deployment files."""

from pathlib import Path, PurePosixPath
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
TOP_FILES = {
    ".gitattributes", ".gitignore", "LICENSE", "README.md", "go.mod", "go.sum",
    "bps.ps1", "bps-codex.ps1", "oauth.example.json",
}
PACKAGES = {"auth", "basispoints", "gateway", "proxy", "retryafter", "sanitize"}
DOCS = {"USAGE.md", "BPS-PROTOCOL-DIAGNOSTIC.md"}
SCRIPTS = {"package.sh", "check-standalone-boundary.py"}
PRIVATE_IMPORTS = (
    "github.com/router-for-me/CLIProxyAPI",
    "/cpa-plugin/", "/internal/gatewaywire", "/internal/stateprobe",
    "/internal/qualityprobe", "/internal/accountusage",
)


def allowed(path):
    parts = PurePosixPath(path).parts
    if len(parts) == 1:
        return path in TOP_FILES
    if len(parts) == 3 and parts[:2] == (".github", "workflows"):
        return parts[2] == "ci.yml"
    if len(parts) == 3 and parts[:2] == ("cmd", "bps-local"):
        return parts[2].endswith(".go")
    if len(parts) == 3 and parts[0] == "internal" and parts[1] in PACKAGES:
        return parts[2].endswith(".go")
    if len(parts) == 2 and parts[0] == "docs":
        return parts[1] in DOCS
    if len(parts) == 2 and parts[0] == "scripts":
        return parts[1] in SCRIPTS
    return False


def main():
    result = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=ROOT, check=True, stdout=subprocess.PIPE,
    )
    paths = sorted(set(result.stdout.decode().strip("\0").split("\0")))
    problems = []
    for name in paths:
        if not name:
            continue
        path = ROOT / name
        if not allowed(name):
            problems.append(f"unexpected distribution path: {name}")
            continue
        if path.is_symlink():
            problems.append(f"symlink is not a standalone source file: {name}")
            continue
        if not path.exists():  # A staged deletion is absent from the next commit.
            continue
        if path.suffix == ".go" or path.name == "go.mod":
            source = path.read_text(encoding="utf-8")
            if any(marker in source for marker in PRIVATE_IMPORTS):
                problems.append(f"private module reference: {name}")
    if problems:
        print("\n".join(problems), file=sys.stderr)
        return 1
    print(f"Standalone source boundary passed ({len(paths)} files).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
