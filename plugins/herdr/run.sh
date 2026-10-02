#!/bin/sh
set -eu
hopr_binary=${HOPR_BIN:-"$HOME/.local/bin/hopr"}
if [ ! -x "$hopr_binary" ]; then
    printf 'Hopr is not installed at %s. Install the Hopr binary first.\n' "$hopr_binary" >&2
    exit 3
fi
exec "$hopr_binary" "$@"
