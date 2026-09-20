#!/usr/bin/env python3
"""Render ```mermaid blocks in a markdown file to PNG images via mermaid.ink,
then rewrite the markdown to reference the images. Pandoc cannot render
mermaid natively for docx output, so we pre-render.

Usage: render-mermaid.py <input.md> <output.md> <img_dir>
"""
import base64
import os
import re
import sys
import urllib.request
import urllib.parse
from pathlib import Path

MERMAID_RE = re.compile(r"```mermaid\n(.*?)\n```", re.DOTALL)


def render(code: str, dest: Path) -> bool:
    encoded = base64.urlsafe_b64encode(code.encode("utf-8")).decode("ascii")
    # PNG, white background, wide canvas for crisp scaling in docx.
    qs = urllib.parse.urlencode({"type": "png", "bgColor": "white", "width": 1600})
    url = f"https://mermaid.ink/img/{encoded}?{qs}"
    try:
        req = urllib.request.Request(url, headers={"User-Agent": "aegiscrawler-docs/1.0"})
        with urllib.request.urlopen(req, timeout=60) as r:
            data = r.read()
        dest.write_bytes(data)
        return True
    except Exception as e:
        sys.stderr.write(f"  FAIL render -> {dest.name}: {e}\n")
        return False


def main(in_path: str, out_path: str, img_dir: str) -> int:
    src = Path(in_path).read_text(encoding="utf-8")
    img_root = Path(img_dir)
    img_root.mkdir(parents=True, exist_ok=True)
    # Relative path from the output markdown file's directory to img_dir.
    out_dir = Path(out_path).resolve().parent
    rel_root = os.path.relpath(str(img_root.resolve()), str(out_dir))

    counter = [0]
    failures = [0]

    def replace(match: re.Match) -> str:
        counter[0] += 1
        idx = counter[0]
        code = match.group(1)
        dest = img_root / f"mermaid-{idx:02d}.png"
        sys.stderr.write(f"  [{idx:02d}] rendering ({len(code)} chars)...\n")
        if render(code, dest):
            rel = f"{rel_root}/mermaid-{idx:02d}.png"
            return f"![图 {idx}]({rel})"
        failures[0] += 1
        # Fall back to a fenced code block so content is not lost.
        return f"```mermaid\n{code}\n```"

    result = MERMAID_RE.sub(replace, src)
    Path(out_path).write_text(result, encoding="utf-8")
    sys.stderr.write(
        f"\nDone: rendered {counter[0] - failures[0]}/{counter[0]} diagrams, "
        f"{failures[0]} failures.\n"
    )
    return 0 if failures[0] == 0 else 1


if __name__ == "__main__":
    if len(sys.argv) != 4:
        sys.stderr.write(
            "usage: render-mermaid.py <input.md> <output.md> <img_dir>\n"
        )
        sys.exit(2)
    sys.exit(main(sys.argv[1], sys.argv[2], sys.argv[3]))
