package cfg

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"vpsmgr/internal/pw"
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
	// URLPath is the immutable secret prefix protecting the whole panel
	// (e.g. /Ab1_cdE-9x). Generated once on first install.
	URLPath string `yaml:"url_path"`
}

type NetCfg struct {
	Subnet       string `yaml:"subnet"`
	Gateway      string `yaml:"gateway"`
	PortBase     int    `yaml:"port_base"`
	PortsPerUser int    `yaml:"ports_per_user"`
	ExtIF        string `yaml:"ext_if"`
	// IPv6Subnet is the global prefix handed out to containers (e.g.
	// "2602:fada:6::/64", or a /80 slice the provider assigned the host).
	// Empty means IPv6 pass-through is disabled.
	// Containers get global addresses via SLAAC on lxdbr0; the host proxies
	// their neighbor discovery. No NAT, no DB schema change: a container's
	// IPv6 is whatever LXD/SLAAC assigned, read live from `lxc list`.
	IPv6Subnet string `yaml:"ipv6_subnet,omitempty"`
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
	if c.Panel.URLPath == "" {
		c.Panel.URLPath = pw.URLSafe(10)
	}
	// VPSMGR_IPV6_SUBNET lets the installer inject the /64 prefix at first
	// install (it overrides whatever is in the config file).
	if v := os.Getenv("VPSMGR_IPV6_SUBNET"); v != "" {
		c.Net.IPv6Subnet = v
	}
	return nil
}

// IPv6Enabled reports whether IPv6 pass-through is configured.
func (c *Config) IPv6Enabled() bool { return c.Net.IPv6Subnet != "" }

// IPv6Network parses and validates the configured IPv6 prefix. It must be a
// global (non-ULA, non-link-local) CIDR — /64 or shorter (e.g. /56), or longer
// up to /80 when the provider hands the host a /80 slice. The prefix length is
// REQUIRED; a bare address is rejected rather than silently assumed to be /64
// (a /80 slice would then get addresses outside the routed prefix).
func (c *Config) IPv6Network() (*net.IPNet, error) {
	s := c.Net.IPv6Subnet
	if s == "" {
		return nil, fmt.Errorf("ipv6_subnet not configured")
	}
	if !strings.Contains(s, "/") {
		return nil, fmt.Errorf("invalid ipv6_subnet %q: prefix length required (e.g. /64 or /80)", s)
	}
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil, fmt.Errorf("invalid ipv6_subnet %q: %w", s, err)
	}
	if n.IP.To4() != nil {
		return nil, fmt.Errorf("invalid ipv6_subnet %q: not an IPv6 prefix", s)
	}
	if n.IP.IsPrivate() || n.IP.IsLinkLocalUnicast() || n.IP.IsLoopback() || n.IP.IsUnspecified() {
		return nil, fmt.Errorf("invalid ipv6_subnet %q: must be a global (public) prefix", s)
	}
	ones, _ := n.Mask.Size()
	// The deterministic per-container address uses the low 48 host bits (a
	// 32-bit username hash + a fixed 0001 last block), so the prefix needs at
	// least that many host bits: any prefix /80 or shorter works.
	if ones > 80 {
		return nil, fmt.Errorf("invalid ipv6_subnet %q: prefix must be /80 or shorter (got /%d)", s, ones)
	}
	return n, nil
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

// DetectPublicIP returns the publicly reachable IPv4. When the provider puts a
// global address directly on the NIC that wins. On clouds that NAT (AWS EC2,
// Aliyun ECS, ...) the NIC carries only a private address and the public IP
// lives at the edge — ask the cloud metadata service, then a generic echo
// service. The machine's own address is the last resort.
func DetectPublicIP(extIF string) string {
	if extIF != "" {
		if ip, err := firstGlobalIPv4(extIF); err == nil && ip != "" {
			return ip
		}
	}
	if s := cloudPublicIP(); s != "" {
		return s
	}
	if extIF != "" {
		if ip, err := firstIPv4(extIF); err == nil && ip != "" {
			return ip
		}
	}
	if s := shCmd("sh", "-c", "hostname -I | awk '{print $1}'"); s != "" {
		return s
	}
	return "127.0.0.1"
}

// cloudPublicIP asks the metadata services of the common NAT-ing clouds (AWS
// EC2, Aliyun ECS) and, as a last resort, a generic public echo service for
// the host's public IPv4. Returns "" when none answers.
func cloudPublicIP() string {
	// AWS EC2: IMDSv2 first (PUT a token, then GET with it), then IMDSv1.
	tok := httpGet("http://169.254.169.254/latest/api/token", "PUT",
		map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "21600"}, 1)
	if tok != "" {
		if s := httpGet("http://169.254.169.254/latest/meta-data/public-ipv4", "GET",
			map[string]string{"X-aws-ec2-metadata-token": tok}, 1); s != "" {
			return s
		}
	}
	if s := httpGet("http://169.254.169.254/latest/meta-data/public-ipv4", "GET", nil, 1); s != "" {
		return s
	}
	// Aliyun ECS.
	for _, k := range []string{"eipv4", "public-ipv4"} {
		if s := httpGet("http://100.100.100.200/latest/meta-data/"+k, "GET", nil, 1); s != "" {
			return s
		}
	}
	// Generic fallback: a public echo service.
	return httpGet("https://api.ipify.org", "GET", nil, 3)
}

// httpGet issues a request and returns the trimmed body (up to 64 bytes), or
// "" on any failure / non-200.
func httpGet(url, method string, headers map[string]string, timeoutSec int) string {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return ""
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func firstGlobalIPv4(iface string) (string, error) {
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
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String(), nil
	}
	return "", fmt.Errorf("no global ipv4 on %s", iface)
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
