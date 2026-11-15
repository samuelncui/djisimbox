# Everyday use

**English** · [简体中文](operations.zh-CN.md)

## Change recipients or add a SIM

The easiest way is the guide: run `sudo djisimbox setup`. It lists the connected SIMs and recent chats, lets you choose recipients and the language, and restarts the service. It rewrites `/etc/djisimbox/config.yaml`, so comments you added by hand are not kept.

To edit by hand instead, open `/etc/djisimbox/config.yaml`. Each SIM has an entry in `routes`; each recipient has an entry in that route's `recipients`. This example sends the first SIM's notifications to two people and the second SIM's to another person. Replace all IDs. Comments start with `#`:

```yaml
version: 1
language: en   # notification language: en or zh-CN
routes:
  - iccid: "89000000000000000001"
    # name: Work SIM
    recipients:
      - bot: default
        chat_id: "123456789"
      - bot: default
        chat_id: "234567890"
  - iccid: "89000000000000000002"
    name: Spare SIM
    recipients:
      - bot: default
        chat_id: "345678901"
```

Use each SIM's full ICCID, rather than its phone number or IMSI; routing matches only the ICCID. `name` is optional: without it, notifications show the phone number stored on the SIM, or `ICCID 尾号 1234` (the last four ICCID digits) if the SIM does not store it. A `name` must be 1-80 characters on one line. Find additional SIMs with `sudo mmcli -L`, use `sudo mmcli -m MODEM_ID` to find the SIM path, then read the ICCID with `sudo mmcli -i SIM_ID`; the modem and SIM IDs may differ. Several SIMs online at once require several modems, one SIM each; two modems together have been tested on hardware. For a new recipient, [find their chat ID](#find-a-telegram-chat-id) first.

To use another bot, add an entry to `bots` in `/etc/djisimbox/secrets.yaml`, such as `family: "full token for another bot"`, and change the relevant recipient's `bot` to `family`.

Save and check the configuration, then restart if the check succeeds:

```sh
sudoedit /etc/djisimbox/config.yaml /etc/djisimbox/secrets.yaml
sudo -u djisimbox /usr/local/bin/djisimbox check
sudo systemctl restart djisimbox
```

If the service refuses to start after an edit, the error names the file and line number. Unknown or misspelled fields, duplicate keys, and extra YAML documents are rejected. Changes take effect only after the restart; there is no hot reload.

Send a test SMS to confirm the intended recipients receive it.

### Find a Telegram chat ID

Ask the recipient to send `/start` to the bot. In a Bash terminal, query its updates and enter the token when prompted; input is hidden:

```bash
read -rsp 'Bot token: ' BOT_TOKEN; printf '\n'
printf 'url = "https://api.telegram.org/bot%s/getUpdates"\n' "$BOT_TOKEN" |
  curl --fail --silent --show-error --config -
unset BOT_TOKEN
```

Use `message.chat.id` from the recipient's message as a string in the configuration, such as `"123456789"`. For a group, add the bot and send `/start@YourBotUsername`. For a channel, make the bot an administrator with permission to post messages, publish a test message, and use `channel_post.chat.id`. Keep the minus sign in group and channel IDs.

If `getUpdates` is empty, send another message and query again. It returns only recent updates that no program has confirmed yet (Telegram keeps them about 24 hours), so if the recipient chatted with the bot long ago, ask them to send any new message to the bot (no need to `/start` again) and query again. If a Webhook or another program already uses the bot, reuse a known chat ID or create a new bot. Keep the existing Webhook enabled.

Without a bot, open the chat in Telegram Web (web.telegram.org): the number after `#` in the address bar is that chat's ID. For your own ID, open Saved Messages. Keep the minus sign for groups. Sending notifications does not require continuous polling; notifications go only to explicitly configured IDs.

## View status and logs

```sh
sudo systemctl status djisimbox --no-pager
sudo -u djisimbox /usr/local/bin/djisimbox status
sudo journalctl --namespace=djisimbox -u djisimbox --no-pager -n 30
```

After successful delivery, `queue.Messages` should return to `0`. Check status and logs for modem, configuration, network, or delivery errors. For live logs, add `-f` to the `journalctl` command. Logs may disappear after a reboot; save them before troubleshooting and remove tokens, SIM IDs, and messages before sharing them publicly.

## No notifications

- **Modem missing:** Run `sudo mmcli -L`. Check power and the USB data cable on USB1 if the list is empty.
- **No SMS on the modem:** Use the number from `mmcli -L` in `sudo mmcli -m MODEM_ID` and `sudo mmcli -m MODEM_ID --messaging-list-sms`. Check that the SIM is ready and registered. If a new SMS still does not appear, test the SIM in a phone.
- **SMS present, but no Telegram notification:** Check status and logs, Wi-Fi access to Telegram, the configured ICCID, token, and chat ID, and the bot's sending permissions. After fixing the issue, check the configuration, restart the service, and send another test SMS.
- **Cellular interface protection fails:** Confirm systemd-networkd is active and the modem's USB identity is `2c7c:0125`, then run the install command again. Other hardware or network managers need their own interface protection setup.
- **SMS works, but call alerts do not:** The installer does not configure voice or VoLTE. Call alerts require a real incoming-call test with each SIM, firmware, and carrier combination.

## Network outages, restarts, and SIM changes

Delivery is retried after a network outage. SMS messages are deleted from the modem only after all recipients receive them; modem storage may fill up during a long outage. A restart may resend messages that have not yet been deleted and lose pending call alerts; calls during downtime may also be missed.

A long SMS arrives in several parts. If some parts never arrive, it cannot be put together or forwarded; after 24 hours it is deleted from the modem to free its storage.

Before changing a SIM, wait for `queue.Messages` to reach `0`, then use `sudo mmcli -m MODEM_ID --messaging-list-sms` to confirm the modem has no old SMS messages. Deal with any old messages first so they are not sent to the new SIM's recipients.

## Upgrade or stop

Wait until `queue.Messages` is `0` and there is no call in progress, then run the install command again:

```sh
sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/samuelncui/djisimbox/main/install.sh)"
```

The installer preserves `/etc/djisimbox/config.yaml` and `/etc/djisimbox/secrets.yaml`, updates the program and service files, checks the setup, and restarts the service. Send a test SMS to confirm the upgrade.

To stop the service and disable startup at boot:

```sh
sudo systemctl disable --now djisimbox
```

Keep the cellular interface protection rule until you are ready to enable cellular data.
