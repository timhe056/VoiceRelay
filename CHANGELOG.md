# VoiceRelay 开发日志

> 独立 UDP 语音中继服务。Go 实现，零外部依赖。

## 开发计划

| 阶段 | 内容 | 状态 |
|------|------|------|
| Phase 1 | 项目骨架 + 包头解析 + 房间管理 | ✅ done |
| Phase 2 | UDP 转发引擎 + HTTP API + 入口集成 | ✅ done |
| Phase 3 | Godot 测试客户端 + 云端部署 + NAT 穿透适配 | ✅ done |
| Phase 4 | 客户端改为语音聊天室（名字/房间号/成员列表/说话者指示） | 🔲 todo |
| Phase 5 | 压测 + pprof 性能优化 | 🔲 todo |

## 日志

### 2026-05-18

**Phase 1** — 项目骨架搭建
- `go mod init` + 目录结构（cmd/internal/docs）
- `internal/header/packet.go`：22 字节包头，Parse / Write / BuildPacket / TokenFromHex
- `internal/room/manager.go`：Manager（Register/Unregister/GetClient/ChannelMembers/UpdateChannel/PurgeExpired/Stats），sync.RWMutex 并发安全，clientIdx 提供 O(1) token 查找
- 对应单元测试：头往返（3×3×3 全组合）、房间 CRUD、并发安全（10 goroutine）

**Phase 2** — 核心转发 + API
- `internal/relay/forwarder.go`：Server 结构体，ListenUDP 收包循环，handlePacket 热路径
- `internal/api/handler.go`：5 端点（POST /join, DELETE /leave, POST /channel, GET /stats, GET /healthz）
- `cmd/voicerelay/main.go`：flag 参数 → room.Manager → 定时清理 → UDP relay → HTTP server → 优雅退出
- 全部 20 测试通过，二进制编译成功

### 2026-05-19

**Phase 3** — 测试客户端 + 云端部署

Godot 客户端（`VoiceRelayClientTest/voice-relay-client-godot/`）：
- 从 Salem 移植音频管线：VoiceCapture → 采集、VoicePlayback → 播放、OpusCodec → Opus 编解码
- Godot 内置 `AudioStreamMicrophone` 在本机不工作，切换到 **NAudio WaveInEvent** 采集麦克风
- `System.Text.Json` 与 Go 服务端 camelCase JSON 不兼容，加 `[JsonPropertyName]` 解决
- `UdpVoiceTransport`：Bind → 拿本地端口 → HTTP Join 上报 → ConnectToServer 设 token
- DebugUI：服务器地址/房间号/频道/Join/Leave/PTT 状态/收发包计数/麦克风音量
- 客户端默认连接云服务器 `8.163.126.40:8080`

服务端部署：
- 独立 git 仓库：`https://github.com/timhe056/VoiceRelay`
- `deploy.sh` 一键部署脚本：检测系统 → 装 Go → 克隆 → 编译 → 防火墙 → systemd 服务
- 部署到阿里云 `8.163.126.40`，systemd 保活

NAT 穿透适配：
- 本地测试通过（loopback），双客户端连云服务器 Recv=0
- 排查发现：HTTP 和 UDP 经运营商级 NAT 后走**不同公网 IP**（169.x vs 14.x）
- 修复：去掉 IP+端口校验，改为每包收到后自动更新转发地址
- 认证完全靠 22 字节包头里的 16 字节随机 token（128-bit，暴力破解不可行）
- 双人云服务器语音互通验证通过

Go 测试客户端：
- `cmd/testclient/main.go`：命令行工具，加入房间后统计收包，用于快速验证转发链路
