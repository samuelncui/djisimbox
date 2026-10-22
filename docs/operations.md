# Everyday use

**English** · [简体中文](operations.zh-CN.md)

## Change recipients or add a SIM

Edit `/etc/djisimbox/config.json`. Each SIM has an entry in `routes`; each recipient has an entry in that route's `recipients`. This example sends the first SIM's notifications to two people and the second SIM's to another person. Replace all IDs:

```json
{
  "version": 1,
  "routes": [
    {
      "iccid": "89000000000000000001",
      "name": "Work SIM",
      "recipients": [
        {"bot": "default", "chat_id": "123456789"},
        {"bot": "default", "chat_id": "234567890"}
      ]
    },
    {
      "iccid": "89000000000000000002",
      "name": "Spare SIM",
      "recipients": [
        {"bot": "default", "chat_id": "345678901"}
      ]
    }
  ]
}
```

New recipients must first send `/start` to the bot, then follow the [installation steps](install.md#3-get-the-sim-iccid-and-telegram-chat-ids) to find their chat IDs. For a group, add the bot and send `/start@YourBotUsername`. For a channel, make the bot an administrator with permission to post messages, then publish a test message. Use `channel_post.chat.id` for the channel ID. Keep the minus sign in group and channel IDs.

To use another bot, add an entry to `bots` in `/etc/djisimbox/secrets.json`, such as `"family": "full token for another bot"`, and change the relevant recipient's `bot` to `family`.

Save and check the configuration, then restart if the check succeeds:

```sh
sudoedit /etc/djisimbox/config.json /etc/djisimbox/secrets.json
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo systemctl restart djisimbox
```

## View status and logs

```sh
sudo systemctl status djisimbox --no-pager
sudo -u djisimbox /usr/local/bin/djisimbox status
sudo journalctl --namespace=djisimbox -u djisimbox --no-pager -n 30
```

After successful delivery, `queue.Messages` should return to `0`. If `Pending` keeps growing, check the network. If `Failed` is greater than zero, check the bot and its sending permissions. If `Quarantined` is greater than zero, check that the ICCID is configured.

For live logs, use `sudo journalctl --namespace=djisimbox -u djisimbox -f`. Logs may disappear after a reboot, so save them before troubleshooting. Remove tokens, SIM IDs, messages, and other private information before sharing logs or command output publicly.

## No notifications

1. **Modem not found**: Run `sudo mmcli -L`. If the list is empty, check that the USB data cable is connected to USB1 and the modem has power, then refresh detection:

   ```sh
   sudo systemctl restart ModemManager
   sudo udevadm trigger --action=change --subsystem-match=tty --sysname-match='ttyUSB*'
   sudo udevadm settle
   sudo mmcli -L
   ```

2. **No SMS on the modem**: Run `sudo mmcli -m MODEM_ID` to check whether the SIM is ready and registered on the network, then run `sudo mmcli -m MODEM_ID --messaging-list-sms`. Replace `MODEM_ID` with the number from the previous step. If a new test SMS still does not appear, use a phone to check whether the SIM can receive SMS locally.
3. **SMS on the modem, but nothing in Telegram**: Use `status` and the logs above to find the problem. Check connectivity with `curl --head --connect-timeout 10 https://api.telegram.org`. Verify the configured ICCID, token, and chat ID, and check whether the bot is blocked or has lost permissions. After fixing the issue, check the configuration, restart, and send another test SMS.
4. **Cellular network interface reported as still enabled**: Restore the rules using the [interface protection steps](install.md#2-disable-the-cellular-network-interface). If they do not match, check the modem's USB identity and whether an earlier networkd rule overrides them. This project's rule matches only `2c7c:0125`.
5. **SMS works, but no call alerts**: The program does not automatically configure the modem's call service. Each SIM, firmware, and carrier combination needs testing. See [VoLTE research](https://github.com/samuelncui/djisimbox/blob/main/.agents/volte-research.md) for further diagnosis.

### Cannot find a Telegram chat ID

If `getUpdates` is empty, send another message to the bot and query again. If another program or Webhook already uses the bot, reuse its known chat ID or create a new bot. Keep the existing Webhook enabled. This program does not need to keep polling for updates to send notifications.

## Network outages, restarts, and SIM changes

Delivery is retried after a network outage. SMS messages stay on the modem until all recipients receive them. Modem storage is limited and may fill up during a long outage. A restart may resend SMS messages that have not yet been deleted and may lose pending call alerts.

Before changing a SIM, wait for `queue.Messages` to reach `0`, then use `sudo mmcli -m MODEM_ID --messaging-list-sms` to confirm the modem has no old SMS messages. Deal with any old messages first so they are not sent to the new SIM's recipients.

## Upgrade or stop

Get the new package using the [download steps](install.md#1-download-the-program), then verify and extract it in a new empty directory. Keep the existing `/etc/djisimbox` configuration. If the release notes require deployment file updates, copy the [service files](install.md#install-service-files) again and run `sudo systemctl daemon-reload`. After changing the log configuration, also run `sudo systemctl restart systemd-journald@djisimbox`.

Wait until `queue.Messages` is `0` and there is no call in progress, then run these commands from the new package directory:

```sh
sudo systemctl stop djisimbox
sudo install -m 0755 djisimbox /usr/local/bin/djisimbox
sudo systemctl start djisimbox
/usr/local/bin/djisimbox version
sudo -u djisimbox /usr/local/bin/djisimbox status
```

Send a test SMS to confirm it works.

To stop the service and disable startup at boot:

```sh
sudo systemctl disable --now djisimbox
```

Keep the cellular interface protection rule after stopping the service, until you are ready to enable cellular data.
