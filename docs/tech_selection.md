# VoiceRelay 技术选型

> 2026-05-18，独立语音中继服务的技术栈选型分析。
> 前提：服务器只做 UDP 包头解析 + 频道路由转发，不解码、不混音、不转码。
> 约束：低内存云服务器，AI 是主要开发力。

---

## 1. 选型前提

### 1.1 服务器只做什么

```
收到 UDP 包 → 解析 22 字节自定义包头 → 查房间/频道 → 转发给同频道其他成员
```

- 不解析 Opus payload
- 不解码/混音/转码
- 不处理 WebRTC（无 ICE/DTLS/SDP）
- 语音采集和播放完全在客户端完成

### 1.2 负载特征

| 指标 | 值 |
|------|-----|
| 每房间最大人数 | 12 |
| 每玩家发包频率 | 50 包/秒（20ms 帧，VoiceConfig.FrameMs=20，仅说话时发送） |
| 单房间典型负载 | 1-2 人同时说话 → 50-100 pps |
| 单房间极端负载 | 12 人全说 → 600 pps |
| 50 房间典型总负载 | ~5,000 pps |
| 每包链路层大小 | ~110 字节（IP 20 + UDP 8 + 自定义头 22 + Opus ~60） |
| 每说话者服务器带宽 | 入站 ~44 kbps，转发 11 人 → 出站 ~484 kbps，合计 ~528 kbps |
| 50 房间典型带宽 | ~40 Mbps 出口 |

**结论：任何现代语言在任何现代 runtime 上都严重过剩。选型约束不是性能上限，是内存下限和运维轻量。**

---

## 2. 语言选型

### 2.1 场景特点

| | 影响 |
|---|------|
| AI 是主要开发力 | "团队语言经验"权重 → 零。AI 写 Go / C# / Rust 质量相同 |
| 低内存云服务器 | 内存占用 → 高权重。每 MB 都算钱 |
| 单二进制部署 | 运行时依赖 → 越少越好 |
| 核心代码量小（~300 行） | 语言细节差异被稀释，生态和工具链更重要 |

### 2.2 对比

| 维度 | Go | C# (.NET 9 AOT) | Rust |
|------|-----|-----------------|------|
| **UDP 吞吐（单核）** | ~100k pps | ~107k pps | ~120k pps |
| **需求吞吐** | ~5k pps（50 房典型） | 同 | 同 |
| **余量** | ~20x | ~21x | ~24x |
| **空闲内存** | **~8 MB** | ~20 MB | ~5 MB |
| **Docker 镜像（压缩）** | **~5 MB（scratch）** | ~45 MB | ~5 MB（scratch） |
| **运行时依赖** | 零（静态链接） | 几乎零（AOT，但有 ICU 尾） | 零 |
| **GC** | sub-ms 暂停 | 热路径可避免 | 无 GC |
| **标准库 UDP** | `net.UDPConn`，一等公民 | `Socket`，需要 SAEA 池 | tokio `UdpSocket` |
| **标准库 HTTP** | `net/http`，自带 mux | 需要 ASP.NET Core 框架 | 需要第三方 framework |
| **AI 代码质量** | **极好**（Go 是 AI 训练最多的后端语言之一） | 好 | 一般（AI 偶尔和 borrow checker 打架） |
| **module 管理** | `go mod`，轻量 | NuGet + csproj | Cargo |
| **交叉编译** | `GOOS=linux go build`，一行 | `dotnet publish -r`，需 SDK | `cross` 或手动配置 target |
| **场景匹配度** | **UDP 转发是 Go 的原生赛道** | 适合更复杂的业务系统 | 适合极致性能场景 |

### 2.3 选择：Go

决定因素，按重要性：

1. **内存**：空闲 ~8MB vs C# ~20MB。在 512MB 云服务器上，Go 留更多内存给 OS page cache。虽然差距绝对值小，但 Go 的 GC 对低内存场景更友好（GOGC 默认 100 的情况下行为可预测）。
2. **镜像体积**：~5MB scratch vs ~45MB Chiseled。拉镜像快、磁盘占用小、攻击面小（scratch 里连 shell 都没有）。
3. **AI 产出质量**：Go 的网络服务代码模式极其固定（`ListenUDP` → `ReadFromUDP` → 处理 → `WriteToUDP`），AI 训练数据里海量高质量 sample，产出的代码更接近社区 idiom。
4. **部署**：`GOOS=linux GOARCH=amd64 go build` 产出单二进制，COPY 进 scratch 就完事。不需要 SDK 镜像、不需要 AOT 配置、不需要 globalization 处理。

