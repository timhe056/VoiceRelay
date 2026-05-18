# VoiceRelay — 独立通用语音中继服务

> 独立于 Salem 的通用 UDP 语音中继服务。任何 C# 游戏客户端均可接入。
> 零依赖 Salem，独立部署，独立演进。

---

## 1. 文档索引

| 文档 | 内容 |
|------|------|
| **[tech_selection.md](docs/tech_selection.md)** | 技术选型：Go + net.UDPConn + net/http + scratch 容器（~5MB 镜像，~8MB 内存） |
| [voice_server_design.md](docs/voice_server_design.md) | 决策记录：延迟分析 + 框架方案对比（自研 vs LiveKit vs Mumble vs Steam） |

## 2. 项目定位

```
┌──────────────────────────────────────────────────────────┐
│                     VoiceRelay                            │
│                                                           │
│  一个轻量级 UDP 语音包中继服务器。                          │
│  接收 → 按频道分组 → 转发给同频道其他成员。                  │
│                                                           │
│  不关心：                                                  │
│    - 游戏逻辑（谁是谁、什么角色）                           │
│    - 谁是 Host、谁是 Client                                │
│    - 用了什么游戏引擎                                      │
│                                                           │
│  只关心：                                                  │
│    - Token 是否有效                                        │
│    - 哪个房间、哪个频道                                    │
│    - 收到包 → 转给谁                                       │
└──────────────────────────────────────────────────────────┘
```

**与 Salem 的关系**：Salem 的 lobby 服务器通过 REST API 为玩家签发 VoiceToken，Salem 客户端用此 Token 连接 VoiceRelay。VoiceRelay 不调用 Salem 的任何代码。

---

## 2. 项目结构

```
VoiceRelay/
├── go.mod
├── go.sum
├── README.md
│
├── docs/
│   ├── tech_selection.md
│   └── voice_server_design.md
│
├── cmd/
│   └── voicerelay/
│       └── main.go                  # 入口：启动 UDP listener + HTTP server
│
├── internal/
│   ├── header/
│   │   ├── packet.go               # 22 字节包头解析
│   │   └── packet_test.go
│   ├── room/
│   │   ├── manager.go              # 房间/频道/客户端管理
│   │   └── manager_test.go
│   ├── relay/
│   │   ├── forwarder.go            # 核心转发逻辑
│   │   └── forwarder_test.go
│   └── api/
│       ├── handler.go              # HTTP handlers (join/leave/channel/stats/health)
│       └── handler_test.go
│
└── sdk/
    └── go/                         # Go 客户端 SDK（用于其他 Go 项目或命令行工具）
        ├── client.go
        └── client_test.go
```

### 2.1 依赖关系

```
cmd/voicerelay
    │
    ├── internal/relay/   (转发逻辑)
    │       └── internal/header/  (包头解析)
    │       └── internal/room/    (房间管理)
    │
    └── internal/api/     (HTTP signaling)
            └── internal/room/
```

零外部依赖。仅用 Go 标准库 `net`、`net/http`、`sync`、`time`、`crypto/rand`。

---

## 3. 协议设计

### 3.1 总览

```
┌─────────────────────────────────────────────────┐
│                 信令通道 (HTTP)                    │
│  POST /voice/join     → { token, endpoint }      │
│  DELETE /voice/leave  → 200                      │
│  GET /voice/health    → { rooms, clients }        │
└─────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────┐
│               数据通道 (UDP)                      │
│  [Header 22B][Opus Payload N bytes]              │
│  服务器解析 Header → 查房间/频道 → 转发           │
└─────────────────────────────────────────────────┘
```

### 3.2 信令协议 — REST API

**POST /voice/join**

```json
// Request
{
    "roomId": "abc123",           // 游戏房间 ID（由游戏服务器分配）
    "displayName": "Player1",     // 显示名（仅用于日志/调试）
    "channel": "day",             // 初始频道
    "gameToken": "eyJhbGc..."     // 游戏服务器的 JWT（可选，用于验证玩家身份）
}

// Response 200
{
    "clientToken": "a1b2c3d4e5f6...",   // 16 字节 hex，VoiceRelay 签发的临时凭证
    "serverEndpoint": "8.163.126.40",
    "serverPort": 9000,
    "expiresAt": "2026-05-18T14:30:00Z" // Token 过期时间
}

// Response 401 — Token 无效（如果配置了验证回调）
// Response 429 — 房间已满
```

