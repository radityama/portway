#!/bin/sh
# Review and run this local script. Never pipe a remote installer into a shell.
set -eu
fail() { printf '%s\n' "Installation failed: $1" >&2; exit 1; }
version='' directory="${HOME:?}/.local/bin" assets='' pinned=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--dir|--assets-dir|--checksums-sha256)
      [ "$#" -ge 2 ] || fail 'missing option value'
      case "$1" in --version) version=$2;; --dir) directory=$2;; --assets-dir) assets=$2;; --checksums-sha256) pinned=$2;; esac
      shift 2;;
    *) fail 'use --version vMAJOR.MINOR.PATCH [--dir directory] [--assets-dir directory] [--checksums-sha256 digest]';;
  esac
done
[ "${#version}" -le 64 ] || fail 'invalid version'
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$' || fail 'pin an explicit release version'
case "$(uname -s)" in Linux) os=linux;; Darwin) os=darwin;; *) fail 'use the PowerShell installer on Windows';; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64;; aarch64|arm64) arch=arm64;; *) fail 'unsupported architecture';; esac
asset="portway_${version}_${os}_${arch}"
[ -n "$directory" ] && [ ! -L "$directory" ] || fail 'unsafe destination'
if [ ! -d "$directory" ]; then (umask 077; mkdir -p "$directory") || fail 'cannot create destination'; fi
directory=$(cd -P "$directory" && pwd)
if [ "$os" = linux ]; then metadata=$(stat -c '%u %a' "$directory"); else metadata=$(stat -f '%u %Lp' "$directory"); fi
owner=${metadata% *} mode=${metadata#* }
[ "$owner" = "$(id -u)" ] || fail 'destination must belong to your account'
case "$mode" in ''|*[!0-7]*) fail 'cannot validate destination permissions';; esac
[ "$((0$mode & 022))" -eq 0 ] || fail 'destination is writable by another account'
[ ! -L "$directory/portway" ] || fail 'existing executable is a symlink'
[ ! -e "$directory/portway" ] || [ -f "$directory/portway" ] || fail 'existing executable is not a regular file'
stage=$(mktemp -d "$directory/.portway-install.XXXXXX")
trap 'rm -rf "$stage"' 0
trap 'exit 1' HUP INT TERM
fetch() {
  name=$1 limit=$2
  if [ -n "$assets" ]; then
    [ -f "$assets/$name" ] && [ ! -L "$assets/$name" ] || fail 'offline asset is missing or unsafe'
    [ "$(wc -c < "$assets/$name")" -le "$limit" ] || fail 'asset exceeds size bound'
    cp "$assets/$name" "$stage/$name"
  else
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 120 --max-filesize "$limit" --output "$stage/$name" "https://github.com/radityama/portway/releases/download/$version/$name" || fail 'HTTPS asset download failed'
  fi
  [ "$(wc -c < "$stage/$name")" -le "$limit" ] || fail 'asset exceeds size bound'
}
digest() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1; elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d ' ' -f 1; else fail 'install sha256sum or shasum'; fi; }
fetch SHA256SUMS 16384
if [ -n "$pinned" ]; then
  [ "${#pinned}" -eq 64 ] && printf '%s\n' "$pinned" | LC_ALL=C grep -Eq '^[0-9a-f]{64}$' || fail 'invalid pinned checksum digest'
  [ "$(digest "$stage/SHA256SUMS")" = "$pinned" ] || fail 'checksum file does not match trusted digest'
fi
expected=$(LC_ALL=C awk -v name="$asset" '$2 == name {print $1}' "$stage/SHA256SUMS")
[ "${#expected}" -eq 64 ] && printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[0-9a-f]{64}$' || fail 'missing, duplicate or invalid artifact checksum'
fetch "$asset" 104857600
[ "$(digest "$stage/$asset")" = "$expected" ] || fail 'artifact checksum mismatch'
chmod 0755 "$stage/$asset"
mv -f "$stage/$asset" "$directory/portway"
printf 'Installed Portway %s at %s/portway\n' "$version" "$directory"