Rust 的内存最低但 AI 在 borrow checker 上的出错率高——对于 AI 开发为主的场景，Go 是最优平衡点。

---

## 3. UDP 层：标准库 `net`

**不引入任何第三方 UDP 库。** 核心转发 ~50 行 Go：

```go
func (s *Server) receiveLoop(conn *net.UDPConn) {
    buf := make([]byte, 2048)
    for {
        n, remoteAddr, err := conn.ReadFromUDP(buf)
        if err != nil {
            continue
        }
        // 复制一份数据，避免 buf 被下一轮 ReadFromUDP 覆盖
        packet := make([]byte, n)
        copy(packet, buf[:n])

        go s.handlePacket(packet, remoteAddr)
    }
}

func (s *Server) handlePacket(data []byte, sender *net.UDPAddr) {
    // 1. 解析 22 字节包头
    header := ParseHeader(data[:22])
    payload := data[22:]

    // 2. Token → 房间/频道
    s.mu.RLock()
    client, ok := s.clients[header.Token]
    s.mu.RUnlock()
    if !ok {
        return // 无效 token，静默丢弃
    }

    // 3. 更新心跳
    atomic.StoreInt64(&client.lastPacket, time.Now().UnixNano())

    // 4. 转发给同频道其他人
    s.mu.RLock()
    targets := s.rooms[client.roomID].channels[client.channel]
    s.mu.RUnlock()

    for _, target := range targets {
        if target.token != header.Token {
            conn.WriteToUDP(data, target.addr)
        }
    }
}
```

**goroutine 开销分析**：每包启动一个 goroutine，50 房间典型 5k pps = 每秒 5k goroutine。每个 goroutine 初始栈 ~2KB，5k × 2KB = 10MB 栈峰值，秒内回收。Go runtime 对此完全无感——`go handlePacket()` 是 Go 网络服务的标准写法。

**无需引入的库**：不需要 LiteNetLib（可靠传输）、不需要 SuperSocket（会话管理）、不需要任何框架。Go 标准库的 `net.UDPConn` 就是这个场景的正确答案。

---

## 4. HTTP 层：标准库 `net/http`

5 个端点，Go 1.22+ 自带 `http.NewServeMux` 支持 method + path 路由：

```go
mux := http.NewServeMux()
mux.HandleFunc("POST /api/voice/join", s.handleJoin)
mux.HandleFunc("DELETE /api/voice/leave", s.handleLeave)
mux.HandleFunc("POST /api/voice/channel", s.handleChannel)
mux.HandleFunc("GET /api/voice/stats", s.handleStats)
mux.HandleFunc("GET /healthz", s.handleHealth)
```

不需要 gin、echo、chi 等框架——5 个端点用标准库最轻。标准库 `net/http` 性能对 signaling API（每秒几十个请求）完全够用，且零依赖。

Go 1.22+ 的 `HandleFunc("METHOD /path", handler)` 路由和 Minimal API 的 `app.MapPost()` 是一一对应的，代码量相当，性能相当。

---

## 5. Opus 库：不需要

服务器只解析自己的 22 字节包头，Opus payload 透明转发。

如果将来需要在服务器端读取 Opus 元数据（静音检测、带宽统计），RFC 6716 Section 3.1 的 TOC 字节只需 5 行 Go：

```go
toc := payload[0]
config := (toc >> 3) & 0x1F
stereo := (toc>>2)&1 == 1
frames := [...]int{1, 2, 2, -1}[toc&0x03]
if frames == -1 {
    frames = int(payload[1] & 0x3F)
}
```

**不需要任何 Opus 库。**

---

## 6. 容器与部署

### 6.1 Dockerfile

```dockerfile
# 编译阶段
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /app/voicerelay ./cmd/voicerelay

# 运行阶段
FROM scratch
COPY --from=build /app/voicerelay /voicerelay
EXPOSE 9000/udp 8080/tcp
ENTRYPOINT ["/voicerelay"]
```

