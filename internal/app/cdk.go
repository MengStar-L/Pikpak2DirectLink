package app

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// cdkAlphabet excludes visually ambiguous characters (0/O, 1/I) so codes are
// easy to read aloud and type.
const cdkAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const maxCDKBatch = 100

var (
	errCDKNotFound  = errors.New("CDK 不存在")
	errCDKExpired   = errors.New("CDK 已过期")
	errCDKExhausted = errors.New("CDK 可解析次数已用完")
)

// CDK is the stored representation of a redemption code.
type CDK struct {
	Code      string
	Remaining int
	Used      int
	ExpiresAt int64 // unix seconds
	CreatedAt int64 // unix seconds
}

// cdkView is the JSON shape returned to clients, with derived fields.
type cdkView struct {
	Code      string `json:"code"`
	Remaining int    `json:"remaining"`
	Used      int    `json:"used"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
	DaysLeft  int    `json:"days_left"`
	Expired   bool   `json:"expired"`
}

func toCDKView(c CDK, now time.Time) cdkView {
	expired := c.ExpiresAt <= now.Unix()
	daysLeft := 0
	if !expired {
		daysLeft = int((c.ExpiresAt - now.Unix() + 86399) / 86400) // ceil to whole days
	}
	return cdkView{
		Code:      c.Code,
		Remaining: c.Remaining,
		Used:      c.Used,
		ExpiresAt: time.Unix(c.ExpiresAt, 0).Format(time.RFC3339),
		CreatedAt: time.Unix(c.CreatedAt, 0).Format(time.RFC3339),
		DaysLeft:  daysLeft,
		Expired:   expired,
	}
}

type cdkStore struct {
	db *sql.DB
}

func newCDKStore(db *sql.DB) *cdkStore {
	return &cdkStore{db: db}
}

func generateCDKCode() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, v := range buf {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(cdkAlphabet[int(v)%len(cdkAlphabet)])
	}
	return sb.String(), nil
}

// normalizeCode upper-cases and trims a user-entered code so minor formatting
// differences still match.
func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// createBatch generates count new CDKs sharing the same quota and expiry.
func (s *cdkStore) createBatch(count, remaining, days int, now time.Time) ([]CDK, error) {
	if count < 1 {
		count = 1
	}
	if count > maxCDKBatch {
		count = maxCDKBatch
	}
	if remaining < 0 {
		remaining = 0
	}
	if days < 1 {
		days = 1
	}

	expiresAt := now.Add(time.Duration(days) * 24 * time.Hour).Unix()
	createdAt := now.Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	out := make([]CDK, 0, count)
	for i := 0; i < count; i++ {
		var code string
		for attempt := 0; attempt < 6; attempt++ {
			candidate, err := generateCDKCode()
			if err != nil {
				return nil, err
			}
			_, err = tx.Exec(
				`INSERT INTO cdks(code, remaining, used, expires_at, created_at) VALUES(?,?,?,?,?)`,
				candidate, remaining, 0, expiresAt, createdAt,
			)
			if err == nil {
				code = candidate
				break
			}
			// Most likely a UNIQUE collision; retry with a new code.
		}
		if code == "" {
			return nil, errors.New("无法生成唯一的 CDK，请重试")
		}
		out = append(out, CDK{Code: code, Remaining: remaining, ExpiresAt: expiresAt, CreatedAt: createdAt})
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *cdkStore) list() ([]CDK, error) {
	rows, err := s.db.Query(`SELECT code, remaining, used, expires_at, created_at FROM cdks ORDER BY created_at DESC, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CDK
	for rows.Next() {
		var c CDK
		if err := rows.Scan(&c.Code, &c.Remaining, &c.Used, &c.ExpiresAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *cdkStore) get(code string) (CDK, bool, error) {
	var c CDK
	err := s.db.QueryRow(
		`SELECT code, remaining, used, expires_at, created_at FROM cdks WHERE code=?`, code,
	).Scan(&c.Code, &c.Remaining, &c.Used, &c.ExpiresAt, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CDK{}, false, nil
	}
	if err != nil {
		return CDK{}, false, err
	}
	return c, true, nil
}

// update resets a CDK's remaining quota and pushes its expiry to now+days.
func (s *cdkStore) update(code string, remaining, days int, now time.Time) (CDK, bool, error) {
	if remaining < 0 {
		remaining = 0
	}
	if days < 1 {
		days = 1
	}
	expiresAt := now.Add(time.Duration(days) * 24 * time.Hour).Unix()
	res, err := s.db.Exec(`UPDATE cdks SET remaining=?, expires_at=? WHERE code=?`, remaining, expiresAt, code)
	if err != nil {
		return CDK{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return CDK{}, false, nil
	}
	return s.get(code)
}

func (s *cdkStore) delete(code string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM cdks WHERE code=?`, code)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// reserve atomically consumes one parse credit. It returns a typed error when
// the CDK is missing, expired, or exhausted so callers can refund or report.
func (s *cdkStore) reserve(code string, now time.Time) (CDK, error) {
	res, err := s.db.Exec(
		`UPDATE cdks SET remaining=remaining-1, used=used+1 WHERE code=? AND remaining>0 AND expires_at>?`,
		code, now.Unix(),
	)
	if err != nil {
		return CDK{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		c, _, err := s.get(code)
		return c, err
	}

	c, ok, err := s.get(code)
	if err != nil {
		return CDK{}, err
	}
	switch {
	case !ok:
		return CDK{}, errCDKNotFound
	case c.ExpiresAt <= now.Unix():
		return CDK{}, errCDKExpired
	default:
		return CDK{}, errCDKExhausted
	}
}

// refund returns a previously reserved credit, e.g. when a parse job fails.
func (s *cdkStore) refund(code string) error {
	_, err := s.db.Exec(`UPDATE cdks SET remaining=remaining+1, used=max(used-1,0) WHERE code=?`, code)
	return err
}
