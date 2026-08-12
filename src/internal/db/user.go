package db

import (
	"database/sql"
	"fmt"

	"vpsmgr/internal/cfg"
)

type User struct {
	ID         int64
	Name       string
	PassHash   string
	Idx        int
	IP         string
	SSHPort    int
	StartPort  int
	InitScript string
	CPU        int
	MemMB      int
	DiskGB     int
	CreatedAt  string
	// TrafficQuotaGB is the monthly traffic quota (upload + download) in GiB.
	// 0 means unlimited.
	TrafficQuotaGB int
}

func (d *DB) CreateUser(name, passHash, ip string, idx, sshPort, startPort, cpu, memMB, diskGB int) (*User, error) {
	u := &User{Name: name, PassHash: passHash, Idx: idx, IP: ip, SSHPort: sshPort, StartPort: startPort,
		CPU: cpu, MemMB: memMB, DiskGB: diskGB, CreatedAt: now()}
	r, err := d.sql.Exec(
		`INSERT INTO users(name, pass_hash, idx, ip, ssh_port, start_port, cpu, mem_mb, disk_gb, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		u.Name, u.PassHash, u.Idx, u.IP, u.SSHPort, u.StartPort, u.CPU, u.MemMB, u.DiskGB, u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.ID, _ = r.LastInsertId()
	return u, nil
}

// NextFreeIdx returns the smallest unused index in [1, cfg.MaxUsers].
func (d *DB) NextFreeIdx() (int, error) {
	rows, err := d.sql.Query(`SELECT idx FROM users ORDER BY idx`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return 0, err
		}
		used[i] = true
	}
	for i := 1; i <= cfg.MaxUsers; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("user limit reached (%d)", cfg.MaxUsers)
}

// UsedSSHPorts returns the set of ssh_port values already assigned to users,
// so the manager can pick a random free one.
func (d *DB) UsedSSHPorts() (map[int]bool, error) {
	rows, err := d.sql.Query(`SELECT ssh_port FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		used[p] = true
	}
	return used, rows.Err()
}

func scanUser(row *sql.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Name, &u.PassHash, &u.Idx, &u.IP, &u.SSHPort, &u.StartPort,
		&u.InitScript, &u.TrafficQuotaGB, &u.CPU, &u.MemMB, &u.DiskGB, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (d *DB) GetUserByName(name string) (*User, error) {
	return scanUser(d.sql.QueryRow(
		`SELECT id, name, pass_hash, idx, ip, ssh_port, start_port, init_script, traffic_quota_gb, cpu, mem_mb, disk_gb, created_at
		 FROM users WHERE name=?`, name))
}

func (d *DB) GetUserByID(id int64) (*User, error) {
	return scanUser(d.sql.QueryRow(
		`SELECT id, name, pass_hash, idx, ip, ssh_port, start_port, init_script, traffic_quota_gb, cpu, mem_mb, disk_gb, created_at
		 FROM users WHERE id=?`, id))
}

func (d *DB) ListUsers() ([]*User, error) {
	rows, err := d.sql.Query(
		`SELECT id, name, pass_hash, idx, ip, ssh_port, start_port, init_script, traffic_quota_gb, cpu, mem_mb, disk_gb, created_at
		 FROM users ORDER BY idx`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Name, &u.PassHash, &u.Idx, &u.IP, &u.SSHPort, &u.StartPort,
			&u.InitScript, &u.TrafficQuotaGB, &u.CPU, &u.MemMB, &u.DiskGB, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) DeleteUser(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

func (d *DB) UpdatePassword(id int64, passHash string) error {
	_, err := d.sql.Exec(`UPDATE users SET pass_hash=? WHERE id=?`, passHash, id)
	return err
}

func (d *DB) UpdateQuotas(id int64, cpu, memMB, diskGB int) error {
	_, err := d.sql.Exec(`UPDATE users SET cpu=?, mem_mb=?, disk_gb=? WHERE id=?`, cpu, memMB, diskGB, id)
	return err
}

// UpdateInitScript sets a user's custom init script (run inside the container
// after a reinstall). An empty string clears it.
func (d *DB) UpdateInitScript(id int64, script string) error {
	_, err := d.sql.Exec(`UPDATE users SET init_script=? WHERE id=?`, script, id)
	return err
}

// UpdateTrafficQuota sets a user's monthly traffic quota in GiB (0 = unlimited).
func (d *DB) UpdateTrafficQuota(id int64, gb int) error {
	_, err := d.sql.Exec(`UPDATE users SET traffic_quota_gb=? WHERE id=?`, gb, id)
	return err
}
