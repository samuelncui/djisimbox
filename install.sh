#!/usr/bin/env bash
set -euo pipefail
umask 077

die() { printf '%s\n' "$*" >&2; exit 1; }

is_dji() {
  local device=$1
  while [[ "$device" != / && -d "$device" ]]; do
    if [[ -f "$device/idVendor" && -f "$device/idProduct" ]]; then
      [[ $(<"$device/idVendor") == 2c7c && $(<"$device/idProduct") == 0125 ]]
      return
    fi
    device=${device%/*}
  done
  return 1
}

protect_interfaces() {
  local iface device name status
  for iface in "${1:-/sys/class/net}"/*; do
    [[ -e "$iface/device" ]] || continue
    device=$(readlink -f "$iface/device")
    if ! is_dji "$device"; then continue; fi
    name=${iface##*/}
    ip link set dev "$name" down
    ip address flush dev "$name"
    networkctl reconfigure "$name"
    status=$(LC_ALL=C networkctl status "$name" --no-pager)
    if [[ "$status" != *'/etc/systemd/network/05-djisimbox-sms-only.network'* ]]; then
      ip link set dev "$name" down
      ip address flush dev "$name"
      die 'Another network rule overrides modem protection. Fix it before continuing.'
    fi
  done
}

discover_sims() {
  local modem info device sim iccid modems
  modems=$(mmcli --list-modems --output-json 2>/dev/null) || return 0
  while IFS= read -r modem; do
    info=$(mmcli --modem "$modem" --output-json 2>/dev/null) || continue
    device=$(jq -r '.modem.generic.device' <<<"$info")
    is_dji "$device" || continue
    sim=$(jq -r '.modem.generic.sim' <<<"$info")
    [[ "$sim" == /org/freedesktop/ModemManager1/SIM/* ]] || continue
    iccid=$(mmcli --sim "$sim" --output-json 2>/dev/null | jq -r '.sim.properties.iccid') || continue
    [[ "$iccid" =~ ^[0-9]{18,22}$ ]] || continue
    printf '%s\n' "$iccid"
  done < <(jq -r '."modem-list"[]' <<<"$modems")
}

valid_chat_id() {
  [[ "$1" =~ ^-?[1-9][0-9]*$ ]] || return 1
  local digits=${1#-} limit=9223372036854775807
  [[ $1 != -* ]] || limit=9223372036854775808
  (( ${#digits} < ${#limit} )) ||
    [[ ${#digits} == ${#limit} && ( "$digits" < "$limit" || "$digits" == "$limit" ) ]]
}

configure() {
  local directory=$1 rows attempt selection iccid='' name token updates chat index
  local -a sims
  if [[ -e "$directory/config.json" || -L "$directory/config.json" ||
        -e "$directory/secrets.json" || -L "$directory/secrets.json" ]]; then
    [[ -f "$directory/config.json" && ! -L "$directory/config.json" &&
       -f "$directory/secrets.json" && ! -L "$directory/secrets.json" ]] ||
      die 'Both config.json and secrets.json must be regular files. Existing files were kept.'
    printf '%s\n' 'Keeping existing configuration.'
    return
  fi

  printf '%s\n' 'Connect the DJI modem with its SIM, then press Enter.'
  read -r
  for attempt in {1..15}; do
    protect_interfaces
    rows=$(discover_sims)
    [[ -n "$rows" ]] && break
    sleep 2
  done
  [[ -n "$rows" ]] || die 'No ready DJI SIM found. Check USB1, power and SIM PIN, then rerun.'
  mapfile -t sims <<<"$rows"
  for index in "${!sims[@]}"; do
    printf '%s) SIM ending %s\n' "$((index + 1))" "${sims[index]: -6}"
  done
  read -rp 'SIM number [1]: ' selection
  selection=${selection:-1}
  for index in "${!sims[@]}"; do
    if [[ "$selection" == "$((index + 1))" ]]; then iccid=${sims[index]}; fi
  done
  [[ -n "$iccid" ]] || die 'Choose a SIM number from the list.'
  read -rp 'SIM display name [SIM 1]: ' name
  name=${name:-SIM 1}
  jq -ne --arg name "$name" '$name | (length <= 80) and test("\\S") and
    (test("[[:cntrl:]\u2028\u2029]") | not)' >/dev/null ||
    die 'SIM display name must be 1-80 characters, include non-whitespace text, and have no control characters.'
  read -rsp 'Telegram bot token: ' token
  printf '\n'
  [[ "$token" =~ ^[0-9]+:[A-Za-z0-9_-]{20,128}$ ]] || die 'Invalid bot token format.'

  printf '%s\n' 'Send /start to your bot. Available chats (if the bot has received updates):'
  read -rp 'Press Enter after sending /start: '
  if updates=$(printf 'url = "https://api.telegram.org/bot%s/getUpdates"\n' "$token" |
      curl --fail --silent --show-error --connect-timeout 10 --max-time 20 --config -); then
    jq -r '[.result[]? | (.message.chat // .channel_post.chat // empty)] |
      unique_by(.id)[] | "\(.id)  \(.title // .username // .first_name // "chat")" |
      gsub("[[:cntrl:]]"; "")' <<<"$updates"
  else
    printf '%s\n' 'Could not list chats. Enter a known chat ID or use a new bot; existing webhooks are kept.'
  fi
  read -rp 'Telegram chat ID (one recipient): ' chat
  valid_chat_id "$chat" || die 'Enter a nonzero signed 64-bit chat ID, keeping any minus sign.'

  jq -n --arg iccid "$iccid" --arg name "$name" --arg chat "$chat" \
    '{version: 1, routes: [{iccid: $iccid, name: $name,
      recipients: [{bot: "default", chat_id: $chat}]}]}' >"$directory/config.json"
  printf '%s' "$token" | jq -Rs '{bots: {default: .}}' >"$directory/secrets.json"
}

main() {
  if [[ ${1:-} == --help ]]; then
    printf '%s\n' 'Usage: sudo ./install.sh' \
      'Install or upgrade on Debian/Armbian with systemd-networkd.' \
      'The first run guides SIM and Telegram setup; reruns preserve configuration.'
    return
  fi
  [[ $# == 0 ]] || die 'Usage: sudo ./install.sh'
  [[ $EUID == 0 ]] || die 'Run sudo ./install.sh on the target board.'
  [[ -f /etc/debian_version && -d /run/systemd/system ]] || die 'Debian/Armbian with systemd is required.'
  systemctl is-active --quiet systemd-networkd || die 'systemd-networkd must already manage networking.'

  local package binary architecture file
  package=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
  case $(uname -m) in
    aarch64) architecture=arm64 ;;
    x86_64) architecture=amd64 ;;
    *) die 'Use a 64-bit ARM64 or amd64 system.' ;;
  esac
  binary="$package/djisimbox"
  [[ -x "$binary" ]] || binary="$package/bin/djisimbox-linux-$architecture"
  [[ -x "$binary" ]] || die 'Extract the matching Linux package first, or build the source binary.'
  "$binary" version

  install -D -m 0644 "$package/deploy/networkd/05-djisimbox-sms-only.network" \
    /etc/systemd/network/05-djisimbox-sms-only.network
  networkctl reload
  protect_interfaces
  apt-get update
  apt-get install --yes --no-install-recommends ca-certificates curl jq modemmanager polkitd
  systemctl enable --now ModemManager
  udevadm trigger --action=change --subsystem-match=tty --sysname-match='ttyUSB*'
  udevadm settle --timeout=30

  if ! getent passwd djisimbox >/dev/null; then
    useradd --system --user-group --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin djisimbox
  fi
  install -d -m 0750 -o root -g djisimbox /etc/djisimbox
  configure /etc/djisimbox
  chown djisimbox:djisimbox /etc/djisimbox/config.json /etc/djisimbox/secrets.json
  chmod 0600 /etc/djisimbox/config.json /etc/djisimbox/secrets.json

  install -m 0644 "$package/deploy/polkit/50-djisimbox.rules" /etc/polkit-1/rules.d/
  install -m 0644 "$package/deploy/systemd/djisimbox.service" /etc/systemd/system/
  install -m 0644 "$package/deploy/systemd/journald@djisimbox.conf" /etc/systemd/
  install -d -m 0755 /etc/systemd/system/systemd-journald@djisimbox.service.d
  install -m 0644 "$package/deploy/systemd/journald-djisimbox-volatile.conf" \
    /etc/systemd/system/systemd-journald@djisimbox.service.d/50-volatile.conf
  systemctl daemon-reload
  "$binary" check
  systemctl stop djisimbox
  install -m 0755 "$binary" /usr/local/bin/djisimbox
  runuser -u djisimbox -- /usr/local/bin/djisimbox check
  systemctl restart systemd-journald@djisimbox
  systemctl enable --now djisimbox
  systemctl is-active --quiet djisimbox
  printf '%s\n' 'Installed. Send a test SMS and confirm it reaches the intended Telegram chat.'
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
