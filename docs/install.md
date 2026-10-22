# Install on Zero 2W

**English** · [简体中文](install.zh-CN.md)

Use ARM64 Armbian / Debian 13 with systemd-networkd. Prepare a microSD card using an [Armbian image](https://armbian.com/boards/orangepizero2w), set up Wi-Fi with access to Telegram, and confirm you can connect over SSH. Use an activated SIM with its PIN lock disabled. USB0 is for power; USB1 connects to the modem with a data cable. For first-time setup, leave the modem disconnected until the installer asks you to connect it.

## 1. Download and verify

On your computer, sign in to GitHub and open [CI builds](https://github.com/samuelncui/djisimbox/actions/workflows/ci.yml?query=branch%3Amain). Select the latest successful green run on `main`. Under **Artifacts**, download `djisimbox-dev-…-linux`. Extract the ZIP and find the package ending in `linux_arm64.tar.gz` and `SHA256SUMS`.

Upload these two files from their directory to the board. Replace the SSH address below with your own:

```sh
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
ssh orangepi@orangepizero2w.local 'mkdir -p ~/djisimbox-install'
scp "$ASSET" SHA256SUMS orangepi@orangepizero2w.local:djisimbox-install/
ssh orangepi@orangepizero2w.local
```

**Run the remaining commands on the board.** Verify and extract the package:

```sh
cd ~/djisimbox-install
ASSET=$(awk '$2 ~ /_linux_arm64.tar.gz$/ {print $2}' SHA256SUMS)
sha256sum --check --ignore-missing SHA256SUMS &&
  tar -xzf "$ASSET" &&
  cd "${ASSET%.tar.gz}"
```

The ARM64 package must show `OK`; download it again if verification fails. Keep the terminal in this extracted directory.

## 2. Run the installer

Create a bot through [BotFather](https://core.telegram.org/bots/features#botfather), save its token, and ask the intended recipient to send `/start` to the bot in a private chat. Then run:

```sh
sudo ./install.sh
```

The installer protects the DJI modem's USB network interfaces (`2c7c:0125`) before downloading dependencies, then installs the required packages and service files. It requires systemd-networkd and keeps Telegram traffic on Wi-Fi.

On first setup, follow the prompts to:

1. Connect the modem when asked, choose a SIM by the last six ICCID digits shown, and enter the SIM name to show in notifications.
2. Enter the bot token; your input is hidden.
3. Enter the intended recipient's numeric chat ID. The installer queries `getUpdates` to show available IDs when possible; only the ID you enter receives notifications.

If the bot already has a Webhook or another program polling its updates, enter a known chat ID manually or use a new bot. Keep the existing Webhook enabled. See [chat ID help](operations.md#find-a-telegram-chat-id) for groups, channels, or an empty result.

The installer writes private configuration files in `/etc/djisimbox`, checks the configuration and modem, and starts the service with startup at boot enabled. Running it again preserves existing configuration and tokens while updating the program and service files. First setup configures one SIM and one recipient; [add more routes or recipients](operations.md#change-recipients-or-add-a-sim) afterward.

## 3. Receive your first notification

Send a normal test SMS to the selected SIM. Installation is complete when the intended Telegram recipient receives a notification like this:

```text
短信通知
SIM: Work SIM
号码: 10000
时间: 2026-01-01T12:00:00Z

Test SMS
```

If you need call alerts, call the SIM and confirm you receive `来电通知`. The installer's checks do not send a test notification. See [everyday use](operations.md) for status, logs, upgrades, and troubleshooting.
