# sing-box 用户节点

fluxlite 的 sing-box 用户节点和 Realm 链路是两个独立功能。Realm 负责多跳端口转发；sing-box 用户节点负责在一台已纳管 VPS 上创建独立的客户端入口、周期额度和客户端配置。

## 隔离方式

每个面板用户使用一个独立 systemd 服务：

```text
/etc/fluxlite/singbox/<id>/config.json
/etc/fluxlite/singbox/<id>/server.crt
/etc/fluxlite/singbox/<id>/server.key
/var/lib/fluxlite-singbox/<id>/
fluxlite-singbox-<id>.service
```

服务之间不共享配置。停止、删除或达到额度只影响一个用户。面板不会覆盖 `/etc/sing-box/config.json`，也不会修改一键脚本创建的 `/etc/sing-box/conf.d/vps-node-*.json`。

当前面板部署要求目标节点已经安装可执行的 `sing-box` 并使用 systemd。配置写入后先执行 `sing-box check`，校验通过才启用服务。

## 协议

创建时一次选择一种协议：SS2022、AnyTLS、VLESS Reality、Hysteria2、TUIC、Trojan、VMess 或 VLESS TLS。TLS 类协议生成独立自签证书，导出的客户端链接带允许自签的参数。

凭据和完整配置使用面板主密钥加密保存；列表接口和审计日志不返回密码、UUID、私钥或链接，只有显式点击“导出”时才解密。

## 流量额度

- 入站与出站字节相加计入额度；基础额度留空表示无限，不执行额度暂停。
- 连接有效期独立设置，默认永久；选择具体日期后到期只停用连接。
- 流量周期从创建时间起计算 30 天，与连接有效期相互独立。
- 周期到期后已用量和本周期追加额度清零，基础额度恢复。
- “增加额度”只增加当前周期额度，不改变周期结束时间或已用量。
- “清零重置”只清零当前周期已用量，不改变周期结束时间。
- 达到额度后面板停止该用户的独立服务；增加额度、清零或进入下一周期后可以恢复。

统计依赖目标节点上的 `iptables`/`ip6tables` 计数规则。流量轮询存在一个采集周期的过冲，计数口径包含协议和网络头部，不等于服务商账单的计费口径。

## 外部节点扫描

“扫描已有节点”只读取 `/etc/sing-box/conf.d/*.json`，返回协议、端口、标签和共享服务状态，不写文件、不重启服务，也不自动套用额度。扫描结果会以 `external` 记录展示。点击“接管”后，面板会先备份片段，短暂停止共享服务，把该片段迁移到独立服务并校验；失败时恢复原片段和共享服务。外部节点与面板管理节点使用不同配置和服务命名空间，可以共存，但端口不能重复。

## 回滚

删除面板管理用户时只停止对应的 `fluxlite-singbox-<id>.service` 并删除该用户目录。Realm 链路、共享 `sing-box` 服务和一键脚本配置不受影响。
