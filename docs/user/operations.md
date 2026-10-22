# 日常使用

## 修改收件人或增加 SIM

编辑 `/etc/djisimbox/config.json`。每张 SIM 在 `routes` 中占一项；每位收件人在该项的 `recipients` 中占一项。下面示例把第一张卡发给两人，第二张卡发给另一人，所有编号都需替换：

```json
{
  "version": 1,
  "routes": [
    {
      "iccid": "89000000000000000001",
      "name": "工作卡",
      "recipients": [
        {"bot": "default", "chat_id": "123456789"},
        {"bot": "default", "chat_id": "234567890"}
      ]
    },
    {
      "iccid": "89000000000000000002",
      "name": "备用卡",
      "recipients": [
        {"bot": "default", "chat_id": "345678901"}
      ]
    }
  ]
}
```

新收件人需先给 bot 发 `/start`，再按[安装步骤](install.md#3-取得-sim-编号和-telegram-收件人)取得聊天编号。群组中先加入 bot 并发 `/start@你的Bot用户名`；频道中将 bot 设为有发消息权限的管理员，再发布测试消息。频道编号取 `channel_post.chat.id`；群组和频道编号的负号要保留。

需要不同 bot 时，在 `/etc/djisimbox/secrets.json` 的 `bots` 中增加一项，例如 `"family": "另一个 bot 的完整 token"`，并把相应收件人的 `bot` 改为 `family`。

保存并检查，成功后重启生效：

```sh
sudoedit /etc/djisimbox/config.json /etc/djisimbox/secrets.json
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo systemctl restart djisimbox
```

## 查看状态和日志

```sh
sudo systemctl status djisimbox --no-pager
sudo -u djisimbox /usr/local/bin/djisimbox status
sudo journalctl --namespace=djisimbox -u djisimbox --no-pager -n 30
```

正常投递后，`queue.Messages` 应回到 `0`。若 `Pending` 持续增加，检查网络；`Failed` 大于零时检查 bot 和发送权限；`Quarantined` 大于零时核对 ICCID 是否已配置。

实时日志使用 `sudo journalctl --namespace=djisimbox -u djisimbox -f`。日志重启后可能消失，排错前先保存；公开日志或命令输出前，去掉 token、SIM 编号和消息等私人信息。

## 没有收到通知

1. **找不到模块**：运行 `sudo mmcli -L`。列表为空时，先确认 USB 数据线接在 USB1、模块已供电，再刷新识别：

   ```sh
   sudo systemctl restart ModemManager
   sudo udevadm trigger --action=change --subsystem-match=tty --sysname-match='ttyUSB*'
   sudo udevadm settle
   sudo mmcli -L
   ```

2. **模块没有短信**：运行 `sudo mmcli -m MODEM_ID` 查看 SIM 是否就绪、是否注册网络，再运行 `sudo mmcli -m MODEM_ID --messaging-list-sms`。`MODEM_ID` 换成上一步编号；新发测试短信仍未出现时，先用手机验证 SIM 能否在当地接收 SMS。
3. **模块有短信，Telegram 没有**：用上面的 `status` 和日志定位。网络可达性可用 `curl --head --connect-timeout 10 https://api.telegram.org` 检查；核对配置中的 ICCID、token、聊天编号，以及 bot 是否被屏蔽或失去权限。修正后检查配置并重启，再发一条测试短信。
4. **报蜂窝网口未关闭**：按[网口保护步骤](install.md#2-关闭蜂窝网口)恢复规则。若未匹配，检查模块 USB 身份及是否有更早的 networkd 规则覆盖它；本项目规则只匹配 `2c7c:0125`。
5. **短信正常，来电没有通知**：程序不会自动配置模块的来电服务；不同 SIM、固件和运营商需单独验证。进一步诊断见[VoLTE 研究](https://github.com/samuelncui/djisimbox/blob/main/docs/development/volte-research.md)。

### 查不到 Telegram 聊天编号

`getUpdates` 为空时，给 bot 再发一条消息后查询。已有其他程序或 Webhook 使用该 bot 时，沿用它已知的聊天编号，或新建 bot；不要关闭现有 Webhook。本程序发送通知不需要持续查询更新。

## 断网、重启与换卡

断网后会重试，短信在所有收件人收到前保留在模块；模块存储有限，长期断网可能收满。重启可能重复发送尚未删除的短信，也可能丢失待发来电提醒。

换 SIM 前先等 `queue.Messages` 变为 `0`，再用 `sudo mmcli -m MODEM_ID --messaging-list-sms` 确认模块没有旧短信；有旧短信时先处理，避免发给新卡的收件人。

## 升级或停用

按[下载步骤](install.md#1-下载程序)取得新包，在新的空目录中校验并解包，保留现有 `/etc/djisimbox` 配置。若版本说明要求更新部署文件，先重新复制[服务文件](install.md#安装服务文件)并执行 `sudo systemctl daemon-reload`；日志配置变更后另执行 `sudo systemctl restart systemd-journald@djisimbox`。

等 `queue.Messages` 为 `0` 且没有来电后，从新包目录执行：

```sh
sudo systemctl stop djisimbox
sudo install -m 0755 djisimbox /usr/local/bin/djisimbox
sudo systemctl start djisimbox
/usr/local/bin/djisimbox version
sudo -u djisimbox /usr/local/bin/djisimbox status
```

最后发送测试短信确认。

停止服务并取消开机启动：

```sh
sudo systemctl disable --now djisimbox
```

停用后保留蜂窝网口保护规则，直到你准备启用蜂窝数据。
