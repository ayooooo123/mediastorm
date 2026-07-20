#!/bin/sh
set -eu

runtime_user="mediastorm"
runtime_group="mediastorm"
cache_dir="/root/cache"
ownership_marker="$cache_dir/.mediastorm-owner"

if [ "$(id -u)" -eq 0 ]; then
	mkdir -p "$cache_dir"

	runtime_uid="$(id -u "$runtime_user")"
	runtime_gid="$(id -g "$runtime_user")"
	expected_owner="$runtime_uid:$runtime_gid"
	current_owner=""
	if [ -f "$ownership_marker" ] && [ ! -L "$ownership_marker" ]; then
		current_owner="$(head -n 1 "$ownership_marker")"
	fi

	if [ "$current_owner" != "$expected_owner" ]; then
		echo "Migrating $cache_dir ownership to $runtime_user ($expected_owner)..."
		chown -R --no-dereference "$expected_owner" "$cache_dir"
		marker_tmp="$(mktemp /tmp/mediastorm-owner.XXXXXX)"
		printf '%s\n' "$expected_owner" > "$marker_tmp"
		chown "$expected_owner" "$marker_tmp"
		# -T replaces a hostile symlink instead of following it.
		mv -fT "$marker_tmp" "$ownership_marker"
	fi

	exec gosu "$runtime_user:$runtime_group" "$@"
fi

# Respect an explicit Docker --user override. Its caller is responsible for
# granting that user access to the mounted cache directory.
exec "$@"