**DELETE /voice/leave**

```
DELETE /voice/leave?token=a1b2c3d4e5f6...
→ 200 OK
```

**GET /voice/health**

```json
// Response 200
{
    "status": "healthy",
    "uptime": "2h 15m",
    "activeRooms": 3,
    "activeClients": 18,
    "packetsPerSecond": 450,
    "bandwidthKbps": 1800
}
```

**POST /voice/channel**（频道切换，由游戏服务器调用）

```json
// Request
{
    "clientToken": "a1b2c3d4e5f6...",
    "newChannel": "dead"
}
// Response 200 — 频道已切换
```

### 3.3 数据协议 — UDP 包头

```
┌──────────────────┬──────────┬──────────┬──────────┬──────────────────┐
│ ClientToken      │ Sequence │ Channel  │ Codec    │ Opus Payload     │
│ (16 bytes)       │ (4 bytes)│ (1 byte) │ (1 byte) │ (N bytes)        │
└──────────────────┴──────────┴──────────┴──────────┴──────────────────┘
  Bytes 0-15        Bytes 16-19  Byte 20    Byte 21    Bytes 22+

Total header: 22 bytes
Opus payload: variable (typically ~60 bytes for 24kbps @ 20ms)
Total packet: ~82 bytes
```

**字段说明：**

| 字段 | 大小 | 说明 |
|------|------|------|
| ClientToken | 16 B | 信令下发的临时凭证，服务器用于查房间/频道 |
| Sequence | 4 B | 递增序号（uint32, big-endian），用于丢包检测 |
| Channel | 1 B | 频道标识（0=Lobby, 1=Day, 2=Witch, 3=Dead, 4-255=自定义） |
| Codec | 1 B | 编码类型（0=Opus, 1=ADPCM, 2=MuLaw） |
| Payload | N B | 编码后的语音数据 |

**设计要点：**
- 包头不含 sender NetId — Token 替代了 NetId，由服务器端维护映射
- Channel 冗余在包头中（服务器也有记录）— 方便服务器快速校验，不匹配则告警
- 总包体 ~82 字节，远低于以太网 MTU (1500)，不会分片

---

## 4. 核心实现

### 4.1 核心转发（`internal/relay/forwarder.go`，~80 行）

```go
func (s *Server) receiveLoop(conn *net.UDPConn) {
    buf := make([]byte, 2048)
    for {
        n, remoteAddr, err := conn.ReadFromUDP(buf)
        if err != nil {
            continue
        }
        // 复制数据，避免 buf 被下一轮覆盖
        packet := make([]byte, n)
        copy(packet, buf[:n])

        go s.handlePacket(packet, remoteAddr)
    }
}

func (s *Server) handlePacket(data []byte, sender *net.UDPAddr) {
    // 1. 解析 22 字节包头
    if len(data) < header.Size {
        return
    }
    hdr := header.Parse(data[:header.Size])
    payload := data[header.Size:]

    // 2. Token → 客户端信息
    s.mu.RLock()
    client, ok := s.clients[hdr.Token]
    s.mu.RUnlock()
    if !ok {
        return // 无效 token
    }

    // 3. IP 防盗用
    if !client.addr.IP.Equal(sender.IP) || client.addr.Port != sender.Port {
        return
    }

    // 4. 更新心跳
    atomic.StoreInt64(&client.lastPacket, time.Now().UnixNano())

    // 5. 转发给同频道其他人
    s.mu.RLock()
    targets := s.rooms[client.roomID].channels[client.channel]
    s.mu.RUnlock()

    for _, t := range targets {
        if t.token != hdr.Token {
            s.conn.WriteToUDP(data, t.addr)
        }
    }
}
```

### 4.2 房间管理（`internal/room/manager.go`，~120 行）

