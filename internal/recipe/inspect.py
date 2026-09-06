"""Prints a JSON manifest of a cloned repository for the detectors.
Usage: python3 inspect.py <repo-dir>. Compatible with Python 3.8+.
Reads only small, well-known files; never executes anything."""
import json
import os
import re
import sys

CAP = 6000


def read(path, cap=CAP):
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as f:
            return f.read(cap)
    except OSError:
        return ""


def main():
    root = sys.argv[1] if len(sys.argv) > 1 else "."
    try:
        names = sorted(os.listdir(root))
    except OSError:
        names = []
    files = [n for n in names if not n.startswith(".git")]
    cmd_dir = os.path.join(root, "cmd")
    cmd_dirs = sorted(d for d in os.listdir(cmd_dir) if os.path.isdir(os.path.join(cmd_dir, d))) if os.path.isdir(cmd_dir) else []
    m = {
        "files": files,
        "cmd_dirs": cmd_dirs,
        "lockfiles": [n for n in files if n in ("package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "bun.lock", "poetry.lock", "uv.lock", "Pipfile.lock")],
        "make_targets": [],
        "has_index_html": "index.html" in files,
    }
    pj = os.path.join(root, "package.json")
    if os.path.isfile(pj):
        try:
            with open(pj, encoding="utf-8") as f:
                data = json.load(f)
            m["package_json"] = {
                "name": data.get("name", ""),
                "scripts": data.get("scripts") or {},
                "dependencies": data.get("dependencies") or {},
                "devDependencies": data.get("devDependencies") or {},
                "packageManager": data.get("packageManager", ""),
            }
        except (OSError, ValueError):
            pass
    for key, name in (("pyproject", "pyproject.toml"), ("requirements", "requirements.txt"), ("go_mod", "go.mod"),
                      ("cargo_toml", "Cargo.toml"), ("procfile", "Procfile"), ("trylive_yaml", "trylive.yaml"),
                      ("dockerfile", "Dockerfile")):
        if name in files:
            m[key] = read(os.path.join(root, name), 4000)
    if "Makefile" in files:
        text = read(os.path.join(root, "Makefile"), 20000)
        m["make_targets"] = sorted(set(re.findall(r"^([A-Za-z0-9_-]+):(?!=)", text, re.M)))
    for readme in ("README.md", "readme.md", "README.rst", "README", "README.txt"):
        if readme in files:
            m["readme"] = read(os.path.join(root, readme))
            break
    sys.stdout.write(json.dumps(m))


if __name__ == "__main__":
    main()
