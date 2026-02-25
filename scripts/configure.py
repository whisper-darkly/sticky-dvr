#!/usr/bin/env python3
"""Render sticky-dvr config templates.

Usage:
    python3 scripts/configure.py --out dist/docker [--local config.local.yaml]

If --local is omitted, config.local.yaml is merged automatically if it exists.

Secrets
-------
Templates call secret(key, type=..., bytes=...) to resolve a secret value.
Resolution order:

  1. config.yaml / config.local.yaml  — user-pinned value (tracked or local)
  2. .secrets.yaml                    — previously generated value (untracked)
  3. generate + persist to .secrets.yaml for stable re-use across runs

Generated secrets accumulate in .secrets.yaml; they are never overwritten once
set unless the user clears them.
"""

import argparse
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


# ---------------------------------------------------------------------------
# Secret resolver — injected into Jinja2 as secret()
# ---------------------------------------------------------------------------

class SecretStore:
    """Lazy secret resolver available in templates as ``secret(key, ...)``.

    Priority:
      1. User value from merged config (config.yaml / config.local.yaml)
      2. Previously generated value persisted in .secrets.yaml
      3. Freshly generated — saved to .secrets.yaml on flush()
    """

    def __init__(self, user_secrets: dict, secrets_file: Path):
        self._user = user_secrets          # from config merge
        self._file = secrets_file
        self._persisted = self._load()
        self._dirty = False

    def _load(self) -> dict:
        if self._file.exists():
            with open(self._file) as f:
                data = yaml.safe_load(f) or {}
            return data.get("secrets", {})
        return {}

    def __call__(self, key: str, type: str = "urlsafe", bytes: int = 18) -> str:
        # 1. User-pinned value
        val = self._user.get(key, "")
        if val:
            return val
        # 2. Previously generated
        val = self._persisted.get(key, "")
        if val:
            return val
        # 3. Generate and stage for persistence
        val = self._generate(type, bytes)
        self._persisted[key] = val
        self._dirty = True
        return val

    def _generate(self, type: str, nbytes: int) -> str:
        if type == "hex":
            return _secrets.token_hex(nbytes)
        if type == "urlsafe":
            return _secrets.token_urlsafe(nbytes)
        raise ValueError(f"unknown secret type: {type!r}")

    def flush(self):
        """Persist any newly generated secrets back to .secrets.yaml."""
        if not self._dirty:
            return
        with open(self._file, "w") as f:
            yaml.dump({"secrets": self._persisted}, f,
                      default_flow_style=False, allow_unicode=True)
        print(f"  generated secrets → {self._file.relative_to(REPO_ROOT)}")


# ---------------------------------------------------------------------------
# Config loading
# ---------------------------------------------------------------------------

def deep_merge(base, override):
    """Recursively merge override into base. Returns base."""
    for key, val in override.items():
        if key in base and isinstance(base[key], dict) and isinstance(val, dict):
            deep_merge(base[key], val)
        else:
            base[key] = val
    return base


def load_config(local_path=None):
    config = {}

    # Layer 1: tracked defaults
    with open(BASE_CONFIG) as f:
        deep_merge(config, yaml.safe_load(f) or {})

    # Layer 2: local override
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


# ---------------------------------------------------------------------------
# Template rendering
# ---------------------------------------------------------------------------

def render_templates(config, out_dir, secret_store):
    out_path = Path(out_dir)
    env = Environment(
        loader=FileSystemLoader(str(TEMPLATES_DIR)),
        keep_trailing_newline=True,
    )
    env.globals["secret"] = secret_store

    for template_path in sorted(TEMPLATES_DIR.rglob("*.j2")):
        rel = template_path.relative_to(TEMPLATES_DIR)
        out_rel = Path(str(rel)[:-3])  # strip .j2
        dest = out_path / out_rel
        dest.parent.mkdir(parents=True, exist_ok=True)
        template = env.get_template(str(rel))
        rendered = template.render(config=config)
        dest.write_text(rendered)
        if dest.suffix == ".sh":
            dest.chmod(dest.stat().st_mode | 0o111)
        print(f"  {dest}")

    secret_store.flush()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

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
    store = SecretStore(
        user_secrets=config.get("secrets", {}),
        secrets_file=SECRETS_FILE,
    )
    render_templates(config, args.out, store)


if __name__ == "__main__":
    main()
