#!/bin/sh
set -eu
integration_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH= cd -- "$integration_dir/../.." && pwd)
base=b99002ac99b09e00b4ca692436cb15a6b0d676f1
for tool in git cargo just; do
    command -v "$tool" >/dev/null || { echo "Required build tool missing: $tool" >&2; exit 1; }
done
cargo nextest --version >/dev/null
[ "$("${ZIG:-zig}" version)" = 0.15.2 ] || { echo 'Herdr requires Zig 0.15.2' >&2; exit 1; }
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/hopr-herdr-build.XXXXXXXX")
echo "Building pinned custom Herdr in $work_dir"
# Zig 0.15.2's build runner ignores SDKROOT. Select an older installed SDK only
# for this build; do not change the system SDK or xcode-select configuration.
if [ -n "${HOPR_MACOS_SDK:-}" ]; then
    test -f "$HOPR_MACOS_SDK/usr/lib/libSystem.tbd"
    mkdir "$work_dir/tools"
    cat > "$work_dir/tools/xcrun" <<'XCRUN'
#!/bin/sh
if [ "$#" = 3 ] && [ "$1" = --sdk ] && [ "$2" = macosx ] && [ "$3" = --show-sdk-path ]; then
    printf '%s\n' "$HOPR_MACOS_SDK"
else
    exec /usr/bin/xcrun "$@"
fi
XCRUN
    chmod +x "$work_dir/tools/xcrun"
    PATH="$work_dir/tools:$PATH"
    export PATH HOPR_MACOS_SDK
fi
git clone --quiet --no-checkout https://github.com/herdrdev/herdr.git "$work_dir/source"
cd "$work_dir/source"
git checkout --quiet --detach "$base"
git apply --check "$integration_dir/context-menu.patch"
git apply "$integration_dir/context-menu.patch"
cargo fmt --check
just test-one hopr
just build
mkdir -p "$project_dir/bin"
cp target/release/herdr "$project_dir/bin/herdr-hopr"
chmod 755 "$project_dir/bin/herdr-hopr"
printf '\nBuilt %s/bin/herdr-hopr. Existing Herdr/configuration unchanged.\n' "$project_dir"
printf 'Source and build logs remain in %s\n' "$work_dir"
