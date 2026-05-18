# VoiceRelay 开发日志

> 独立 UDP 语音中继服务。Go 实现，零外部依赖，~5MB 镜像。

## 开发计划

| 阶段 | 内容 | 状态 |
|------|------|------|
| Phase 1 | 项目骨架 + 包头解析 + 房间管理 | ✅ done |
| Phase 2 | UDP 转发引擎 + HTTP API + 入口集成 | ✅ done |
| Phase 3 | 压测 + sync.Pool 优化 + pprof | 🔲 todo |
| Phase 4 | 命令行模拟客户端 + 集成指南 | 🔲 todo |
| Phase 5 | 部署文档 + Dockerfile | 🔲 todo |

## 日志

### 2026-05-18

**Phase 1** — 项目骨架搭建
- `go mod init` + 目录结构（cmd/internal/docs）
- `internal/header/packet.go`：22 字节包头，Parse / Write / BuildPacket / TokenFromHex
- `internal/room/manager.go`：Manager（Register/Unregister/GetClient/ChannelMembers/UpdateChannel/PurgeExpired/Stats），sync.RWMutex 并发安全，clientIdx 提供 O(1) token 查找
- `internal/room/manager.go`：SetLastPacket 方法用于测试时间注入
- 对应单元测试：头往返（3×3×3 全组合）、房间 CRUD、并发安全（10 goroutine）
- 设计文档清理：voice_server_design.md 砍掉 C# 实现部分仅留决策记录，voice_service_standalone_analysis.md 删除（决策已定）

**Phase 2** — 核心转发 + API
- `internal/relay/forwarder.go`：Server 结构体，ListenUDP 收包循环，handlePacket 热路径（header parse → token lookup → IP 防盗用 → channel 校验 → Touch → 转发同频道 peers）
- `internal/api/handler.go`：5 端点（POST /join, DELETE /leave, POST /channel, GET /stats, GET /healthz），自签发 16B 随机 token，JSON 请求/响应
- `cmd/voicerelay/main.go`：flag 参数 → room.Manager → 定时清理 goroutine → UDP relay goroutine → HTTP server goroutine → os.Signal 优雅退出
- 对应单元测试：转发正确性/反欺骗/无效 token/并发转发（6 client），API 全端点覆盖（加入/满员/离开/频道切换/统计/健康检查）
- 全部 20 测试通过，二进制编译成功
