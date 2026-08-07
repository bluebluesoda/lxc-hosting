package lx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Client struct{}

func (c *Client) Run(args ...string) (string, error) {
	out, err := exec.Command("lxc", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("lxc %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ExecSH runs a shell script inside a container as root.
func (c *Client) ExecSH(name, script string) (string, error) {
	out, err := exec.Command("lxc", "exec", name, "--", "/bin/sh", "-c", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("lxc exec %s: %s", name, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

type info struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	State  *struct {
		CPU *struct {
			Usage int64 `json:"usage"`
		} `json:"cpu"`
		Memory *struct {
			Usage int64 `json:"usage"`
			Total int64 `json:"total"`
		} `json:"memory"`
		Network map[string]struct {
			Addresses []struct {
				Family  string `json:"family"`
				Address string `json:"address"`
			} `json:"addresses"`
		} `json:"network"`
	} `json:"state"`
}

// Usage describes a container's live CPU/memory accounting.
type Usage struct {
	CPUUsage int64 // nanoseconds of CPU time used since start
	MemUsage int64 // bytes currently used
	MemTotal int64 // memory limit in bytes
}

// UsageMap returns current CPU/memory accounting for every container.
func (c *Client) UsageMap() (map[string]Usage, error) {
	out, err := c.Run("list", "--format=json")
	if err != nil {
		return nil, err
	}
	var items []info
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return nil, err
	}
	m := make(map[string]Usage)
	for _, it := range items {
		if it.State == nil || it.Status != "Running" {
			continue
		}
		u := Usage{}
		if it.State.CPU != nil {
			u.CPUUsage = it.State.CPU.Usage
		}
		if it.State.Memory != nil {
			u.MemUsage = it.State.Memory.Usage
			u.MemTotal = it.State.Memory.Total
		}
		m[it.Name] = u
	}
	return m, nil
}

func (c *Client) list() ([]info, error) {
	out, err := c.Run("list", "--format=json")
	if err != nil {
		return nil, err
	}
	var items []info
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (c *Client) State(name string) (string, error) {
	items, err := c.list()
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Name == name {
			return it.Status, nil
		}
	}
	return "", errors.New("instance not found: " + name)
}

func (c *Client) IPv4(name string) (string, error) {
	items, err := c.list()
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Name == name && it.State != nil {
			for _, ifs := range it.State.Network {
				for _, a := range ifs.Addresses {
					if a.Family == "inet" && a.Address != "127.0.0.1" {
						return a.Address, nil
					}
				}
			}
		}
	}
	return "", errors.New("no ipv4 for " + name)
}

func (c *Client) Start(name string) error { _, err := c.Run("start", name); return err }
func (c *Client) Stop(name string) error  { _, err := c.Run("stop", name); return err }

func (c *Client) SetCPU(name string, n int) error {
	_, err := c.Run("config", "set", name, "limits.cpu="+strconv.Itoa(n))
	return err
}

func (c *Client) SetMem(name string, mb int) error {
	_, err := c.Run("config", "set", name, "limits.memory="+strconv.Itoa(mb)+"MiB")
	return err
}

func (c *Client) SetDisk(name string, gb int) error {
	_, err := c.Run("config", "device", "set", name, "root", "size="+strconv.Itoa(gb)+"GiB")
	return err
}
func (c *Client) Restart(name string) error {
	if _, err := c.Run("restart", name); err != nil {
		return err
	}
	return c.WaitReady(name, 90*time.Second)
}
func (c *Client) Delete(name string) error { _, err := c.Run("delete", "--force", name); return err }

func (c *Client) ImageExists(alias string) (bool, error) {
	if _, err := c.Run("image", "show", alias); err != nil {
		return false, nil
	}
	return true, nil
}

// Launch creates a container with limits, static IP and autostart enabled,
// then starts it and waits until it is ready. security.nesting allows running
// Docker / nested containers inside the instance.
func (c *Client) Launch(name, image, ip string, cpu, memMB, diskGB int) error {
	args := []string{"init", image, name,
		"-c", "limits.cpu=" + strconv.Itoa(cpu),
		"-c", "limits.memory=" + strconv.Itoa(memMB) + "MiB",
		"-c", "boot.autostart=true",
		"-c", "security.nesting=true",
	}
	if _, err := c.Run(args...); err != nil {
		return err
	}
	// Static IP on the eth0 device inherited from the default profile.
	if _, err := c.Run("config", "device", "override", name, "eth0", "ipv4.address="+ip); err != nil {
		return fmt.Errorf("override eth0 ip: %w", err)
	}
	if _, err := c.Run("config", "device", "override", name, "root", "size="+strconv.Itoa(diskGB)+"GiB"); err != nil {
		return fmt.Errorf("override root size: %w", err)
	}
	if err := c.Start(name); err != nil {
		return err
	}
	return c.WaitReady(name, 120*time.Second)
}

// WaitReady waits until the container is running and accepts exec.
func (c *Client) WaitReady(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := c.State(name); err == nil && st == "Running" {
			if _, err := c.ExecSH(name, "true"); err == nil {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("container %s not ready within %v", name, timeout)
}
