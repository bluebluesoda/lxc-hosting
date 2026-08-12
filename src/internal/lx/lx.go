package lx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client talks to the local LXD daemon over its Unix socket using the REST
// API. All mutations go through async operations that are waited on, so the
// caller sees the same blocking semantics the `lxc` CLI used to provide, but
// without paying for a process spawn (and a snap bootstrap) on every call.
type Client struct {
	base string
	http *http.Client
}

// New creates a client for the LXD Unix socket at path. The connection is
// lazy: nothing dials the socket until the first request.
func New(socket string) *Client {
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	// Per-request cap guards against a hung daemon. The operation wait uses
	// timeout=30 server-side, so 60s covers it with margin.
	return &Client{base: "http://unix", http: &http.Client{Transport: t, Timeout: 60 * time.Second}}
}

// ---- LXD REST API primitives ----

type response struct {
	Type       string          `json:"type"`
	StatusCode int             `json:"status_code"`
	Error      string          `json:"error"`
	Operation  string          `json:"operation"`
	Metadata   json.RawMessage `json:"metadata"`
}

// do sends a request and returns the LXD response envelope. The envelope's
// "error" type is turned into a Go error.
func (c *Client) do(method, path string, body any) (*response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("lxd %s %s: bad response: %w", method, path, err)
	}
	if r.Type == "error" {
		return nil, fmt.Errorf("lxd %s %s: %s", method, path, r.Error)
	}
	return &r, nil
}

// get performs a sync GET and unmarshals the metadata into out.
func (c *Client) get(path string, out any) error {
	r, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(r.Metadata, out); err != nil {
			return err
		}
	}
	return nil
}

// patch sends a sync PATCH with the given body (unmarshaled into out).
func (c *Client) patch(path string, body, out any) error {
	r, err := c.do(http.MethodPatch, path, body)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(r.Metadata, out); err != nil {
			return err
		}
	}
	return nil
}

// sendOp triggers an async operation (POST/PUT/DELETE) and waits for it.
// path is the API path; the operation location comes back in the response.
func (c *Client) sendOp(method, path string, body any, timeout time.Duration) error {
	r, err := c.do(method, path, body)
	if err != nil {
		return err
	}
	return c.wait(r.Operation, timeout)
}

// wait blocks until the async operation at opPath (/1.0/operations/...) has
// finished, returning its error.
func (c *Client) wait(opPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var op struct {
			Status string `json:"status"`
			Err    string `json:"err"`
		}
		if err := c.get(opPath+"/wait?timeout=30", &op); err != nil {
			return err
		}
		if op.Status != "Running" {
			if op.Err != "" {
				return errors.New(op.Err)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lxd operation timed out after %v", timeout)
		}
	}
}

// ---- API payload shapes (subset of api.*, matching 5.21 LTS) ----

type instance struct {
	Name    string            `json:"name"`
	Status  string            `json:"status"`
	Config  map[string]string `json:"config"`
	Devices map[string]device `json:"devices"`
}

type device map[string]string

type instState struct {
	Status string `json:"status"`
	CPU    *struct {
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
			Scope   string `json:"scope"`
		} `json:"addresses"`
		Counters struct {
			BytesReceived int64 `json:"bytes_received"`
			BytesSent     int64 `json:"bytes_sent"`
		} `json:"counters"`
	} `json:"network"`
}

type createReq struct {
	Name     string            `json:"name"`
	Source   map[string]string `json:"source"`
	Config   map[string]string `json:"config"`
	Devices  map[string]device `json:"devices"`
	Profiles []string          `json:"profiles"`
}

