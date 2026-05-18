#!/bin/bash
set -e

# ============================================================
# VoiceRelay 云服务器一键部署脚本
# 支持 Ubuntu 18.04+ / Debian 10+ / CentOS 7+
# 用法: chmod +x deploy.sh && sudo ./deploy.sh
# ============================================================

REPO_URL="https://github.com/timhe056/VoiceRelay.git"
INSTALL_DIR="/opt/voicerelay"
PUBLIC_IP=$(curl -s ifconfig.me 2>/dev/null || curl -s ip.sb 2>/dev/null || echo "127.0.0.1")
UDP_PORT=9000
HTTP_PORT=8080

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

log()  { echo -e "${GREEN}[VoiceRelay]${NC} $1"; }
warn() { echo -e "${RED}[VoiceRelay]${NC} $1"; }

# ── 1. 检测系统 ──────────────────────────────────────────
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS=$ID
else
    warn "无法检测系统版本，假定为 Ubuntu"
    OS="ubuntu"
fi
log "检测到系统: $OS"

# ── 2. 安装 Go（如果未安装或版本太低） ──────────────────
install_go() {
    GO_VERSION="1.24.0"
    GO_ARCH="linux-amd64"

    if command -v go &>/dev/null; then
        CURRENT=$(go version | grep -oP 'go\K[0-9]+\.[0-9]+' | head -1)
        if [ "$(printf '%s\n' "1.22" "$CURRENT" | sort -V | head -1)" = "1.22" ]; then
            log "Go $CURRENT 已安装，版本满足要求"
            return
        fi
    fi

    log "安装 Go ${GO_VERSION}..."
    curl -sL "https://go.dev/dl/go${GO_VERSION}.${GO_ARCH}.tar.gz" -o /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz

    cat >> /etc/profile.d/go.sh <<'PROFILE'
export PATH=$PATH:/usr/local/go/bin
export GOPATH=$HOME/go
PROFILE
    export PATH=$PATH:/usr/local/go/bin
    log "Go ${GO_VERSION} 安装完成"
}
install_go

# ── 3. 安装 git ─────────────────────────────────────────
if ! command -v git &>/dev/null; then
    log "安装 git..."
    case $OS in
        ubuntu|debian) apt-get update -qq && apt-get install -y -qq git ;;
        centos|rhel)   yum install -y -q git ;;
        *)             apt-get update -qq && apt-get install -y -qq git ;;
    esac
fi

# ── 4. 克隆/更新代码 ────────────────────────────────────
if [ -d "$INSTALL_DIR/.git" ]; then
    log "更新代码..."
    cd "$INSTALL_DIR"
    git pull origin master
else
    log "克隆仓库..."
    rm -rf "$INSTALL_DIR"
    git clone "$REPO_URL" "$INSTALL_DIR"
    cd "$INSTALL_DIR"
fi

# ── 5. 编译 ──────────────────────────────────────────────
log "编译 VoiceRelay..."
cd "$INSTALL_DIR"
/usr/local/go/bin/go build -ldflags="-s -w" -o voicerelay ./cmd/voicerelay
log "编译完成: $(ls -lh voicerelay | awk '{print $5}')"

# ── 6. 防火墙放通 ──────────────────────────────────────
log "配置防火墙..."
case $OS in
    ubuntu|debian)
        if command -v ufw &>/dev/null && ufw status | grep -q "Status: active"; then
            ufw allow ${UDP_PORT}/udp 2>/dev/null || true
            ufw allow ${HTTP_PORT}/tcp 2>/dev/null || true
            log "UFW 已放通 ${UDP_PORT}/udp ${HTTP_PORT}/tcp"
        fi
        ;;
    centos|rhel)
        if command -v firewall-cmd &>/dev/null && systemctl is-active --quiet firewalld 2>/dev/null; then
            firewall-cmd --permanent --add-port=${UDP_PORT}/udp 2>/dev/null || true
            firewall-cmd --permanent --add-port=${HTTP_PORT}/tcp 2>/dev/null || true
            firewall-cmd --reload 2>/dev/null || true
            log "firewalld 已放通 ${UDP_PORT}/udp ${HTTP_PORT}/tcp"
        fi
        ;;
esac

# 云服务器安全组提示
log "============================================"
log "如果你的云服务器有安全组（阿里云/腾讯云等），"
log "请确保在控制台放通以下端口："
log "  UDP ${UDP_PORT}  (语音数据)"
log "  TCP ${HTTP_PORT}  (信令 API)"
log "============================================"

# ── 7. 创建 systemd 服务 ─────────────────────────────────
log "创建 systemd 服务..."
cat > /etc/systemd/system/voicerelay.service <<SERVICE
[Unit]
Description=VoiceRelay UDP Voice Relay Server
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/voicerelay -udp :${UDP_PORT} -http :${HTTP_PORT} -public-ip ${PUBLIC_IP}
Restart=always
RestartSec=3
WorkingDirectory=${INSTALL_DIR}

[Install]
WantedBy=multi-user.target
SERVICE

# ── 8. 启动服务 ─────────────────────────────────────────
systemctl daemon-reload
systemctl enable voicerelay
systemctl restart voicerelay

sleep 1
if systemctl is-active --quiet voicerelay; then
    log "============================================"
    log "VoiceRelay 部署成功！"
    log "  Public IP : ${PUBLIC_IP}"
    log "  UDP 语音  : ${PUBLIC_IP}:${UDP_PORT}"
    log "  HTTP API  : ${PUBLIC_IP}:${HTTP_PORT}"
    log "  状态检查  : curl http://${PUBLIC_IP}:${HTTP_PORT}/healthz"
    log "  查看日志  : journalctl -u voicerelay -f"
    log "============================================"
else
    warn "服务启动失败，查看日志: journalctl -u voicerelay -n 50"
    exit 1
fi
