# fluxlite

多跳端口转发的管理面板。定义一条链路，它把每一跳的 realm 配置生成好、下发到位、并用抓包证明流量真的到了落地。

单个静态二进制，内嵌前端。节点上只跑 realm，不装 agent。

```
客户端 ──► 入口节点 ──► 中继节点 ──► 中继节点 ──► 落地地址
           ▲            ▲            ▲
           └────────────┴────────────┴──── fluxlite 经 SSH 下发与巡检
```

## 为什么不直接用现成面板

这个项目只做一件事：多跳转发。它从实际运维里长出来，针对三个反复踩到的坑做了设计：

**「能连上」不等于「转发通」。** realm 先在本地 accept 客户端连接，之后才异步拨后端。所以即使后端已经挂了，TCP 连接照样成功，而且快得离谱——一条物理 RTT 50ms 的链路，建连只要 4ms。fluxlite 的验证不采信建连结果：它在最后一跳抓包，确认特征串真的出现在发往落地的 payload 里、并且落地回了 ACK。

**NAT 机器不一定过 UDP。** 很多 NAT 小鸡只映射 TCP。配一条 tcp+udp 链路上去，面板一片绿，UDP 流量全进黑洞。fluxlite 在纳管时实测 UDP 穿透，而且**带本机自发自收的对照组**——监听没起来和网络不通是两回事，分不清就会得出错误结论。探测不出来时记为「未知」，绝不武断标记为不通。

**有些节点根本连不上。** 内网机器往往只对某台特定前置可见。节点可以声明跳板，下发时自动经 ProxyJump 链穿透，不需要为此单独搭 VPN。

## 特性

- 任意跳数的转发链，逐跳自动分配端口（尊重每台机的端口池，NAT 机只有几个可用端口也能管）
- 每条链路独立 realm 实例 —— realm 没有热重载，共用进程意味着改一条链路会断掉整机所有链路
- 幂等下发：配置 hash 比对，没变就不重启，不制造无谓断流
- 支持 systemd 与 OpenRC（Alpine 上强制 `supervise-daemon`，避免 OOM 后不自愈）
- 定时巡检，发现 relay 掉了自动重新下发
- 凭据 AES-256-GCM 加密存储，主密钥在数据库之外
- 强制两步验证、登录失败锁定、完整审计日志
- SSH host key 固定（TOFU），防止中间人劫持到节点的 root 会话

## 安装

从 [Releases](https://github.com/kukumi1/fluxlite/releases) 下载对应架构的二进制：

```bash
curl -fsSL -o /usr/local/bin/fluxlited \
  https://github.com/kukumi1/fluxlite/releases/latest/download/fluxlited-linux-amd64
chmod +x /usr/local/bin/fluxlited
```

生成主密钥并保存好 —— **弄丢它，所有已存的节点凭据都解不开了**：

```bash
fluxlited --genkey
```

systemd 单元：

```ini
[Unit]
Description=fluxlite control plane
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=FLUXLITE_MASTER_KEY=<上一步生成的密钥>
ExecStart=/usr/local/bin/fluxlited --listen 127.0.0.1:7800 --data /var/lib/fluxlite
Restart=always
RestartSec=3

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

## 使用

1. 首次打开面板创建管理员账号，并绑定两步验证
2. **节点** 页添加机器：地址、登录方式、端口池。内网节点选一个跳板
3. 对每个节点执行**探测**，拿到系统、架构、init 类型和 UDP 能力
4. **链路** 页新建链路：按顺序选节点，填落地地址，选协议
5. **下发**，然后**验证**

验证结果只有出现「抓包已证实数据真实抵达落地」才算真的通。其余项目全绿但这项没过，说明链路能建连但不转发。

> 抓包验证需要最后一跳装有 `tcpdump`。没有时该项报「未知」而不是「通过」。

## 配置项

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--listen` | `127.0.0.1:7800` | 监听地址 |
| `--data` | `/var/lib/fluxlite` | 数据目录（数据库与 realm 缓存） |
| `--reconcile-interval` | `5m` | 巡检间隔 |
| `--insecure-cookies` | `false` | 允许 HTTP 下发送 session cookie，仅开发用 |
| `--genkey` | | 生成主密钥后退出 |

环境变量 `FLUXLITE_MASTER_KEY`（hex）或 `FLUXLITE_MASTER_KEY_FILE`（密钥文件路径）二选一，必须设置。

## 从源码构建

```bash
cd web && npm ci && npm run build && cd ..
go build -o fluxlited ./cmd/fluxlited
```

前端产物由 `go:embed` 打进二进制，所以要先构建前端。

## 架构

| 模块 | 职责 |
|---|---|
| `prober` | 探测节点的 OS / init / 架构 / realm 版本 / UDP 穿透能力 |
| `planner` | 链路定义 → 各跳的 realm 配置与端口分配 |
| `applier` | SSH 下发，hash 比对幂等，从末跳往前建 |
| `verifier` | 逐跳连通性 + 末跳抓包证明投递 |
| `watcher` | 定时探活与配置漂移 reconcile |
| `sshx` | SSH 连接层，ProxyJump 链与 host key 固定 |

下发顺序是**从最后一跳往前**：中继不会指向一个还不存在的监听端口，入口只在整条链就绪后才对外提供服务。

## 致谢

多跳路由的数据模型思路来自 [flux-panel](https://github.com/0xNetuser/flux-panel)。转发内核是 [realm](https://github.com/zhboner/realm)。

## License

Apache-2.0
