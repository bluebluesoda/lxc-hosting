package db

import (
	"database/sql"
	"errors"
)

type Domain struct {
	ID        int64
	UserID    int64
	Domain    string
	CreatedAt string
}

func (d *DB) AddDomain(userID int64, domain string) (*Domain, error) {
	dmn := &Domain{UserID: userID, Domain: domain, CreatedAt: now()}
	r, err := d.sql.Exec(`INSERT INTO domains(user_id, domain, created_at) VALUES(?,?,?)`,
		userID, domain, dmn.CreatedAt)
	if err != nil {
		return nil, err
	}
	dmn.ID, _ = r.LastInsertId()
	return dmn, nil
}

func (d *DB) ListDomains(userID int64) ([]*Domain, error) {
	rows, err := d.sql.Query(`SELECT id, user_id, domain, created_at FROM domains WHERE user_id=? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Domain
	for rows.Next() {
		x := &Domain{}
		if err := rows.Scan(&x.ID, &x.UserID, &x.Domain, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (d *DB) DeleteDomain(userID int64, domain string) error {
	r, err := d.sql.Exec(`DELETE FROM domains WHERE user_id=? AND domain=?`, userID, domain)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return errors.New("domain not found")
	}
	return nil
}

func (d *DB) DomainExists(domain string) (bool, error) {
	var one int
	err := d.sql.QueryRow(`SELECT 1 FROM domains WHERE domain=?`, domain).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}