| 镜像方案 | 解压后 | 压缩后 |
|---------|--------|--------|
| **Go + scratch（选用）** | **~8 MB** | **~5 MB** |
| Go + alpine | ~15 MB | ~7 MB |
| Go + distroless | ~12 MB | ~6 MB |

`-ldflags="-s -w"` 去掉符号表和调试信息（~30% 体积缩减）。`CGO_ENABLED=0` 纯 Go 静态链接。`scratch` 是空镜像——没有 shell、没有包管理器、没有 CA 证书（如果需要 HTTPS 回调验证，改用 `distroless` 或 `alpine`）。

### 6.2 资源配置建议

| 环境 | CPU | 内存 | 可支撑 |
|------|-----|------|--------|
| 最低 | 0.5 核 | **64 MB** | ~10 房间 |
| 推荐 | 1 核 | **128 MB** | ~50 房间 |
| 充裕 | 2 核 | 256 MB | ~200 房间 |

相比 C# 方案，每个档位内存需求降低 ~50%。128MB 实例跑 50 房间绰绰有余。

---

## 7. 项目结构（Go 版）

```
VoiceRelay/
├── go.mod
├── go.sum
├── README.md
├── docs/
│   ├── tech_selection.md
│   ├── voice_server_design.md
│   └── voice_service_standalone_analysis.md
├── cmd/
│   └── voicerelay/
│       └── main.go                  // 入口：启动 UDP listener + HTTP server
├── internal/
│   ├── header/
│   │   ├── packet.go               // 22 字节包头解析
│   │   └── packet_test.go
│   ├── room/
│   │   ├── manager.go              // 房间/频道/客户端管理
│   │   └── manager_test.go
│   ├── relay/
│   │   ├── forwarder.go            // 核心转发逻辑
│   │   └── forwarder_test.go
│   └── api/
│       ├── handler.go              // HTTP handlers (join/leave/channel/stats/health)
│       └── handler_test.go
└── sdk/
    └── go/                         // Go 客户端 SDK（未来其他 Go 项目用）
```

相比 C# 的 3 个 csproj + sln，Go 只有 1 个 module，所有包在 `internal/` 下。

---

## 8. 技术栈总结

| 层 | 选择 | 核心理由 |
|---|------|---------|
| **语言** | Go | 内存 ~8MB，镜像 ~5MB，AI 产出质量高，UDP 转发是原生赛道 |
| **UDP** | `net.UDPConn`（标准库） | 零依赖，~50 行核心循环 |
| **HTTP** | `net/http`（标准库，Go 1.22+） | 5 端点无需框架 |
| **容器** | `scratch` | ~5MB 压缩镜像，零攻击面 |
| **Opus** | 无 | 服务器只转发不解码 |
| **最低部署** | 64MB / 0.5 核 | 够跑 10 房间 |

### 与关键竞品对比

| | VoiceRelay (Go) | Discord | Steam Voice |
|---|----------------|---------|-------------|
| 编码 | Opus | Opus | Opus |
| 默认码率 | 24 kbps | **64 kbps** | ~16-32 kbps |
| 24→32kbps 建议 | 提升到 32kbps 接近透明 | - | - |
| 协议 | 纯 UDP + 22B 头 | WebRTC (DTLS+SRTP+RTP) | Opus over Steam transport |
| 每包协议开销 | **50 字节** | ~70 字节 | 未公开 |
| 每说话者链路占用 | **44 kbps** | ~134 kbps | 相似 |
| 延迟 | 10-20ms（直连） | 80-150ms（需 ICE 握手） | 15-150ms（P2P vs 中继回落） |
| 服务器成本 | **~128MB 实例** | 数百万并发级 infra | 零（Steam 承担） |
| 平台锁定 | 无 | 无 | **Steam 独占** |
| 质量监控 | 白盒可控 | 黑盒 | 黑盒 |
| 音质（MOS） | 24kbps ~4.2 | 64kbps ~4.5 | ≈16-32kbps ~4.0-4.3 |

**建议将客户端 Opus 码率从 24kbps 提升到 32kbps**——24→32 的带宽增量极小（每说话者从 44→52 kbps），但 MOS 从 4.2→4.3，sibilant 音（s/sh/f）表现有可感知改善。这是免费午餐。
