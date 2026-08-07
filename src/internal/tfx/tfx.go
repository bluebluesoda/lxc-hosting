package tfx

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"vpsmgr/internal/cfg"
)

type Traefik struct {
	dir string
}

func New(c *cfg.Config) *Traefik { return &Traefik{dir: c.TraefikDir()} }

var nameRe = regexp.MustCompile(`[^a-zA-Z0-9-]+`)

func sanitize(s string) string { return nameRe.ReplaceAllString(s, "-") }

func (t *Traefik) filePath(name string) string {
	return filepath.Join(t.dir, "user-"+name+".yaml")
}

// WriteUser regenerates the full dynamic config for a user from its domains.
func (t *Traefik) WriteUser(name, ip string, domains []string) error {
	var b strings.Builder
	b.WriteString("http:\n  routers:\n")
	svc := "s-" + name
	tcpSvc := "t-" + name
	for _, d := range domains {
		rtr := "u-" + sanitize(name) + "-" + sanitize(d)
		fmt.Fprintf(&b, "    %s:\n      rule: \"Host(`%s`)\"\n      entryPoints: [web]\n      service: %s\n", rtr, d, svc)
	}
	b.WriteString("  services:\n")
	fmt.Fprintf(&b, "    %s:\n      loadBalancer:\n        servers: [{ url: \"http://%s:80\" }]\n", svc, ip)
	b.WriteString("tcp:\n  routers:\n")
	for _, d := range domains {
		rtr := "t-" + sanitize(name) + "-" + sanitize(d)
		fmt.Fprintf(&b, "    %s:\n      rule: \"HostSNI(`%s`)\"\n      entryPoints: [websecure]\n      tls: { passthrough: true }\n      service: %s\n", rtr, d, tcpSvc)
	}
	b.WriteString("  services:\n")
	fmt.Fprintf(&b, "    %s:\n      loadBalancer:\n        servers: [{ address: \"%s:443\" }]\n", tcpSvc, ip)

	tmp := t.filePath(name) + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.filePath(name))
}

func (t *Traefik) RemoveUser(name string) error {
	err := os.Remove(t.filePath(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
