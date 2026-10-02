#!/bin/sh
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
destination=${1:-"$HOME/.local/bin/hopr"}
if [ ! -f "$repo_dir/bin/hopr" ]; then
  echo "Build first: make build" >&2
  exit 2
fi
if [ -e "$destination" ] || [ -L "$destination" ]; then
  echo "Refusing to overwrite $destination. Move the old binary aside explicitly." >&2
  exit 2
fi
mkdir -p "$(dirname -- "$destination")"
install -m 755 "$repo_dir/bin/hopr" "$destination"
printf 'Installed %s\nNo configuration or credentials were changed.\n' "$destination"