```go
type RoomManager struct {
    mu    sync.RWMutex
    rooms map[string]*Room
}

func (m *RoomManager) RegisterClient(roomID string, channel byte,
    addr *net.UDPAddr, token [16]byte) *Client {

    m.mu.Lock()
    defer m.mu.Unlock()

    room, ok := m.rooms[roomID]
    if !ok {
        room = NewRoom(roomID)
        m.rooms[roomID] = room
    }

    client := &Client{
        token:   token,
        addr:    addr,
        channel: channel,
        roomID:  roomID,
    }
    room.channels[channel] = append(room.channels[channel], client)
    return client
}

func (m *RoomManager) PurgeExpired(timeout time.Duration) int {
    m.mu.Lock()
    defer m.mu.Unlock()

    now := time.Now().UnixNano()
    count := 0
    for id, room := range m.rooms {
        for ch, clients := range room.channels {
            alive := clients[:0]
            for _, c := range clients {
                if now-atomic.LoadInt64(&c.lastPacket) < int64(timeout) {
                    alive = append(alive, c)
                } else {
                    count++
                }
            }
            room.channels[ch] = alive
        }
        if room.isEmpty() {
            delete(m.rooms, id)
        }
    }
    return count
}
```

### 4.3 HTTP API（`internal/api/handler.go`，~80 行）

```go
func NewHandler(mgr *room.RoomManager) http.Handler {
    mux := http.NewServeMux()

    mux.HandleFunc("POST /api/voice/join", func(w http.ResponseWriter, r *http.Request) {
        var req JoinRequest
        json.NewDecoder(r.Body).Decode(&req)

        token := generateToken()
        addr := resolveUDPAddr(r.RemoteAddr, req.UDPPort)
        client := mgr.RegisterClient(req.RoomID, req.Channel, addr, token)

        json.NewEncoder(w).Encode(JoinResponse{
            ClientToken:    hex.EncodeToString(token[:]),
            ServerEndpoint: publicIP,
            ServerPort:     udpPort,
            ExpiresAt:      time.Now().Add(ClientTimeout),
        })
    })

    mux.HandleFunc("DELETE /api/voice/leave", func(w http.ResponseWriter, r *http.Request) {
        token, _ := hex.DecodeString(r.URL.Query().Get("token"))
        mgr.UnregisterClient((*[16]byte)(token))
        w.WriteHeader(http.StatusOK)
    })

    return mux
}
```

### 4.4 入口（`cmd/voicerelay/main.go`，~40 行）

```go
func main() {
    mgr := room.NewRoomManager()

    // 定时清理过期客户端
    go func() {
        for range time.Tick(30 * time.Second) {
            mgr.PurgeExpired(2 * time.Minute)
        }
    }()

    server := relay.NewServer(mgr)
    go server.ListenUDP(":9000")

    http.ListenAndServe(":8080", api.NewHandler(mgr))
}
```

---

## 5. 实施计划

### Phase 1 — 骨架搭建（2-3 天）

| # | 任务 | 产出 |
|---|------|------|
| 1.1 | `go mod init` + 目录骨架 | 编译通过的空项目 |
| 1.2 | `internal/header/packet.go` + 单元测试（序列化/反序列化往返） | 包头正确 |
| 1.3 | `internal/room/` 模型 + manager + 单元测试（注册/注销/查询/过期清理） | 房间管理正确 |

### Phase 2 — 核心转发（2 天）

| # | 任务 | 产出 |
|---|------|------|
| 2.1 | `internal/relay/forwarder.go` + 单元测试（正常转发/IP 不匹配/Token 无效） | 转发逻辑正确 |
| 2.2 | `internal/api/handler.go` HTTP handlers (join/leave/channel/stats/health) | HTTP 接口可用 |
| 2.3 | `cmd/voicerelay/main.go` 入口集成 | 服务器能启动 |
| 2.4 | 手动测试：2 个命令行客户端互发 UDP → 服务器转发 | 端到端通 |

### Phase 3 — 压测与优化（2 天）

| # | 任务 | 产出 |
|---|------|------|
| 3.1 | 模拟 12 客户端同时说话转发 | 性能基线 |
| 3.2 | `sync.Pool` 优化热路径分配 + pprof 分析 | 内存/CPU 基线 |
| 3.3 | 过期客户端定时清理 + 空闲房间回收 | 内存不泄漏 |

### Phase 4 — 文档与集成示例（1 天）

| # | 任务 | 产出 |
|---|------|------|
| 4.1 | README 完成 | 项目说明 |
| 4.2 | 集成示例：命令行模拟客户端 | 可演示 |
| 4.3 | 集成指南：Salem 如何接入（不修改 Salem 代码） | 文档 |

**总工期：7-8 天**（Go 项目结构更简单，省了 C# 的 sln/csproj/SDK 搭建和 AOT 调试时间）

---

## 6. 认证策略

VoiceRelay 不实现自己的用户系统。提供两种认证模式：

### 模式 1：自签发 Token（默认，适合开发/小规模）

