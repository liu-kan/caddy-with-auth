#!/bin/sh
# 从 Cloudflare 官网生成 trusted_proxies 用的静态地址段（Caddyfile 片段，一行 "static <CIDR>..."）。
# 用法：./gen-cloudflare-ranges.sh [输出文件，默认 ./cloudflare-ranges.caddy]
# 拉取或校验失败时不覆盖原文件。
set -eu

out="${1:-./cloudflare-ranges.caddy}"
list="$(mktemp)"
raw="$(mktemp)"
trap 'rm -f "$list" "$raw" "$list.v4" "$list.v6" "$out.tmp"' EXIT

# curl 必须独立执行：POSIX sh 没有 pipefail，放进管道会掩盖一类列表的下载失败。
for family in v4 v6; do
	curl -fsS --max-time 20 "https://www.cloudflare.com/ips-$family" >"$raw"
	# 官网响应可能没有末尾换行；先补齐，避免 IPv4 最后一段和 IPv6 第一段粘连。
	printf '\n' >>"$raw"
	tr -d '\r' <"$raw" | sed '/^$/d' >"$list.$family"
	case "$family" in
		v4) pattern='^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$' ;;
		v6) pattern='^[0-9A-Fa-f]*:[0-9A-Fa-f:]+/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$' ;;
	esac
	if [ ! -s "$list.$family" ] || grep -Evq "$pattern" "$list.$family"; then
		echo "error: empty or unexpected Cloudflare $family list, $out not changed" >&2
		exit 1
	fi
done

cat "$list.v4" "$list.v6" >"$list"
count="$(wc -l <"$list" | tr -d ' ')"
if [ "$count" -lt 15 ]; then
	echo "error: only $count ranges fetched, $out not changed" >&2
	exit 1
fi

printf 'static %s\n' "$(tr '\n' ' ' <"$list" | sed 's/ $//')" >"$out.tmp"
mv "$out.tmp" "$out"
echo "wrote $count Cloudflare ranges (IPv4 + IPv6) to $out"
