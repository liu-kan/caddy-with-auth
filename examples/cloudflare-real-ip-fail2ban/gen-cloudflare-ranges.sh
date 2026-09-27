#!/bin/sh
# 从 Cloudflare 官网生成 trusted_proxies 用的静态地址段（Caddyfile 片段，一行 "static <CIDR>..."）。
# 用法：./gen-cloudflare-ranges.sh [输出文件，默认 ./cloudflare-ranges.caddy]
# 拉取或校验失败时不覆盖原文件。
set -eu

out="${1:-./cloudflare-ranges.caddy}"
list="$(mktemp)"
trap 'rm -f "$list" "$out.tmp"' EXIT

for family in v4 v6; do
	curl -fsS --max-time 20 "https://www.cloudflare.com/ips-$family"
	echo
done | tr -d '\r' | sed '/^$/d' >"$list"

if grep -Evq '^[0-9A-Fa-f:.]+/[0-9]{1,3}$' "$list"; then
	echo "error: unexpected line in Cloudflare IP list, $out not changed" >&2
	exit 1
fi
count="$(wc -l <"$list" | tr -d ' ')"
if [ "$count" -lt 15 ]; then
	echo "error: only $count ranges fetched, $out not changed" >&2
	exit 1
fi

printf 'static %s\n' "$(tr '\n' ' ' <"$list" | sed 's/ $//')" >"$out.tmp"
mv "$out.tmp" "$out"
echo "wrote $count Cloudflare ranges to $out"