```
Game Server                          VoiceRelay
    │                                     │
    │── POST /voice/join                  │
    │   { roomId, channel }               │
    │                                     │── 自签发 16B 随机 Token
    │←─ { token, endpoint, port }         │
    │                                     │
    │── 把 token 发给客户端（通过已有信道）│
```

VoiceRelay 自己生成随机 Token，不需要知道玩家是谁。适合房间管理由游戏服务器自行保证的场景。

### 模式 2：验证回调（生产环境）

```
Game Server                          VoiceRelay
    │                                     │
    │── POST /voice/join                  │
    │   { roomId, channel, gameJwt }      │
    │                                     │── POST {callbackUrl}/verify { gameJwt, roomId }
    │                                     │     → 200 { valid: true, channel: "day" }
    │                                     │     → 403 { valid: false }
    │                                     │── 签发 VoiceRelay Token
    │←─ { token, endpoint, port }         │
```

VoiceRelay 配置一个验证回调 URL，收到 join 请求时调游戏服务器验证 JWT 的有效性。游戏服务器可以拒绝或覆盖频道分配。

```json
// config.json（可选，VoiceRelay 也支持环境变量）
{
  "udp_port": 9000,
  "http_port": 8080,
  "auth": {
    "mode": "selfsigned",
    "callback_url": "https://salem-server/api/voice/verify",
    "callback_timeout_ms": 3000
  },
  "room": {
    "max_clients_per_room": 12,
    "client_timeout_minutes": 2,
    "cleanup_interval_seconds": 30
  }
}
```

---

## 7. 部署

### 7.1 独立部署（当前推荐）

```
docker run -d \
  -p 9000:9000/udp \
  -p 8080:8080/tcp \
  -v ./config.json:/config.json \
  voicerelay:latest
```

- UDP 9000：语音数据
- TCP 8080：REST API（join/leave/stats/health）
- 一个 128MB 内存的云服务器足够支撑 50+ 并发房间

### 7.2 二进制部署（无 Docker）

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o voicerelay ./cmd/voicerelay
scp voicerelay user@8.163.126.40:/opt/voicerelay/
ssh user@8.163.126.40 '/opt/voicerelay/voicerelay -udp :9000 -http :8080'
```

单二进制 ~6MB，scp 上传即部署，无运行时依赖。

### 7.3 与游戏服务器同机部署

和 Salem ASP.NET Core 服务器在同一台机器上，但不同进程、不同端口：

```
8.163.126.40:5000  → Salem Lobby (HTTP/WS)
8.163.126.40:9000  → VoiceRelay (UDP)
8.163.126.40:8080  → VoiceRelay Admin (HTTP)
```

进程隔离，互不影响。VoiceRelay 挂掉不影响游戏逻辑，游戏服务器挂掉不影响正在进行的语音通话。

---

## 8. Salem 如何接入（不修改 Salem 代码）

VoiceRelay 是独立项目，Salem 通过以下方式接入（完全在 Salem 侧完成，不影响 VoiceRelay）：

```
Salem Lobby Server                    VoiceRelay
    │                                     │
    │── 玩家进入房间                      │
    │── 调用 POST /voice/join             │
    │   { roomId, channel }               │
    │←─ { token, endpoint, port }         │
    │── 通过现有 WebSocket/消息            │
    │   把 { token, endpoint, port }      │
    │   下发给客户端                       │
    │                                     │
    │── 玩家频道变更（死亡/夜晚等）        │
    │── 调用 POST /voice/channel          │
    │   { token, newChannel }             │
    │                                     │
    │── 玩家离开房间                      │
    │── 调用 DELETE /voice/leave          │
    
Salem Client (Godot)
    │── 收到 { token, endpoint, port }
    │── new VoiceRelayClient(token, endpoint, port)
    │── 语音采集 → client.SendVoiceFrame(opusPayload, channel)
    │── client.OnVoiceFrameReceived → 语音播放
```

**Salem 侧需要的改动（不在本次 VoiceRelay 项目范围内）：**
1. Lobby 服务器：新增 3 个 HTTP 调用（join/channel/leave）
2. 客户端：新增 `VoiceRelayClient` 替代 `NetworkVoiceTransport` 的语音路径
3. 配置：`voiceServerEndpoint` + `voiceServerPort`

这些改动将在 Salem 的 Host/Client 重划方案中一并处理。
