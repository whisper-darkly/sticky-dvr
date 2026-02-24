#!/usr/bin/env python3
"""Render sticky-dvr config templates.

Usage:
    python3 scripts/configure.py --out dist/docker [--local config.local.yaml]

If --local is omitted, config.local.yaml is merged automatically if it exists.

Secrets
-------
Paths listed in SECRET_PATHS are auto-generated if absent or empty in
.secrets.yaml (untracked). On each run:

  1. .secrets.yaml  — generated secrets, base layer
  2. config.yaml    — tracked defaults, merged on top
  3. local config   — machine overrides, merged on top

Any value present in a higher layer wins. Secrets already in .secrets.yaml
are reused as-is, so they stay stable across runs.

A .env file is also written to the output directory for docker compose.
"""

import argparse
import copy
import os
import sys
from pathlib import Path

import yaml
from jinja2 import Environment, FileSystemLoader

try:
    import secrets as _secrets
except ImportError:
    print("error: Python 3.6+ required (secrets module missing)", file=sys.stderr)
    sys.exit(1)

SCRIPT_DIR = Path(__file__).parent
REPO_ROOT = SCRIPT_DIR.parent

TEMPLATES_DIR = REPO_ROOT / "config-templates"
BASE_CONFIG = REPO_ROOT / "config.yaml"
DEFAULT_LOCAL = REPO_ROOT / "config.local.yaml"
SECRETS_FILE = REPO_ROOT / ".secrets.yaml"

# Dot-notation paths → generation spec.
# type "hex"     → secrets.token_hex(bytes)      e.g. 32 bytes = 64-char hex
# type "urlsafe" → secrets.token_urlsafe(bytes)  e.g. 18 bytes ≈ 24-char url-safe
SECRET_PATHS = {
    "secrets.jwt_secret":     {"type": "hex",     "bytes": 32},
    "secrets.db_admin_pass":  {"type": "urlsafe", "bytes": 18},
    "secrets.db_app_pass":    {"type": "urlsafe", "bytes": 18},
    "secrets.admin_password": {"type": "urlsafe", "bytes": 18},
}


def generate_secret(spec):
    if spec["type"] == "hex":
        return _secrets.token_hex(spec["bytes"])
    if spec["type"] == "urlsafe":
        return _secrets.token_urlsafe(spec["bytes"])
    raise ValueError(f"unknown secret type: {spec['type']}")


def get_nested(d, path):
    for key in path.split("."):
        if not isinstance(d, dict):
            return None
        d = d.get(key)
    return d


def set_nested(d, path, value):
    keys = path.split(".")
    for key in keys[:-1]:
        d = d.setdefault(key, {})
    d[keys[-1]] = value


def deep_merge(base, override):
    """Recursively merge override into base. Returns base."""
    for key, val in override.items():
        if key in base and isinstance(base[key], dict) and isinstance(val, dict):
            deep_merge(base[key], val)
        else:
            base[key] = val
    return base


def ensure_secrets():
    """Load .secrets.yaml, generate any missing/empty secrets, persist, return dict."""
    existing = {}
    if SECRETS_FILE.exists():
        with open(SECRETS_FILE) as f:
            existing = yaml.safe_load(f) or {}

    changed = False
    for path, spec in SECRET_PATHS.items():
        if not get_nested(existing, path):
            set_nested(existing, path, generate_secret(spec))
            changed = True

    if changed:
        with open(SECRETS_FILE, "w") as f:
            yaml.dump(existing, f, default_flow_style=False, allow_unicode=True)
        print(f"  generated secrets → {SECRETS_FILE.relative_to(REPO_ROOT)}")

    return existing


def load_config(local_path=None):
    # Layer 1: generated secrets (base)
    config = copy.deepcopy(ensure_secrets())

    # Layer 2: tracked config.yaml
    with open(BASE_CONFIG) as f:
        deep_merge(config, yaml.safe_load(f) or {})

    # Layer 3: local override
    if local_path is not None:
        p = Path(local_path)
        if not p.exists():
            print(f"error: local config not found: {p}", file=sys.stderr)
            sys.exit(1)
        with open(p) as f:
            deep_merge(config, yaml.safe_load(f) or {})
    elif DEFAULT_LOCAL.exists():
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


def write_env(config, out_dir):
    """Write .env for docker compose — secrets and derived connection strings."""
    s = config.get("secrets", {})
    db = config.get("db", {})

    user = db.get("user", "sticky")
    host = db.get("host", "postgres")
    port = db.get("port", 5432)
    name = db.get("name", "sticky")

    lines = [
        f"JWT_SECRET={s.get('jwt_secret', '')}",
        f"PG_ADMIN_PASSWORD={s.get('db_admin_pass', '')}",
        f"DB_DSN=postgres://{user}:{s.get('db_app_pass', '')}@{host}:{port}/{name}?sslmode=disable",
        f"ADMIN_PASSWORD={s.get('admin_password', '')}",
    ]

    dest = Path(out_dir) / ".env"
    dest.write_text("\n".join(lines) + "\n")
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

    print(f"Rendering templates → {args.out}/")
    config = load_config(local_path=args.local)
    render_templates(config, args.out)
    write_env(config, args.out)


if __name__ == "__main__":
    main()
