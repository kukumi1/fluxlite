# fluxlite

一个专注于**多跳端口转发**和**sing-box 用户节点**的轻量面板。

- Go 单二进制，前端资源内嵌
- SSH/ProxyJump 管理节点，不安装常驻 Agent
- Realm 链路：TCP、TCP+UDP、多跳、抓包验证、配置漂移自愈
- sing-box 用户节点：独立服务、独立系统用户、额度周期、上下行限速、二维码和客户端配置
- 流量审计：实时列出每台机器上每个节点访问了哪些网站
- 节点订阅与 Telegram 管理
- SQLite 持久化，凭据 AES-256-GCM 加密
- 支持 systemd 和 Alpine OpenRC
- 深浅双色管理界面：可折叠导航、彩色指标、节点抽屉编辑与多跳链路视图
- 个人中心可调整立体效果和毛玻璃强度（1–100），外观偏好保存在当前浏览器
- 中文登录页，支持预先设置安全问题＋一次性恢复码找回密码；已开启 TOTP 时仍需验证码

## 功能概览

### Realm 多跳链路

- 任意跳数，自动从节点端口池分配端口
- 创建、下发、验证、停止、删除和后台巡检
- 节点能力探测：架构、系统、UDP、IPv6、Realm 版本
- 入口 IPv6、NAT 端口避让、iptables/ip6tables 流量计数
- 链路额度、按天统计、仪表盘趋势和审计日志

### sing-box 用户节点

- SS2022、AnyTLS、VLESS Reality、Hysteria2、TUIC、Trojan、VMess、VLESS TLS
- SS2022 可选 AES-128-GCM、AES-256-GCM、ChaCha20-Poly1305
- 每个用户独立配置、独立服务，并以独立系统用户 `fluxlite-sb-<id>` 运行；支持 systemd/OpenRC
- 基础额度留空表示无限；有额度时按入站+出站合计限制
- 上行、下行可分别限速（Mbps，0 表示不限），依赖目标机的 `tc`
- 流量周期可自定义，默认 30 天
- 连接过期时间可选择日期时间；留空表示永久
- 二维码、连接链接、JSON 配置、在线/离线开关
- 在线状态按实际客户端会话判断，管理员开关只控制节点是否可用
- 计数规则在机器重启或防火墙重载后丢失时，下一次采样会自动补回
- 扫描并接管已有 `/etc/sing-box/conf.d/*.json` 节点，支持从原凭据导出客户端链接和配置
- 接管时直接保留宿主机 sing-box 片段中的协议、TLS、Reality、传输和凭据配置，不改远程服务密码

### 流量审计

「流量审计」页按时间倒序列出：**哪台机器的哪个节点（端口、协议）访问了什么网站或 IP**。

- 面板每 3 秒读一次每台在线机器上运行的全部 sing-box：面板创建的节点和机器上自建的 sing-box 都包括在内。新加的节点或端口会自动出现，不需要任何设置。
- 数据来自 sing-box 自己的连接日志（info 级别）：面板节点写到 `/var/lib/fluxlite-singbox/<id>/access.log`；自建的 sing-box 按它的配置读日志文件、OpenRC 重定向的输出或 systemd journal。
- 自建 sing-box 的日志级别低于 info 时看不到连接，面板会自动改一次：有 `-C` 目录时放一个 `zz-fluxlite-audit-log.json`，否则只改 `log.level` 并保留备份；改完先 `sing-box check`，再只重启那一个服务。
- 经 Realm 链路的流量记在落地机的节点下。不记录客户端地址，也不追踪链路的来源。
- 记录只保存在内存里，最多 1000 条，重复访问会合并计数；面板重启后从头开始。支持按机器筛选、搜索、翻页，以及暂停刷新以便排查。

### 节点订阅

勾选已有托管节点生成客户端订阅，一个节点最多加入一个订阅；节点额度和到期统一在节点板块管理。删除订阅仅解除关联，不删除节点。提供五种客户端格式。详见 [订阅与稳定性说明](docs/SUBSCRIPTIONS.md)。

### Telegram 管理

支持中文 Bot 查询按钮与 Telegram 内嵌面板：查看整体数据，管理链路、机器与节点；不开放终端和账户安全设置。默认关闭，在普通网页个人中心配置并绑定管理员。详见 [Telegram 接入指南](docs/TELEGRAM.md)。

