#!/usr/bin/env python3
"""Render sticky-dvr config templates.

Usage:
    python3 scripts/configure.py --out dist/docker [--local config.local.yaml]

If --local is omitted, config.local.yaml is merged automatically if it exists.
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
DEFAULT_LOCAL = REPO_ROOT / "config.local.yaml"


def deep_merge(base, override):
    """Recursively merge override into base. Returns base."""
    for key, val in override.items():
        if key in base and isinstance(base[key], dict) and isinstance(val, dict):
            deep_merge(base[key], val)
        else:
            base[key] = val
    return base


def load_config(local_path=None):
    with open(BASE_CONFIG) as f:
        config = yaml.safe_load(f)

    if local_path is not None:
        # Explicit path — error if missing
        p = Path(local_path)
        if not p.exists():
            print(f"error: local config not found: {p}", file=sys.stderr)
            sys.exit(1)
        with open(p) as f:
            deep_merge(config, yaml.safe_load(f) or {})
    elif DEFAULT_LOCAL.exists():
        # Auto-detect
        with open(DEFAULT_LOCAL) as f:
            deep_merge(config, yaml.safe_load(f) or {})

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
        "--local",
        metavar="FILE",
        help="Local config to merge over config.yaml (default: config.local.yaml if present)",
    )
    args = parser.parse_args()

    config = load_config(local_path=args.local)

    print(f"Rendering templates → {args.out}/")
    render_templates(config, args.out)


if __name__ == "__main__":
    main()
