package internal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
)

// Host identifies the machine a query ran on.
type Host struct {
	ID                string             `json:"id"`
	Hostname          string             `json:"hostname"`
	NetworkInterfaces []NetworkInterface `json:"network_interfaces"`
	Platform          string             `json:"platform"`
}

type NetworkInterface struct {
	Name        string   `json:"name"`
	IPAddresses []string `json:"ip_addresses"`
	MACAddress  string   `json:"mac_address"`
}

// Platform is this agent's platform as used in query requests: "linux", "darwin", "windows", ...
func Platform() string {
	return runtime.GOOS
}

// HostID returns the hardware UUID reported by osquery's system_info table.
func HostID(ctx context.Context) (string, error) {
	rows, err := ExecuteQuery[struct {
		UUID string `json:"uuid"`
	}](ctx, "SELECT uuid FROM system_info;")
	if err != nil {
		return "", fmt.Errorf("get host id: %w", err)
	}
	if len(rows) == 0 || rows[0].UUID == "" {
		return "", errors.New("get host id: system_info returned no uuid")
	}
	return rows[0].UUID, nil
}

// CurrentHost returns host details. The hostname and network interfaces are read on every
// call so that results reflect address changes (DHCP, VPNs, ...).
func CurrentHost(id string) Host {
	hostname, _ := os.Hostname()
	return Host{
		ID:                id,
		Hostname:          hostname,
		NetworkInterfaces: networkInterfaces(),
		Platform:          Platform(),
	}
}

func networkInterfaces() []NetworkInterface {
	out := []NetworkInterface{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		ni := NetworkInterface{
			Name:        iface.Name,
			IPAddresses: []string{},
			MACAddress:  iface.HardwareAddr.String(),
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			switch a := addr.(type) {
			case *net.IPNet:
				ni.IPAddresses = append(ni.IPAddresses, a.IP.String())
			case *net.IPAddr:
				ni.IPAddresses = append(ni.IPAddresses, a.IP.String())
			}
		}
		out = append(out, ni)
	}
	return out
}

// PrimaryAddress returns the IP address and MAC address of the network interface used for
// the default route. It finds the route by "connecting" a UDP socket to a documentation-only
// address (RFC 5737 / RFC 3849), which sends no packets. If there is no default route it
// falls back to the first up, non-loopback interface with an IP address.
func PrimaryAddress() (ip, mac string, err error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", "", fmt.Errorf("list network interfaces: %w", err)
	}

	for _, probe := range []string{"192.0.2.1:9", "[2001:db8::1]:9"} {
		conn, err := net.Dial("udp", probe)
		if err != nil {
			continue
		}
		local := conn.LocalAddr().(*net.UDPAddr).IP
		conn.Close()
		for _, iface := range ifaces {
			addrs, _ := iface.Addrs()
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.Equal(local) {
					return local.String(), iface.HardwareAddr.String(), nil
				}
			}
		}
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLinkLocalUnicast() {
				return ipnet.IP.String(), iface.HardwareAddr.String(), nil
			}
		}
	}
	return "", "", errors.New("no network interface with an IP address")
}
