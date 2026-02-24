#!/usr/bin/env python3
"""Render sticky-dvr config templates.

Usage:
    python3 scripts/configure.py --out dist/docker [--merge-local]

Env vars:
    MERGE_LOCAL=1   equivalent to --merge-local
"""

import argparse
import os
import sys
from pathlib import Path

import yaml
from jinja2 import Environment, FileSystemLoader

SCRIPT_DIR = Path(__file__).parent
REPO_ROOT = SCRIPT_DIR.parent

TEMPLATES_DIR = REPO_ROOT / "config-templates"
BASE_CONFIG = REPO_ROOT / "config.yaml"
LOCAL_CONFIG = REPO_ROOT / "config.local.yaml"


def deep_merge(base, override):
    """Recursively merge override into base. Returns base."""
    for key, val in override.items():
        if key in base and isinstance(base[key], dict) and isinstance(val, dict):
            deep_merge(base[key], val)
        else:
            base[key] = val
    return base


def load_config(merge_local=False):
    with open(BASE_CONFIG) as f:
        config = yaml.safe_load(f)

    if merge_local:
        if LOCAL_CONFIG.exists():
            with open(LOCAL_CONFIG) as f:
                local = yaml.safe_load(f) or {}
            deep_merge(config, local)
        else:
            print(
                f"warning: --merge-local set but {LOCAL_CONFIG} not found",
                file=sys.stderr,
            )

    return config


def render_templates(config, out_dir):
    out_path = Path(out_dir)
    env = Environment(
        loader=FileSystemLoader(str(TEMPLATES_DIR)),
        keep_trailing_newline=True,
    )

    for template_path in sorted(TEMPLATES_DIR.rglob("*.j2")):
        rel = template_path.relative_to(TEMPLATES_DIR)
        out_rel = Path(str(rel)[:-3])  # strip .j2
        dest = out_path / out_rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        template = env.get_template(str(rel))
        rendered = template.render(config=config)
        dest.write_text(rendered)
        print(f"  {dest}")


def main():
    parser = argparse.ArgumentParser(description="Render sticky-dvr config templates")
    parser.add_argument("--out", default="dist/docker", help="Output directory")
    parser.add_argument(
        "--merge-local",
        action="store_true",
        help="Merge config.local.yaml over config.yaml",
    )
    args = parser.parse_args()

    merge_local = args.merge_local or os.environ.get("MERGE_LOCAL") == "1"
    config = load_config(merge_local=merge_local)

    print(f"Rendering templates → {args.out}/")
    render_templates(config, args.out)


if __name__ == "__main__":
    main()
