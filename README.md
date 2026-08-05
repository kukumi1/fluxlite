# fluxlite

多跳端口转发的管理面板。定义一条链路，它把每一跳的 realm 配置生成好、下发到位，并用抓包证明流量真的到了落地。

单个静态二进制，内嵌前端。节点上只跑 realm，不装 agent。

```
客户端 ──► 入口节点 ──► 中继节点 ──► 中继节点 ──► 落地地址
           ▲            ▲            ▲
           └────────────┴────────────┴──── fluxlite 经 SSH 下发与巡检
```

<!-- 目录 -->

- [为什么不直接用现成面板](#为什么不直接用现成面板)
- [特性](#特性)
- [安装](#安装)
- [部署形态](#部署形态)
- [上手](#上手)
- [命令行参数](#命令行参数)
- [从源码构建](#从源码构建)
- [文档](#文档)

## 为什么不直接用现成面板

这个项目只做一件事：多跳转发。它从实际运维里长出来，针对几个反复踩到、而且**看不出来**的坑做了设计。

**「能连上」不等于「转发通」。** realm 先在本地 accept 客户端连接，之后才异步拨后端。所以即使后端已经挂了，TCP 连接照样成功，而且快得离谱——一条物理 RTT 50ms 的链路，建连只要 4ms。fluxlite 的验证不采信建连结果：它在最后一跳抓包，确认特征串真的出现在发往落地的 payload 里。

**NAT 机器不一定过 UDP。** 很多 NAT 小鸡只映射 TCP。配一条 tcp+udp 链路上去，面板一片绿，UDP 流量全进黑洞。fluxlite 在纳管时实测 UDP 穿透，而且**带本机自发自收的对照组**——监听没起来和网络不通是两回事，分不清就会得出错误结论。测不出来时记为「未知」，绝不武断标记为不通。

**端口可能被 iptables 悄悄占着。** DNAT 规则在 PREROUTING 就改写了目标地址，数据包根本到不了本地套接字，而 `ss` 看不见任何东西。绑在这种端口上的 relay 会正常启动、正常显示、一个字节收不到。fluxlite 分配端口时同时读套接字和 nat 表。

**有些节点根本连不上。** 内网机器往往只对某台特定前置可见。节点可以声明跳板，下发时自动经 ProxyJump 链穿透，不需要为此单独搭 VPN。

设计上贯穿一条原则：**「不知道」永远不折叠成「好」或「坏」**。测不出来就显示未知，采样过期就标记过期——一个冻住的绿灯比红灯更危险。

## 特性

**转发**

- 任意跳数的转发链，逐跳自动分配端口，尊重每台机的端口池
- 分配时跳过节点上已被占用的端口：监听中的套接字、以及被 DNAT/REDIRECT 规则劫走的端口
- 每条链路一个独立 realm 实例 —— realm 没有热重载，共用进程意味着改一条链路会断掉整机所有链路
- TCP / TCP+UDP，建链前校验每一跳的 UDP 能力
- 落地地址支持域名，realm 按连接解析，DDNS 换址自动跟上

**下发与自愈**

- 幂等下发：配置 hash 比对，没变就不重启，不制造无谓断流
- 从末跳往前建，中继不会指向一个还不存在的监听端口
- 重启后复查服务是否真的活着 —— `Restart=always` 会让崩溃循环中的服务也报「启动成功」
- 定时巡检自动纠正**配置漂移**：有人手改了节点上的配置，会被改回并重启
- 支持 systemd 与 OpenRC（Alpine 上强制 `supervise-daemon`，避免 OOM 后不自愈）

**可观测**

- 每跳延迟与存活状态后台采样，链路卡片实时刷新
- 采样过期会明确标记，不会拿旧数据冒充现状
- 抓包验证端到端投递，这是唯一可信的结论
- 完整审计日志

**安全**

- 凭据 AES-256-GCM 加密存储，主密钥在数据库之外
- 可选两步验证、登录失败锁定、改密码自动踢掉其他会话
- SSH host key 固定（TOFU），防止中间人劫持到节点的 root 会话
- 一键注册全程私钥不出面板

## 安装

从 [Releases](https://github.com/kukumi1/fluxlite/releases) 下载对应架构的二进制：

```bash
curl -fsSL -o /usr/local/bin/fluxlited \
  https://github.com/kukumi1/fluxlite/releases/latest/download/fluxlited-linux-amd64
chmod +x /usr/local/bin/fluxlited
```

生成主密钥并**离线备份**——弄丢它，所有已存的节点凭据都解不开，只能全部重新纳管：

```bash
fluxlited --genkey > /etc/fluxlite/master.key
chmod 600 /etc/fluxlite/master.key
```

systemd 单元：

```ini
[Unit]
Description=fluxlite control plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=FLUXLITE_MASTER_KEY_FILE=/etc/fluxlite/master.key
ExecStart=/usr/local/bin/fluxlited --listen 127.0.0.1:7800 --data /var/lib/fluxlite
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=full
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

## 部署形态

面板持有所有节点的 root 凭据，是整套系统里最高价值的目标。**默认只监听 `127.0.0.1`**，公网访问请交给反向代理处理 TLS：

```caddyfile
fl.example.com {
    reverse_proxy 127.0.0.1:7800
}
```

不想开公网就用 SSH 隧道：

```bash
ssh -L 7800:127.0.0.1:7800 root@your-server
# 然后浏览器打开 http://localhost:7800
```

仅在本地无 TLS 调试时才需要 `--insecure-cookies`。

> 反向代理只提供 TLS，不提供认证。放到公网上时，密码就是唯一防线——除非开启两步验证。

## 上手

1. 首次打开面板创建管理员账号，按需绑定两步验证（可跳过，之后在个人中心随时开启）
2. **节点**页点「一键注册」，填名称、连接地址和端口池，生成命令
3. 把命令粘到目标机器上以 root 执行
4. **链路**页新建链路：按顺序选节点，填落地地址 `host:port`，选协议
5. **下发**，然后**验证**

### 一键注册

```bash
curl -fsSL https://your-panel/enroll.sh | sh -s -- https://your-panel <token>
```

脚本会识别系统架构与 init、把面板公钥写进 `authorized_keys`、从**面板**下载安装 realm，然后回报并触发面板立即回连验证，结果直接打在终端上。

几个要点：

- **私钥始终留在面板**，只有公钥下发到节点，注册过程不把可用凭据放到网络上
- realm 由面板下发，**节点不需要访问 GitHub** —— 国内机器最常卡住的一步
- 令牌一次性、60 分钟过期
- 严格 POSIX sh，Alpine 的 busybox ash 上同样可用
- 会检查 sshd 是否禁用了公钥认证或 root 登录，提前告警而不是等回连失败

**NAT 机器的连接地址必须手填**，脚本无法自动探测：NAT 主机的出口地址和入口地址通常不是一个。填服务商给你的映射地址即可，其余信息脚本自己搞定。

仍然可以走「手动添加」用密码或私钥纳管，两种方式并存。

### 读懂验证结果

只有出现**「抓包已证实数据真实抵达落地」**才算真的通。其余项目全绿但这项没过，说明链路能建连但不转发。

> 抓包验证需要最后一跳装有 `tcpdump`。没有时该项报「未知」而不是「通过」。

## 命令行参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--listen` | `127.0.0.1:7800` | 监听地址 |
| `--data` | `/var/lib/fluxlite` | 数据目录（数据库与 realm 缓存） |
| `--reconcile-interval` | `5m` | 巡检间隔：探测节点、纠正配置漂移 |
| `--sample-interval` | `30s` | 采样间隔：每跳存活与延迟 |
| `--insecure-cookies` | `false` | 允许 HTTP 下发送 session cookie，仅开发用 |
| `--genkey` | | 生成主密钥后退出 |
| `--version` | | 打印版本后退出 |

主密钥二选一，必须设置：

| 环境变量 | 说明 |
|---|---|
| `FLUXLITE_MASTER_KEY` | 密钥本身（hex） |
| `FLUXLITE_MASTER_KEY_FILE` | 密钥文件路径，推荐 |

## 从源码构建

```bash
cd web && npm ci && npm run build && cd ..
go build -o fluxlited ./cmd/fluxlited
```

前端产物由 `go:embed` 打进二进制，所以**必须先构建前端**。

交叉编译（无 CGO，产物是静态二进制）：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags)" -o fluxlited-linux-amd64 ./cmd/fluxlited
```

跑测试：

```bash
go test ./...
```

## 文档

- [架构](docs/ARCHITECTURE.md) —— 模块划分、数据流、以及几个关键设计决策的由来
- [运维手册](docs/OPERATIONS.md) —— 升级、端口池、故障排查、备份恢复
- [路线图](docs/ROADMAP.md) —— 接下来打算做什么，以及明确不做什么

## 致谢

多跳路由的数据模型思路来自 [flux-panel](https://github.com/0xNetuser/flux-panel)。转发内核是 [realm](https://github.com/zhboner/realm)。

## License

Apache-2.0
