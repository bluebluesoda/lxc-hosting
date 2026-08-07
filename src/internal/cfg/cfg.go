package cfg

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultDataDir     = "/etc/vpsmgr"
	DefaultNftDir      = "/etc/vpsmgr/nftables.d"
	DefaultNftMain     = "/etc/vpsmgr/nftables.conf"
	DefaultTraefikDir  = "/etc/traefik/dynamic"
	DefaultDB          = "/etc/vpsmgr/vpsmgr.db"
	DefaultListen      = ":8443"
	DefaultSubnet      = "10.42.0.0/24"
	DefaultGateway     = "10.42.0.1"
	DefaultPortBase    = 10000
	DefaultPortsPerUser = 50
	DefaultBridge      = "lxdbr0"
	DefaultPool        = "vpsmgr"
	DefaultImage       = "vpsmgr/debian-sshd"
	DefaultImageFB     = "images:debian/13"
)

type Config struct {
	Panel PanelCfg `yaml:"panel"`
	Net   NetCfg   `yaml:"net"`
	LXD   LXDCfg   `yaml:"lxd"`
}

type PanelCfg struct {
	Listen      string `yaml:"listen"`
	Cert        string `yaml:"cert"`
	Key         string `yaml:"key"`
	DB          string `yaml:"db"`
	PublicIP    string `yaml:"public_ip"`
	SessionDays int    `yaml:"session_days"`
}

type NetCfg struct {
	Subnet       string `yaml:"subnet"`
	Gateway      string `yaml:"gateway"`
	PortBase     int    `yaml:"port_base"`
	PortsPerUser int    `yaml:"ports_per_user"`
	ExtIF        string `yaml:"ext_if"`
}

type LXDCfg struct {
	Image         string `yaml:"image"`
	ImageFallback string `yaml:"image_fallback"`
	Pool          string `yaml:"pool"`
	Bridge        string `yaml:"bridge"`
}

func Default() *Config {
	c := &Config{}
	c.Panel = PanelCfg{Listen: DefaultListen, Cert: DefaultDataDir + "/panel.crt", Key: DefaultDataDir + "/panel.key", DB: DefaultDB, SessionDays: 3}
	c.Net = NetCfg{Subnet: DefaultSubnet, Gateway: DefaultGateway, PortBase: DefaultPortBase, PortsPerUser: DefaultPortsPerUser}
	c.LXD = LXDCfg{Image: DefaultImage, ImageFallback: DefaultImageFB, Pool: DefaultPool, Bridge: DefaultBridge}
	return c
}

func Path() string {
	if p := os.Getenv("VPSMGR_CONFIG"); p != "" {
		return p
	}
	return DefaultDataDir + "/config.yaml"
}

func (c *Config) DataDir() string { return DefaultDataDir }
func (c *Config) NftDir() string  { return DefaultNftDir }
func (c *Config) NftMain() string { return DefaultNftMain }
func (c *Config) TraefikDir() string { return DefaultTraefikDir }

// SubnetIP returns the IP portion of the subnet CIDR.
func (c *Config) SubnetIP() string {
	ip, _, err := net.ParseCIDR(c.Net.Subnet)
	if err != nil {
		return ""
	}
	return ip.String()
}

func Load() (*Config, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if err := c.FillAuto(); err != nil {
		return nil, err
	}
	return c, nil
}

func Save(c *Config) error {
	if err := os.MkdirAll(DefaultDataDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(Path(), MustYAML(c), 0o600)
}

func MustYAML(c *Config) []byte {
	b, err := yaml.Marshal(c)
	if err != nil {
		panic(err)
	}
	return b
}

func (c *Config) FillAuto() error {
	if c.Net.ExtIF == "" {
		c.Net.ExtIF = DetectExtIF()
	}
	if c.Panel.PublicIP == "" {
		c.Panel.PublicIP = DetectPublicIP(c.Net.ExtIF)
	}
	return nil
}

func shCmd(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DetectExtIF returns the name of the interface used for the default route.
func DetectExtIF() string {
	if s := shCmd("sh", "-c", "ip route show default | awk '{print $5; exit}'"); s != "" {
		return s
	}
	if s := shCmd("sh", "-c", "ip -o link show up | grep -v -E 'lo|lxdbr|virbr|docker' | awk -F': ' '{print $2; exit}'"); s != "" {
		return s
	}
	return "eth0"
}

// DetectPublicIP returns the public IP, falling back to the machine's own
// address on the external interface (may be a private IP).
func DetectPublicIP(extIF string) string {
	if extIF != "" {
		ip, err := firstIPv4(extIF)
		if err == nil && ip != "" {
			return ip
		}
	}
	if s := shCmd("sh", "-c", "hostname -I | awk '{print $1}'"); s != "" {
		return s
	}
	return "127.0.0.1"
}

func firstIPv4(iface string) (string, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return "", err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no ipv4 on %s", iface)
}
