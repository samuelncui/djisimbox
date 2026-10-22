# Install on Zero 2W

**English** · [简体中文](install.zh-CN.md)

This guide uses ARM64 Armbian / Debian 13 with systemd-networkd. Prepare a microSD card using an [Armbian image](https://armbian.com/boards/orangepizero2w), set up Wi-Fi, and confirm you can connect over SSH. USB0 is for power; USB1 connects to the modem with a data cable. **Keep the modem disconnected until step 2 tells you to connect it.**

## 1. Download the program

On your computer, sign in to GitHub and open [CI builds](https://github.com/samuelncui/djisimbox/actions/workflows/ci.yml?query=branch%3Amain). Select the latest successful green run on `main`. Under **Artifacts** at the bottom of the page, download `djisimbox-dev-…-linux`. Extract the ZIP and find the package ending in `linux_arm64.tar.gz` and `SHA256SUMS`.

Upload these two files from their directory to the board. Replace the SSH address below with your own:

```sh
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
ssh orangepi@orangepizero2w.local 'mkdir -p ~/djisimbox-install'
scp "$ASSET" SHA256SUMS orangepi@orangepizero2w.local:djisimbox-install/
ssh orangepi@orangepizero2w.local
```

**Run all remaining commands on the board.** Install dependencies, verify the download, and extract it:

```sh
sudo apt-get update
sudo apt-get install --no-install-recommends ca-certificates curl modemmanager polkitd
cd ~/djisimbox-install
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
sha256sum --check --ignore-missing SHA256SUMS &&
  tar -xzf "$ASSET" &&
  cd "${ASSET%.tar.gz}"
```

The ARM64 package should show `OK`; download it again if verification fails. Keep running the following commands from this extracted directory.

## 2. Disable the cellular network interface

Run `systemctl is-active systemd-networkd`; it should report `active`. Systems using another network manager need their own interface protection setup; the rules below cannot be used directly.

```sh
sudo install -m 0644 deploy/networkd/05-djisimbox-sms-only.network /etc/systemd/network/
sudo networkctl reload
```

Connect the modem now. Run `networkctl list` and find the new USB network interface, such as `usb0` or a name beginning with `enx`. Replace the placeholder below:

```sh
MODEM_IFACE=replace_with_modem_interface
sudo networkctl reconfigure "$MODEM_IFACE"
networkctl status "$MODEM_IFACE" --no-pager
ip -br address show dev "$MODEM_IFACE"
ip route show default
ip -6 route show default
```

Confirm that `Network File` is `05-djisimbox-sms-only.network`, the interface is `DOWN` with no IP address, and the default routes still use Wi-Fi. This rule covers the verified DJI modem. If it does not match, disconnect the modem and set up protection before continuing.

## 3. Get the SIM ICCID and Telegram chat IDs

Start the modem management service, then run these queries one at a time. Replace `MODEM_ID` with the number from `mmcli -L` and `SIM_ID` with the number at the end of the SIM path shown by `mmcli -m`. They may differ.

```sh
sudo systemctl enable --now ModemManager
sudo mmcli -L
sudo mmcli -m MODEM_ID
sudo mmcli -i SIM_ID
```

Copy the SIM's full **ICCID**, rather than its phone number or IMSI. If the modem is missing, see [troubleshooting](operations.md#no-notifications).

Create a bot through [BotFather](https://core.telegram.org/bots/features#botfather) and save its token. Ask the recipient to send `/start` in a private chat with the bot. In the board's Bash terminal, run the following and enter the token when prompted:

```bash
read -rsp 'Bot token: ' BOT_TOKEN; printf '\n'
printf 'url = "https://api.telegram.org/bot%s/getUpdates"\n' "$BOT_TOKEN" |
  curl --fail --silent --show-error --config -
unset BOT_TOKEN
```

Find the message just sent and copy `message.chat.id`, such as `123456789` below. Enter it as a string in the configuration: `"123456789"`.

```json
{"result":[{"message":{"chat":{"id":123456789},"text":"/start"}}]}
```

If the result is empty, send `/start` again and repeat the query. For conflicts with an existing bot, see [troubleshooting](operations.md#cannot-find-a-telegram-chat-id). For groups and channels, see [adding recipients](operations.md#change-recipients-or-add-a-sim).

## 4. Configure and install the service

```sh
sudo useradd --system --user-group --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin djisimbox
sudo install -m 0755 djisimbox /usr/local/bin/djisimbox
sudo install -d -m 0750 -o root -g djisimbox /etc/djisimbox
sudo install -m 0600 -o djisimbox -g djisimbox examples/config.json /etc/djisimbox/config.json
sudo install -m 0600 -o djisimbox -g djisimbox examples/secrets.json /etc/djisimbox/secrets.json
sudoedit /etc/djisimbox/config.json /etc/djisimbox/secrets.json
```

- In `config.json`, replace `iccid` with the ICCID you found, `name` with the SIM name to show in alerts, and `chat_id` with the recipient's chat ID.
- In `secrets.json`, replace the token placeholder with the full token from BotFather. Keep both bot names as `default`.

### Install service files

Save the configuration, then run:

```sh
sudo install -m 0644 deploy/polkit/50-djisimbox.rules /etc/polkit-1/rules.d/
sudo install -m 0644 deploy/systemd/djisimbox.service /etc/systemd/system/
sudo install -m 0644 deploy/systemd/journald@djisimbox.conf /etc/systemd/
sudo install -d -m 0755 /etc/systemd/system/systemd-journald@djisimbox.service.d
sudo install -m 0644 deploy/systemd/journald-djisimbox-volatile.conf /etc/systemd/system/systemd-journald@djisimbox.service.d/50-volatile.conf
sudo systemctl daemon-reload
sudo -u djisimbox /usr/local/bin/djisimbox check
```

Once `check` succeeds and `modems` includes your modem, start the service and enable it at boot:

```sh
sudo systemctl enable --now djisimbox
sudo systemctl status djisimbox --no-pager
```

## 5. Receive your first notification

Send a normal test SMS to the SIM. Confirm that the intended Telegram recipient receives a notification like this:

```text
短信通知
SIM: Work SIM
号码: 10000
时间: 2026-01-01T12:00:00Z

Test SMS
```

If you need call alerts, call the SIM and confirm you receive `来电通知`. `check` does not send a test notification. See [everyday use](operations.md) to change recipients, upgrade, or troubleshoot.
