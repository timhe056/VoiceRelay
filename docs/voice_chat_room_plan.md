# 语音聊天室 — 实现方案

> 2026-05-19，基于已验证通路的 VoiceRelay 服务端 + Godot 客户端，演进为简易语音聊天室。

---

## 1. 当前状态

```
✅ VoiceRelay 服务端（Go）
   - UDP 转发 :9000
   - HTTP API :8080（join/leave/channel/stats/health）
   - 本地验证通过：Godot → server → Go testclient 全链路通

✅ Godot 测试客户端（C#）
   - NAudio 采集 + Concentus Opus 编码
   - VoiceRelay UDP 协议发送
   - 接收 + 解码 + 播放
   - 硬编码 roomId="test-room"，PTT=T

✅ Go 命令行测试客户端
   - 加入房间，统计收包
```

## 2. 目标

```
┌─────────────────────────────────────────────────┐
│              云服务器 (8.163.126.40)               │
│                                                   │
│  VoiceRelay :9000/udp  :8080/tcp (Docker)        │
│                                                   │
└────────┬──────────────────────────┬──────────────┘
         │                          │
    ┌────▼────┐               ┌─────▼────┐
    │ 玩家 A  │               │ 玩家 B   │
    │ (Godot) │               │ (Godot)  │
    │         │               │          │
    │ 输入:   │               │ 输入:    │
    │  名字   │               │  名字    │
    │  房间号 │               │  房间号  │
    │  PTT=T  │               │  PTT=T   │
    └─────────┘               └──────────┘
```

**玩家体验**：打开客户端 → 输入名字和房间号 → 点加入 → 同房间内按住 T 说话互听。

---

## 3. 分两步走

### Step 1 — 服务端可部署（1 天）

| 任务 | 说明 |
|------|------|
| Dockerfile | 基于 golang:1.24-alpine 编译 + scratch 运行，~5MB 镜像 |
| 环境变量配置 | UdpPort / HttpPort / PublicIP / MaxPerRoom / TimeoutMin — 替代硬编码 |
| 部署文档 | Docker run 命令 + 二进制部署两种方式 |
| 云服务器试部署 | 在 8.163.126.40 上起 Docker 容器，验证外网可达 |

### Step 2 — Godot 客户端改聊天室（1-2 天）

| 任务 | 说明 |
|------|------|
| 名字/房间号输入 | UI 上加 Name 输入框 + Room ID 输入框，替代硬编码 |
| 房间成员显示 | 服务端新增 `GET /api/voice/room/{roomId}/members`，客户端轮询显示成员列表 |
| 说话者指示 | 解析收到的包的 token，显示 "XXX 正在说话" |
| 服务器地址可配 | 输入框支持输入云服务器 IP:Port |

---

## 4. 服务端改动

### 4.1 Dockerfile

```dockerfile
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /voicerelay ./cmd/voicerelay

FROM scratch
COPY --from=build /voicerelay /voicerelay
EXPOSE 9000/udp 8080/tcp
ENTRYPOINT ["/voicerelay"]
```

### 4.2 新增 API：房间成员

```
GET /api/voice/room/{roomId}/members

Response 200:
{
  "roomId": "test-room",
  "members": [
    {"token": "abc...", "channel": 0, "online": true},
    {"token": "def...", "channel": 0, "online": true}
  ]
}
```

`online` = 最近 30 秒内有心跳包。这需要 Manager 暴露 `ListRoomMembers(roomID)` 方法（已可基于现有数据结构实现）。

### 4.3 配置方式

用环境变量替代 flag 默认值：`UDP_PORT`, `HTTP_PORT`, `PUBLIC_IP`, `MAX_PER_ROOM`, `TIMEOUT_MIN`。

```bash
docker run -d \
  -p 9000:9000/udp -p 8080:8080/tcp \
  -e PUBLIC_IP=8.163.126.40 \
  voicerelay:latest
```

---

## 5. Godot 客户端改动

### 5.1 UI 布局

```
┌──────────────────────────────────┐
│  VoiceRelay Chat Room            │
│                                  │
│  Server: [8.163.126.40:8080   ]  │
│  Name:   [Player1             ]  │
│  Room:   [lobby               ]  │
│                                  │
│  [Join]  [Leave]    Status: OK   │
│                                  │
│  Members:                        │
│  ● Player1 (me)                  │
│  ● Player2 🔈 talking            │
│  ○ Player3                       │
│                                  │
│  Hold [T] to talk   Sent: 1234   │
└──────────────────────────────────┘
```

### 5.2 现有代码改动

| 文件 | 改动 |
|------|------|
| `DebugUI.cs` → `ChatRoomUI.cs` | 重命名 + 加 Name/Room/Server 输入框 + 成员列表 |
| `VoiceRelayClient.cs` | Join 接受 name 参数；添加成员列表轮询；暴露成员信息事件 |
| `SignalingClient.cs` | 新增 `GetMembersAsync(roomId)` |

### 5.3 说话者识别

收到的 UDP 包里前 16 字节是发送者的 token。服务端返回的成员列表包含 `{token, name}` 映射，客户端查表即可显示 "XXX 正在说话"。

---

## 6. 不做的

- 房间创建/删除管理（目前创建即存在，无人自动清理）
- 用户认证/密码（公开聊天室，知道房间号就能进）
- 频道切换 UI（保持单一频道，手动调）
- 音量混音器（保持默认增益）

---

## 7. 实施顺序

```
Step 1-1: 服务端 Dockerfile + 环境变量          (1h)
Step 1-2: 服务端 members API                     (1h)
Step 1-3: 云服务器部署 + 验证外网可达            (1h)

Step 2-1: 客户端 ChatRoomUI（名字/房间/服务器）  (2h)
Step 2-2: 客户端成员列表 + 说话者指示            (2h)
Step 2-3: 双客户端联调（本地 + 远程各一个）      (1h)
```
