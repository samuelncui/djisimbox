"""Installer boundaries using synthetic sysfs, ModemManager and Telegram data."""

import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().parents[1] / "install.sh"
TOKEN = "123456789:AAAAAAAAAAAAAAAAAAAA"
ICCID = "89000000000000000001"
OTHER_ICCID = "89000000000000000002"


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="djisimbox-install-test-")
        self.addCleanup(self.temporary.cleanup)
        self.fixture = Path(self.temporary.name)
        self.config = self.fixture / "config"
        self.config.mkdir()

    def bash(self, commands, input="", **env):
        return subprocess.run(
            ["bash", "-c", 'source "$1"\n' + commands, "bash", str(INSTALLER)],
            input=input, text=True, capture_output=True,
            env=dict(os.environ, FIXTURE=str(self.fixture), **env), timeout=10,
        )

    def device(self, name, vendor="2c7c", product="0125"):
        device = self.fixture / "devices" / name
        device.mkdir(parents=True)
        (device / "idVendor").write_text(vendor + "\n")
        (device / "idProduct").write_text(product + "\n")
        interface = self.fixture / "net" / name
        interface.mkdir(parents=True)
        child = device / "1-1:1.0" / "net"
        child.mkdir(parents=True)
        (interface / "device").symlink_to(child)
        return device

    def test_protects_only_matching_usb_modem(self):
        self.device("cell0")
        self.device("wifi0", vendor="1234")
        result = self.bash('''
ip() { printf 'ip %s\\n' "$*" >>"$FIXTURE/trace"; }
networkctl() {
  printf 'networkctl %s\\n' "$*" >>"$FIXTURE/trace"
  if [[ $1 == status ]]; then
    printf '%s\\n' 'Network File: /etc/systemd/network/05-djisimbox-sms-only.network'
  fi
}
protect_interfaces "$FIXTURE/net"
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.fixture / "trace").read_text().splitlines(), [
            "ip link set dev cell0 down", "ip address flush dev cell0",
            "networkctl reconfigure cell0", "networkctl status cell0 --no-pager",
        ])

    def test_overriding_rule_stops_and_leaves_modem_disabled(self):
        self.device("cell0")
        result = self.bash('''
ip() { printf 'ip %s\\n' "$*" >>"$FIXTURE/trace"; }
networkctl() { printf '%s\\n' 'Network File: /etc/systemd/network/00-other.network'; }
protect_interfaces "$FIXTURE/net"
touch "$FIXTURE/continued"
''')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("overrides modem protection", result.stderr)
        self.assertFalse((self.fixture / "continued").exists())
        self.assertEqual((self.fixture / "trace").read_text().splitlines()[-2:], [
            "ip link set dev cell0 down", "ip address flush dev cell0",
        ])

    def test_discovers_dji_sim_and_skips_other_devices_and_missing_sim(self):
        device = self.device("cell0")
        other = self.device("wifi0", vendor="1234")
        modem_paths = [f"/org/freedesktop/ModemManager1/Modem/{i}" for i in range(3)]
        (self.fixture / "modems.json").write_text(json.dumps({"modem-list": modem_paths}))
        for i, (path, sim) in enumerate([
            (device, "/org/freedesktop/ModemManager1/SIM/7"),
            (other, "/org/freedesktop/ModemManager1/SIM/8"),
            (device, "--"),
        ]):
            (self.fixture / f"modem-{i}.json").write_text(json.dumps({
                "modem": {"generic": {"device": str(path), "sim": sim}},
            }))
        (self.fixture / "sim.json").write_text(json.dumps({
            "sim": {"properties": {"iccid": ICCID}},
        }))
        result = self.bash('''
mmcli() {
  case $1 in
    --list-modems) cat "$FIXTURE/modems.json" ;;
    --modem) cat "$FIXTURE/modem-${2##*/}.json" ;;
    --sim) [[ $2 == /org/freedesktop/ModemManager1/SIM/7 ]]; cat "$FIXTURE/sim.json" ;;
    *) return 1 ;;
  esac
}
discover_sims
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), ICCID)

    def configure(self, input, curl_failure=False):
        commands = '''
protect_interfaces() { :; }
discover_sims() { printf '%s\\n' 89000000000000000001 89000000000000000002; }
jq() { printf 'jq %s\\n' "$*" >>"$FIXTURE/argv"; command jq "$@"; }
curl() {
  printf 'curl %s\\n' "$*" >>"$FIXTURE/argv"
  cat >"$FIXTURE/request"
'''
        commands += "  return 22\n" if curl_failure else '''
  printf '%s\\n' '{"ok":true,"result":[{"message":{"chat":{"id":123456789,"first_name":"Alice"}}},{"message":{"chat":{"id":234567890,"first_name":"Bob"}}}]}'
'''
        commands += '}\nconfigure "$FIXTURE/config"\n'
        return self.bash(commands, input)

    def test_first_setup_binds_only_selected_sim_and_explicit_recipient(self):
        name = 'Work "SIM" \\ backup'
        result = self.configure(f"\n2\n{name}\n{TOKEN}\n\n234567890\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.config / "config.json").read_text()), {
            "version": 1, "routes": [{"iccid": OTHER_ICCID, "name": name,
                "recipients": [{"bot": "default", "chat_id": "234567890"}]}],
        })
        self.assertEqual(json.loads((self.config / "secrets.json").read_text()), {
            "bots": {"default": TOKEN},
        })
        for name in ("config.json", "secrets.json"):
            self.assertEqual(stat.S_IMODE((self.config / name).stat().st_mode), 0o600)
        self.assertNotIn(TOKEN, result.stdout + result.stderr)
        self.assertNotIn(TOKEN, (self.fixture / "argv").read_text())
        self.assertNotIn(OTHER_ICCID, result.stdout)
        self.assertIn("/getUpdates", (self.fixture / "request").read_text())

    def test_webhook_conflict_allows_known_group_id(self):
        result = self.configure(f"\n1\n\n{TOKEN}\n\n-1001234567890\n", curl_failure=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        config = json.loads((self.config / "config.json").read_text())
        self.assertEqual(config["routes"][0]["recipients"], [
            {"bot": "default", "chat_id": "-1001234567890"},
        ])
        self.assertNotIn("deleteWebhook", (self.fixture / "request").read_text())

    def test_rerun_preserves_config_without_querying_telegram_or_modem(self):
        config = '{"version":1,"routes":[]}\n'
        secrets = json.dumps({"bots": {"default": TOKEN}})
        (self.config / "config.json").write_text(config)
        (self.config / "secrets.json").write_text(secrets)
        result = self.bash('''
discover_sims() { exit 90; }
curl() { exit 91; }
configure "$FIXTURE/config"
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.config / "config.json").read_text(), config)
        self.assertEqual((self.config / "secrets.json").read_text(), secrets)

    def test_partial_configuration_is_preserved(self):
        (self.config / "config.json").write_text("existing configuration")
        result = self.bash('configure "$FIXTURE/config"\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.config / "config.json").read_text(), "existing configuration")
        self.assertFalse((self.config / "secrets.json").exists())

    def test_invalid_token_never_reaches_telegram_or_creates_config(self):
        result = self.configure('\n1\n\nnot-a-token\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Invalid bot token format", result.stderr)
        self.assertFalse((self.fixture / "request").exists())
        self.assertFalse((self.config / "config.json").exists())

    def test_invalid_setup_input_does_not_leave_configuration_behind(self):
        for name, chat in [("SIM\x01", "123456789"), ("\u2000", "123456789"), ("名" * 81, "123456789"),
                           ("SIM\u2028name", "123456789"), ("SIM\u2029name", "123456789"),
                           ("SIM 1", "9223372036854775808"), ("SIM 1", "-9223372036854775809")]:
            with self.subTest(name=name, chat=chat):
                for path in self.config.iterdir():
                    path.unlink()
                result = self.configure(f"\n1\n{name}\n{TOKEN}\n\n{chat}\n")
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.config / "config.json").exists())
                self.assertFalse((self.config / "secrets.json").exists())

    def test_chat_id_boundaries_are_validated_without_numeric_rounding(self):
        for value, expected in [("1", 0), ("-1", 0), ("9223372036854775807", 0),
                                ("-9223372036854775808", 0), ("0", 1), ("01", 1),
                                ("9223372036854775808", 1), ("-9223372036854775809", 1)]:
            with self.subTest(value=value):
                result = self.bash(f'valid_chat_id {value}\n')
                self.assertEqual(result.returncode, expected)

    def test_eighty_character_unicode_name_is_accepted(self):
        name = "名" * 80
        result = self.configure(f"\n1\n{name}\n{TOKEN}\n\n9223372036854775807\n")
        self.assertEqual(result.returncode, 0, result.stderr)
        route = json.loads((self.config / "config.json").read_text())["routes"][0]
        self.assertEqual(route["name"], name)
        self.assertEqual(route["recipients"][0]["chat_id"], "9223372036854775807")

    def test_help_has_no_installation_side_effects(self):
        result = subprocess.run([str(INSTALLER), "--help"], text=True, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("sudo ./install.sh", result.stdout)


if __name__ == "__main__":
    unittest.main()
