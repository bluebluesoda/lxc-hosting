package db

import (
	"database/sql"
	"errors"
)

type User struct {
	ID        int64
	Name      string
	PassHash  string
	Idx       int
	IP        string
	PortBase  int
	CPU       int
	MemMB     int
	DiskGB    int
	CreatedAt string
}

const maxIdx = 253

func (d *DB) CreateUser(name, passHash, ip string, idx, portBase, cpu, memMB, diskGB int) (*User, error) {
	u := &User{Name: name, PassHash: passHash, Idx: idx, IP: ip, PortBase: portBase,
		CPU: cpu, MemMB: memMB, DiskGB: diskGB, CreatedAt: now()}
	r, err := d.sql.Exec(
		`INSERT INTO users(name, pass_hash, idx, ip, port_base, cpu, mem_mb, disk_gb, created_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		u.Name, u.PassHash, u.Idx, u.IP, u.PortBase, u.CPU, u.MemMB, u.DiskGB, u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.ID, _ = r.LastInsertId()
	return u, nil
}

// NextFreeIdx returns the smallest unused index in [1, maxIdx].
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
	for i := 1; i <= maxIdx; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, errors.New("user limit reached (253)")
}

func scanUser(row *sql.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Name, &u.PassHash, &u.Idx, &u.IP, &u.PortBase,
		&u.CPU, &u.MemMB, &u.DiskGB, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (d *DB) GetUserByName(name string) (*User, error) {
	return scanUser(d.sql.QueryRow(
		`SELECT id, name, pass_hash, idx, ip, port_base, cpu, mem_mb, disk_gb, created_at
		 FROM users WHERE name=?`, name))
}

func (d *DB) GetUserByID(id int64) (*User, error) {
	return scanUser(d.sql.QueryRow(
		`SELECT id, name, pass_hash, idx, ip, port_base, cpu, mem_mb, disk_gb, created_at
		 FROM users WHERE id=?`, id))
}

func (d *DB) ListUsers() ([]*User, error) {
	rows, err := d.sql.Query(
		`SELECT id, name, pass_hash, idx, ip, port_base, cpu, mem_mb, disk_gb, created_at
		 FROM users ORDER BY idx`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Name, &u.PassHash, &u.Idx, &u.IP, &u.PortBase,
			&u.CPU, &u.MemMB, &u.DiskGB, &u.CreatedAt); err != nil {
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
