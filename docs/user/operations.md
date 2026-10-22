# 日常运维

## 查看运行状态

```sh
/usr/local/bin/djisimbox version
sudo systemctl status djisimbox --no-pager
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo -u djisimbox /usr/local/bin/djisimbox status
sudo journalctl --namespace=djisimbox -u djisimbox --no-pager -n 30
```

| 命令 | 用途 |
| --- | --- |
| `version` | 输出构建版本。 |
| `check [-config PATH]` | 加载并校验配置与凭据，确认 runtime 位于 tmpfs，检查蜂窝主机接口并列出 ModemManager 设备；不要求服务正在运行。 |
| `status [-config PATH]` | 加载配置并访问运行进程的私有 Unix socket，返回计数与队列预算；由服务账户执行。 |
| `run [-config PATH]` | 前台运行，通常由 systemd 管理；启动时加载配置，并独占该运行目录。 |

默认配置路径为 `/etc/djisimbox/config.json`。`check` 会显示设备的 `sim_suffix`，仍应在公开日志前去掉；它不会验证 Telegram 凭据的在线有效性。`status` 不包含短信正文、SIM 标识或 token，但它的成功只证明状态端点可访问。

`status` 的 `storage` 应为 `memory`，`queue` 的字段如下。统计同时包含短信和来电，重启后重置。

| 字段 | 含义 |
| --- | --- |
| `Messages` | 当前活动通知数，包括等待原件释放的通知。 |
| `Pending` | 待投递文本分段数；重试中的分段也计入。 |
| `Delivered` | 本进程累计获 Telegram 成功响应的分段数。 |
| `Failed` | 当前永久失败的分段数，需要处理目标或凭据。 |
| `Quarantined` | 当前没有匹配收件人的通知数。 |
| `Forwarded` | 全部目标成功，且已完成短信原件删除确认或来电本地确认的通知数。 |
| `Bytes` | 当前队列的逻辑字节预算用量。 |

查看实时日志可用 `sudo journalctl --namespace=djisimbox -u djisimbox -f`。日志保留在 RAM 且有容量限制，重启或滚动后可能消失；日志包含 SIM 显示名称和运行标识，公开前应审阅。

## 确认接收和投递

短信通知标题为 `短信通知`，之后依次是可取得的 `SIM`、`号码`、`时间`，空行后为短信正文。正文按 Unicode 边界分段，使用纯文本发送。短信时间来自 modem，缺失时省略。

来电通知标题为 `来电通知`，使用同样的字段顺序；号码不可取得时显示 `未知号码`，时间为本地第一次观察到响铃的时间。程序仅发送文字通知，来电本身仍由模块和运营商处理。

验收时分别检查以下结果：

1. 普通短信在全部目标出现，长短信分段顺序正确；日志出现 `Telegram delivery confirmed`。
2. 所有目标成功后出现 `notification delivered to all recipients; source released`，队列条目释放。对于短信，可用 `sudo mmcli -m ID --messaging-list-sms` 确认该原件已删除，`ID` 替换为实际 modem ID。
3. 若需要来电通知，真实呼入能进入 ModemManager 的来电对象并生成 Telegram 通知。`voice: true`、LTE 注册或 IMS 状态都不能替代该检查。
4. 蜂窝主机接口持续关闭、没有地址，默认路由仍经独立网络。使用 `ip -s link show dev "$MODEM_IFACE"` 比较测试前后计数，变量取安装时确认的模块网口名。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| `cellular host interface must be down without addresses` | 检查实际网口、USB 身份、networkd 首个匹配规则及残留地址，恢复[安装时的保护](install.md#2-先保护蜂窝网口)后再启动。 |
| `runtime_dir must be on tmpfs` | 使用默认 `/run/djisimbox`，检查 `/run` 的文件系统和 systemd 的 `RuntimeDirectory`。 |
| `check` 的 `modems` 为空 | 检查供电、USB 数据连接、ModemManager、SIM 与串口识别。成功退出不等于找到硬件。 |
| `Quarantined` 大于零 | 核对完整 ICCID 是否有路由、身份是否可读取；按[配置说明](configuration.md)修正并重启。 |
| `Pending` 持续增加 | 检查独立网络、DNS、HTTPS 与 Telegram 可达性。临时网络错误和限流自动退避重试，观察模块剩余短信容量。 |
| `Failed` 大于零或日志出现 `Telegram destination requires attention` | 核对 bot token、目标数字 ID、bot 是否被屏蔽或失去发送权限；修复后重启以重新读取保留短信。已成功目标可能重复收到。 |
| Telegram 全部成功但 `Messages` 尚未释放 | 查看原件删除/确认错误；程序保留条目并重试。清理 modem 原件会改变恢复来源，先确认各目标已收到。 |
| 短信正常、来电没有通知 | 检查 Voice 接口、运营商/SIM 的来电服务及 IMS/VoLTE。按[硬件研究](https://github.com/samuelncui/djisimbox/blob/main/docs/development/volte-research.md)的证据边界诊断，配置变更后分别复验来电和短信。 |
| modem、SIM 或 ModemManager 发生变更后服务重启 | 程序用重启重新建立来源关系，systemd 会重试启动；仍保留的短信可能重复发送。 |

## 断网、重启和换卡

短信只有在所有目标的全部分段成功后才删除。暂时断网或队列满时，未接纳的短信留在 modem；可容纳的时间受模块存储容量限制。永久失败的目标也会阻止原件删除。完整断电后 modem 是否保留原件需要按具体硬件验证。

内存中的投递记录随重启消失，无法保证仅发送一次。来电没有可供恢复的短信原件，服务停机、过短响铃、队列拒绝或重启可能丢失通知；已入队通知在同一次进程中可等待网络恢复。换 SIM 前排空该 modem 的短信，以免把旧消息归给新 SIM。

## 升级与卸载

取得并校验新包后，先查看 `status`，尽量等队列清空且没有来电。停止服务，将新包内的程序替换到原位置，再启动并检查版本、状态、短信和来电。以下命令从新包目录执行：

```sh
sudo systemctl stop djisimbox
sudo install -m 0755 djisimbox /usr/local/bin/djisimbox
sudo systemctl start djisimbox
/usr/local/bin/djisimbox version
sudo -u djisimbox /usr/local/bin/djisimbox status
```

保留现有 `/etc/djisimbox`。若该版本的部署文件有变化，按安装指南更新对应文件并执行 `sudo systemctl daemon-reload`；日志配置更新还需重启 `systemd-journald@djisimbox`，使新限制生效。重启带来的重复短信和来电丢失语义同上。

停止并取消开机启动：

```sh
sudo systemctl disable --now djisimbox
```

卸载时移除本程序的 binary、systemd unit、Polkit 规则、日志命名空间配置及其 drop-in，执行 `sudo systemctl daemon-reload`。按自己的保留策略处理静态配置与凭据。蜂窝网口保护涉及 SIM 的数据使用政策，保留该规则，直到明确准备启用主机蜂窝数据。
