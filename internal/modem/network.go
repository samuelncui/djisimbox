package modem

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// ErrNetworkActive means a cellular host interface lacks the required protection.
var ErrNetworkActive = errors.New("cellular host interface must be down without addresses")

// CheckNetwork checks the verified DJI USB identity, including ECM ports absent from ModemManager.
// Provisioning owns persistent interface protection; this check never changes host networking.
func CheckNetwork() error {
	// Match hardware ancestry so USB ports and generated interface names may change.
	interfaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("enumerate host interfaces: %w", err)
	}
	for _, iface := range interfaces {
		device, err := filepath.EvalSymlinks(filepath.Join("/sys/class/net", iface.Name, "device"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect network device: %w", err)
		}
		for device != "/" && device != "." {
			vendor, err := os.ReadFile(filepath.Join(device, "idVendor"))
			if err == nil {
				product, err := os.ReadFile(filepath.Join(device, "idProduct"))
				if err != nil {
					return fmt.Errorf("inspect USB product: %w", err)
				}
				if strings.TrimSpace(string(vendor)) == "2c7c" && strings.TrimSpace(string(product)) == "0125" {
					if err := checkInterface(&iface); err != nil {
						return err
					}
				}
				break
			}
			device = filepath.Dir(device)
		}
	}
	return nil
}

func checkInterface(iface *net.Interface) error {
	// Reject either an enabled port or lingering IPv4/IPv6 addresses.
	if iface.Flags&net.FlagUp != 0 {
		return fmt.Errorf("%w: %s", ErrNetworkActive, iface.Name)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return fmt.Errorf("read cellular interface addresses: %w", err)
	}
	if len(addresses) > 0 {
		return fmt.Errorf("%w: %s", ErrNetworkActive, iface.Name)
	}
	return nil
}
