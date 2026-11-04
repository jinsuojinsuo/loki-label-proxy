#!/usr/bin/env bash
# 由 loki-label-proxy.service 启动；统一启停交给 systemctl。
# 修改本启动脚本后，需要重启服务才生效。
# 重启全部代理：sudo systemctl restart loki-label-proxy
# 二进制、脚本和 config 目录放在安装目录中。
set -euo pipefail
umask 077
cd "${INSTALL_DIR:-/usr/local/loki-label-proxy}"
mkdir -p logs

# 每个 Loki 一条启动命令：监听端口、上游地址、权限配置和日志分别设置。
./loki-label-proxy --listen=:8081 \
    --addr=http://loki-a:3100 \
    --config=./config/loki-a.yaml \
    --log=debug >>logs/loki-a.log 2>&1 &

./loki-label-proxy --listen=:8082 \
    --addr=https://loki-b.example.com/loki-prefix/ \
    --config=./config/loki-b.yaml \
    --log=info >>logs/loki-b.log 2>&1 &

# 上游 Basic Auth：命令前加 LOKI_USERNAME="账号" LOKI_PASSWORD="明文密码"。
# 或在命令中加 --loki-user="账号" --loki-pass="明文密码"。
# 代理入口 Basic Auth：在对应命令中加 --auth-user="账号" --auth-pass-sha256="密码SHA256值"。
# Grafana 使用入口账号和对应的明文密码。
# 新增 Loki 时，复制一条命令并修改参数，保留末尾的 &。

# 保持主进程运行；停止服务或主进程退出时，systemd 清理全部代理子进程。
wait