type stateAction struct {
	Action  string `json:"action"`
	Force   bool   `json:"force,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// ---- high-level helpers ----

func (c *Client) list() ([]instance, error) {
	var insts []instance
	if err := c.get("/1.0/instances?recursion=1", &insts); err != nil {
		return nil, err
	}
	return insts, nil
}

// stateOf returns the live state of one instance.
func (c *Client) stateOf(name string) (*instState, error) {
	var st instState
	if err := c.get("/1.0/instances/"+url.PathEscape(name)+"/state", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// containerInfo maps a live state into the ContainerInfo shape.
func containerInfo(name string, st *instState) ContainerInfo {
	ci := ContainerInfo{Status: st.Status}
	if st.CPU != nil {
		ci.CPUUsage = st.CPU.Usage
	}
	if st.Memory != nil {
		ci.MemUsage = st.Memory.Usage
		ci.MemTotal = st.Memory.Total
	}
	for _, ifs := range st.Network {
		ci.Rx += ifs.Counters.BytesReceived
		ci.Tx += ifs.Counters.BytesSent
		for _, a := range ifs.Addresses {
			if ci.IPv4 == "" && a.Family == "inet" && a.Address != "127.0.0.1" {
				ci.IPv4 = a.Address
			}
		}
	}
	return ci
}

// snapshot returns the status plus live state of every instance using ONE list
// call followed by concurrent per-instance state calls for the running ones.
func (c *Client) snapshot() (map[string]ContainerInfo, error) {
	insts, err := c.list()
	if err != nil {
		return nil, err
	}
	out := make(map[string]ContainerInfo, len(insts))
	type res struct {
		name string
		ci   ContainerInfo
	}
	ch := make(chan res, len(insts))
	running := 0
	for _, it := range insts {
		out[it.Name] = ContainerInfo{Status: it.Status}
		if it.Status != "Running" {
			continue
		}
		running++
		go func(n string) {
			st, err := c.stateOf(n)
			if err != nil {
				ch <- res{} // keep the Running status we already recorded
				return
			}
			ch <- res{n, containerInfo(n, st)}
		}(it.Name)
	}
	for i := 0; i < running; i++ {
		if r := <-ch; r.name != "" {
			out[r.name] = r.ci
		}
	}
	return out, nil
}

// ---- data types exposed to mgr ----

// Usage describes a container's live CPU/memory accounting.
type Usage struct {
	CPUUsage int64 // nanoseconds of CPU time used since start
	MemUsage int64 // bytes currently used
	MemTotal int64 // memory limit in bytes
}

// UsageMap returns current CPU/memory accounting for every container.
func (c *Client) UsageMap() (map[string]Usage, error) {
	sn, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	m := make(map[string]Usage, len(sn))
	for name, ci := range sn {
		if ci.Status != "Running" {
			continue
		}
		m[name] = Usage{CPUUsage: ci.CPUUsage, MemUsage: ci.MemUsage, MemTotal: ci.MemTotal}
	}
	return m, nil
}

// Traffic describes a container's cumulative network counters since its last
// start. Counters are per-device and reset to zero when the container is
// restarted or reinstalled.
type Traffic struct {
	Rx int64 // bytes received (download)
	Tx int64 // bytes sent (upload)
}

// TrafficMap returns the cumulative network counters of every running
// container, keyed by container name. Stopped containers have no state and are
// omitted.
func (c *Client) TrafficMap() (map[string]Traffic, error) {
	sn, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	m := make(map[string]Traffic, len(sn))
	for name, ci := range sn {
		if ci.Status != "Running" {
			continue
		}
		m[name] = Traffic{Rx: ci.Rx, Tx: ci.Tx}
	}
	return m, nil
}

// ContainerInfo is one snapshot of a container taken from a single state read.
type ContainerInfo struct {
	Status   string
	CPUUsage int64 // nanoseconds of CPU time since start (0 if not running)
	MemUsage int64 // bytes currently used (0 if not running)
	MemTotal int64 // memory limit in bytes (0 if not running)
	Rx       int64 // cumulative bytes received (download) since start
	Tx       int64 // cumulative bytes sent (upload) since start
	IPv4     string
}

// Containers returns a live snapshot of every container: one list call plus
// concurrent state reads, regardless of the number of containers. Stopped
// containers yield zeroed CPU/mem/traffic values with Status reflecting the
// real status.
func (c *Client) Containers() (map[string]ContainerInfo, error) {
	return c.snapshot()
}

// State returns the status of one container.
func (c *Client) State(name string) (string, error) {
	var it instance
	if err := c.get("/1.0/instances/"+url.PathEscape(name), &it); err != nil {
		return "", err
	}
	return it.Status, nil
}

// ---- mutations ----

// NetworkSet sets one key=value config option on a managed LXD network
// (e.g. lxdbr0). Used for IPv6 pass-through bridge configuration.
func (c *Client) NetworkSet(network, kv string) error {
	key, val, ok := strings.Cut(kv, "=")
	if !ok {
		return fmt.Errorf("lxd network set: invalid key=value %q", kv)
	}
	body := map[string]map[string]string{"config": {key: val}}
	return c.patch("/1.0/networks/"+url.PathEscape(network), body, nil)
}

func (c *Client) Start(name string) error {
	return c.sendOp(http.MethodPut, "/1.0/instances/"+url.PathEscape(name)+"/state",
		stateAction{Action: "start"}, 2*time.Minute)
}

func (c *Client) Stop(name string) error {
	return c.sendOp(http.MethodPut, "/1.0/instances/"+url.PathEscape(name)+"/state",
		stateAction{Action: "stop"}, 2*time.Minute)
}

func (c *Client) Restart(name string) error {
	if err := c.sendOp(http.MethodPut, "/1.0/instances/"+url.PathEscape(name)+"/state",
		stateAction{Action: "restart"}, 2*time.Minute); err != nil {
		return err
	}
	return c.WaitReady(name, 90*time.Second)
}

// cpuLimitConfig maps a CPU quota in tenths of a core onto LXD config keys.
// Whole cores set `limits.cpu=<n>`. Fractional quotas (0.1..0.9) pin the
// container to a single core and add a time allowance
// (`limits.cpu.allowance=<n>ms/100ms`) so it may only use that slice of the
// core. Setting the allowance to "" removes it, which is how a fractional
// quota is switched back to whole cores (PATCH merges and deletes empty keys).
func cpuLimitConfig(cpuTenths int) map[string]string {
	if cpuTenths%10 != 0 {
		return map[string]string{
			"limits.cpu":           "1",
			"limits.cpu.allowance": strconv.Itoa(cpuTenths*10) + "ms/100ms",
		}
	}
	return map[string]string{
		"limits.cpu":           strconv.Itoa(cpuTenths / 10),
		"limits.cpu.allowance": "",
	}
}

// SetCPU live-updates the CPU quota (tenths of a core). Whole cores set
// `limits.cpu`; fractional quotas pin to one core with a time allowance.
func (c *Client) SetCPU(name string, cpuTenths int) error {
	return c.patch("/1.0/instances/"+url.PathEscape(name),
		map[string]map[string]string{"config": cpuLimitConfig(cpuTenths)}, nil)
}

// SetMem live-updates the memory limit.
func (c *Client) SetMem(name string, mb int) error {
	body := map[string]map[string]string{"config": {"limits.memory": strconv.Itoa(mb) + "MiB"}}
	return c.patch("/1.0/instances/"+url.PathEscape(name), body, nil)
}

// SetDisk grows the root device's size. The device map is fetched first and
// patched as a whole because a PATCH replaces the entire devices map.
func (c *Client) SetDisk(name string, gb int) error {
	var it instance
	if err := c.get("/1.0/instances/"+url.PathEscape(name)+"?recursion=1", &it); err != nil {
		return err
	}
	devices := it.Devices
	root, ok := devices["root"]
	if !ok {
		return fmt.Errorf("lxd: instance %s has no root device", name)
	}
	root["size"] = strconv.Itoa(gb) + "GiB"
	devices["root"] = root
	body := map[string]map[string]device{"devices": devices}
	return c.patch("/1.0/instances/"+url.PathEscape(name), body, nil)
}

// Delete force-stops the container if needed and removes it.
func (c *Client) Delete(name string) error {
	st, err := c.stateOf(name)
	if err == nil && st.Status != "Stopped" {
		_ = c.sendOp(http.MethodPut, "/1.0/instances/"+url.PathEscape(name)+"/state",
			stateAction{Action: "stop", Force: true, Timeout: -1}, 2*time.Minute)
	}
	return c.sendOp(http.MethodDelete, "/1.0/instances/"+url.PathEscape(name), nil, 2*time.Minute)
}

// ImageExists reports whether an image alias is present.
func (c *Client) ImageExists(alias string) (bool, error) {
	_, err := c.do(http.MethodGet, "/1.0/images/aliases/"+url.PathEscape(alias), nil)
	if err != nil {
		return false, nil
	}
	return true, nil
}

// PoolResources returns the storage pool's total/used bytes from the LXD API.
func (c *Client) PoolResources(pool string) (total, used int64, err error) {
	var res struct {
		Space struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
		} `json:"space"`
	}
	if err := c.get("/1.0/storage-pools/"+url.PathEscape(pool)+"/resources", &res); err != nil {
		return 0, 0, err
	}
	return res.Space.Total, res.Space.Used, nil
}

// Launch creates a container with limits, static IPv4 (and optional static
// IPv6), root size and autostart enabled, then starts it and waits until it is
// ready. security.nesting allows running Docker / nested containers inside.
// pool and bridge name the storage pool and managed bridge (from config).
// cpu is a quota in tenths of a core (see cpuLimitConfig).
// Everything is submitted in ONE create request — the config, the eth0 static
// addresses and the root size — so no follow-up device overrides are needed.
func (c *Client) Launch(pool, bridge, name, image, ip, ipv6 string, cpu, memMB, diskGB int) error {
	eth0 := device{
		"type":         "nic",
		"nictype":      "bridged",
		"parent":       bridge,
		"name":         "eth0",
		"ipv4.address": ip,
	}
	if ipv6 != "" {
		eth0["ipv6.address"] = ipv6
	}
	config := cpuLimitConfig(cpu)
	config["limits.memory"] = strconv.Itoa(memMB) + "MiB"
	config["boot.autostart"] = "true"
	config["security.nesting"] = "true"
	req := createReq{
		Name:   name,
		Source: map[string]string{"type": "image", "alias": image},
		Config: config,
		Devices: map[string]device{
			"eth0": eth0,
			"root": {"type": "disk", "path": "/", "pool": pool, "size": strconv.Itoa(diskGB) + "GiB"},
		},
		Profiles: []string{"default"},
	}
	if err := c.sendOp(http.MethodPost, "/1.0/instances", req, 5*time.Minute); err != nil {
		return err
	}
	// Creating an instance leaves it Stopped; start it before waiting.
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

// ExecSH runs a shell script inside a container as root. Kept on the `lxc`
// CLI: exec needs a websocket transport, and the scripted calls (provisioning,
// the readiness probe) are infrequent relative to the panel's polling loops.
func (c *Client) ExecSH(name, script string) (string, error) {
	out, err := exec.Command("lxc", "exec", name, "--", "/bin/sh", "-c", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("lxc exec %s: %s", name, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
