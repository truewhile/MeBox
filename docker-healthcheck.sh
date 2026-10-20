#!/bin/sh
# MeBox 容器健康检查。
#
# 背景：MeBox 只监听一个端口，且开启 HTTPS 后该端口只接受 TLS（明文 HTTP 不再
# 服务）。健康检查若固定用 http:// 探测，每轮都会被 TLS 监听拒绝并在服务日志里
# 留下一条 "client sent an HTTP request to an HTTPS server"，同时容器状态永远
# 是 unhealthy。反过来固定用 https:// 探测，则在未开启 HTTPS 的部署上产生
# "first record does not look like a TLS handshake"。
#
# 当前协议由服务进程写入 $MEBOX_APP_DATA_DIR/.http_scheme（见
# cmd/server/server_manager.go 的 writeSchemeState）。脚本优先按该文件选择协议，
# 文件缺失或该协议探测失败时再试另一种，避免状态文件过期导致误报不健康。
set -u

port="${MEBOX_APP_PORT:-8080}"
data_dir="${MEBOX_APP_DATA_DIR:-/data}"

scheme_file="$data_dir/.http_scheme"
scheme=""
if [ -r "$scheme_file" ]; then
	scheme="$(tr -d '[:space:]' <"$scheme_file")"
fi
if [ "$scheme" = "https" ]; then
	first="https"
	second="http"
else
	first="http"
	second="https"
fi

for proto in "$first" "$second"; do
	# --no-check-certificate：用户常用自签证书或 IP 访问，校验域名必然会失败。
	if busybox wget -q --spider -T 3 --no-check-certificate \
		"$proto://127.0.0.1:$port/api/health" >/dev/null 2>&1; then
		exit 0
	fi
done

exit 1