## 快速安装

从 [Releases](https://github.com/kukumi1/fluxlite/releases) 下载对应架构的二进制：

```bash
install -m 755 fluxlited-linux-arm64 /usr/local/bin/fluxlited
export FLUXLITE_MASTER_KEY="$(/usr/local/bin/fluxlited --genkey)"
mkdir -p /var/lib/fluxlite
/usr/local/bin/fluxlited --listen 127.0.0.1:7800 --data /var/lib/fluxlite
```

生产环境建议使用 systemd，并通过 Caddy/Nginx 反向代理 HTTPS。面板默认只监听 `127.0.0.1:7800`；不开放公网时可以使用 SSH 隧道：

```bash
ssh -L 7800:127.0.0.1:7800 root@PANEL_HOST
```

## 基本使用

1. 首次打开面板创建管理员账号并配置主密钥。
2. 在“机器”页面手动添加或一键注册 VPS。
3. 在“链路”页面创建 Realm 多跳链路，点击下发并验证。
4. 在“节点”页面创建 sing-box 用户节点，选择机器、协议、端口、额度和限速。
5. 外部扫描节点默认只读；点击接管后才由面板创建独立服务。
6. 在“流量审计”页面查看各节点访问了什么。

密码找回默认未启用。登录后到“个人中心 → 密码找回”设置安全问题并离线保存恢复码；恢复成功会退出所有会话并消耗恢复设置，重新登录后需再次设置。详见[账号恢复](docs/OPERATIONS.md#账号)。

删除机器前，面板会先清理该机器上由面板创建的 Realm 和 sing-box 服务、配置及计数规则。外部扫描记录只影响面板数据库，不会删除远程配置。

## sing-box 额度和周期

- 基础额度为空：无限制；无流量时显示 `--`，有流量时显示已用量。
- 基础额度为数值：达到入站+出站合计额度后停止该用户服务。
- 流量周期独立计算，默认 30 天，可在编辑客户端时调整。
- 连接有效期独立计算，留空永久；过期后只停止该连接。
- 用户编辑、加量、减量、清零、启停和删除均写入审计日志。

目标机器需要已经安装可执行的 sing-box。面板不会自动下载未经固定版本校验的内核；配置下发前会执行 `sing-box check`。

## 命令行参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--listen` | `127.0.0.1:7800` | HTTP 监听地址 |
| `--data` | `/var/lib/fluxlite` | 数据库与运行数据目录 |
| `--reconcile-interval` | `5m` | 节点探测和配置漂移巡检 |
| `--sample-interval` | `10s` | 节点指标、链路存活和延迟采样 |
| `--traffic-interval` | `1m` | 流量计数采集 |
| `--insecure-cookies` | `false` | 仅开发环境允许 HTTP Cookie |
| `--genkey` | — | 生成主密钥并退出 |
| `--version` | — | 输出版本并退出 |

主密钥必须通过以下任一环境变量提供：

```text
FLUXLITE_MASTER_KEY       # 32 字节 hex
FLUXLITE_MASTER_KEY_FILE  # 推荐，文件内容为 32 字节 hex
```

## 从源码构建

```bash
cd web
npm ci
npm test
npm run build
cd ..
go test ./...
go vet ./...
go build ./...
```

交叉编译 ARM64：

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w -X main.version=VERSION" \
  -o fluxlited-linux-arm64 ./cmd/fluxlited
```

## 文档

- [架构说明](docs/ARCHITECTURE.md)
- [运维手册](docs/OPERATIONS.md)
- [sing-box 用户节点](docs/SINGBOX.md)
- [订阅与稳定性说明](docs/SUBSCRIPTIONS.md)
- [Telegram 接入指南](docs/TELEGRAM.md)
- [路线图与限制](docs/ROADMAP.md)

## 设计边界

- 不安装节点 Agent，不做常驻回连。
- 不做通用代理面板：订阅只打包面板托管的节点，不接入外部订阅源。
- 不覆盖外部 sing-box 配置，外部节点必须显式接管；流量审计对外部 sing-box 只读日志，唯一的改动是把低于 info 的日志级别调到 info。
- 流量审计不记录客户端地址，也不落库。
- 入口公网可达性仍需从外部节点验证；本机回环验证不能证明 NAT 映射可达。

## License

Apache-2.0
