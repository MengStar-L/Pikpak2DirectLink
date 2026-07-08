package app

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// cdkAlphabet excludes visually ambiguous characters (0/O, 1/I) so codes are
// easy to read aloud and type.
const cdkAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const maxCDKBatch = 100

var (
	errCDKNotFound      = errors.New("CDK not found")
	errCDKExpired       = errors.New("CDK expired")
	errCDKExhausted     = errors.New("CDK traffic exhausted")
	errCDKSameMergeCode = errors.New("primary and secondary CDK must be different")
	errCDKProxyMismatch = errors.New("CDK proxy permissions differ")
)

// errCDKOverdraw signals that a resolved file is larger than the CDK's remaining
// traffic. It is a deterministic, user-caused refusal — not an account fault —
// so the resolve loop must fail the job terminally instead of retrying on other
// accounts.
type errCDKOverdraw struct {
	size      int64
	remaining int64
}

func (e errCDKOverdraw) Error() string {
	return fmt.Sprintf("selected size %s exceeds remaining CDK traffic (%s remaining)", formatTrafficLabel(e.size), formatTrafficLabel(e.remaining))
}

// CDK is the stored representation of a redemption code. Quota is metered in
// bytes of downstream traffic (RemainingBytes left, UsedBytes consumed).
type CDK struct {
	Code           string
	RemainingBytes int64
	UsedBytes      int64
	ExpiresAt      int64 // unix seconds
	CreatedAt      int64 // unix seconds
	AllowProxy     bool  // whether this CDK may use proxy (中转) download
}

// cdkView is the JSON shape returned to clients, with derived fields. Traffic is
// reported both as raw bytes (for the UI to do exact math) and as a human label.
type cdkView struct {
	Code           string `json:"code"`
	RemainingBytes int64  `json:"remaining_bytes"`
	UsedBytes      int64  `json:"used_bytes"`
	RemainingLabel string `json:"remaining_label"`
	UsedLabel      string `json:"used_label"`
	ExpiresAt      string `json:"expires_at"`
	CreatedAt      string `json:"created_at"`
	DaysLeft       int    `json:"days_left"`
	Expired        bool   `json:"expired"`
	AllowProxy     bool   `json:"allow_proxy"`
}

func toCDKView(c CDK, now time.Time) cdkView {
	expired := c.ExpiresAt <= now.Unix()
	daysLeft := 0
	if !expired {
		daysLeft = int((c.ExpiresAt - now.Unix() + 86399) / 86400) // ceil to whole days
	}
	return cdkView{
		Code:           c.Code,
		RemainingBytes: c.RemainingBytes,
		UsedBytes:      c.UsedBytes,
		RemainingLabel: formatTrafficLabel(c.RemainingBytes),
		UsedLabel:      formatTrafficLabel(c.UsedBytes),
		ExpiresAt:      time.Unix(c.ExpiresAt, 0).Format(time.RFC3339),
		CreatedAt:      time.Unix(c.CreatedAt, 0).Format(time.RFC3339),
		DaysLeft:       daysLeft,
		Expired:        expired,
		AllowProxy:     c.AllowProxy,
	}
}

// b2i maps a bool to the 0/1 integer SQLite stores it as.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// formatTrafficLabel renders a byte count as a compact human-readable string
// using binary units (GiB shown as "GB"), matching the unit convention used for
// account limits.
func formatTrafficLabel(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(b)
	i := -1
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	if value >= 100 || value == float64(int64(value)) {
		return fmt.Sprintf("%.0f %s", value, units[i])
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
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

// createBatch generates count new CDKs sharing the same traffic quota (bytes),
// expiry, and proxy-download permission.
func (s *cdkStore) createBatch(count int, remainingBytes int64, days int, allowProxy bool, now time.Time) ([]CDK, error) {
	if count < 1 {
		count = 1
	}
	if count > maxCDKBatch {
		count = maxCDKBatch
	}
	if remainingBytes < 0 {
		remainingBytes = 0
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
				`INSERT INTO cdks(code, remaining_bytes, used_bytes, expires_at, created_at, allow_proxy) VALUES(?,?,?,?,?,?)`,
				candidate, remainingBytes, 0, expiresAt, createdAt, b2i(allowProxy),
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
		out = append(out, CDK{Code: code, RemainingBytes: remainingBytes, ExpiresAt: expiresAt, CreatedAt: createdAt, AllowProxy: allowProxy})
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *cdkStore) list() ([]CDK, error) {
	rows, err := s.db.Query(`SELECT code, remaining_bytes, used_bytes, expires_at, created_at, allow_proxy FROM cdks ORDER BY created_at DESC, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CDK
	for rows.Next() {
		var c CDK
		var allow int64
		if err := rows.Scan(&c.Code, &c.RemainingBytes, &c.UsedBytes, &c.ExpiresAt, &c.CreatedAt, &allow); err != nil {
			return nil, err
		}
		c.AllowProxy = allow != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *cdkStore) get(code string) (CDK, bool, error) {
	c, err := scanCDK(s.db.QueryRow(
		`SELECT code, remaining_bytes, used_bytes, expires_at, created_at, allow_proxy FROM cdks WHERE code=?`, code,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return CDK{}, false, nil
	}
	if err != nil {
		return CDK{}, false, err
	}
	return c, true, nil
}

type cdkScanner interface {
	Scan(dest ...any) error
}

func scanCDK(scanner cdkScanner) (CDK, error) {
	var c CDK
	var allow int64
	if err := scanner.Scan(&c.Code, &c.RemainingBytes, &c.UsedBytes, &c.ExpiresAt, &c.CreatedAt, &allow); err != nil {
		return CDK{}, err
	}
	c.AllowProxy = allow != 0
	return c, nil
}

// update resets a CDK's remaining traffic quota (bytes), pushes its expiry to
// now+days, and sets whether it may use proxy download.
func (s *cdkStore) update(code string, remainingBytes int64, days int, allowProxy bool, now time.Time) (CDK, bool, error) {
	if remainingBytes < 0 {
		remainingBytes = 0
	}
	if days < 1 {
		days = 1
	}
	expiresAt := now.Add(time.Duration(days) * 24 * time.Hour).Unix()
	res, err := s.db.Exec(`UPDATE cdks SET remaining_bytes=?, expires_at=?, allow_proxy=? WHERE code=?`, remainingBytes, expiresAt, b2i(allowProxy), code)
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

// merge moves the secondary CDK's remaining traffic into the primary CDK, keeps
// the later expiry, and deletes the secondary CDK in one transaction.
func (s *cdkStore) merge(primaryCode, secondaryCode string, now time.Time) (CDK, error) {
	primaryCode = normalizeCode(primaryCode)
	secondaryCode = normalizeCode(secondaryCode)
	if primaryCode == "" || secondaryCode == "" {
		return CDK{}, errCDKNotFound
	}
	if primaryCode == secondaryCode {
		return CDK{}, errCDKSameMergeCode
	}

	tx, err := s.db.Begin()
	if err != nil {
		return CDK{}, err
	}
	defer tx.Rollback()

	// SQLite serializes writers. Acquiring the writer lock before reading avoids
	// two concurrent merges both observing the same secondary CDK.
	if _, err := tx.Exec(`UPDATE cdks SET code=code WHERE code IN (?,?)`, primaryCode, secondaryCode); err != nil {
		return CDK{}, err
	}

	load := func(code string) (CDK, bool, error) {
		c, err := scanCDK(tx.QueryRow(
			`SELECT code, remaining_bytes, used_bytes, expires_at, created_at, allow_proxy FROM cdks WHERE code=?`,
			code,
		))
		if errors.Is(err, sql.ErrNoRows) {
			return CDK{}, false, nil
		}
		if err != nil {
			return CDK{}, false, err
		}
		return c, true, nil
	}

	primary, ok, err := load(primaryCode)
	if err != nil {
		return CDK{}, err
	}
	if !ok {
		return CDK{}, errCDKNotFound
	}
	secondary, ok, err := load(secondaryCode)
	if err != nil {
		return CDK{}, err
	}
	if !ok {
		return CDK{}, errCDKNotFound
	}

	nowUnix := now.Unix()
	if primary.ExpiresAt <= nowUnix || secondary.ExpiresAt <= nowUnix {
		return CDK{}, errCDKExpired
	}
	if secondary.RemainingBytes <= 0 {
		return CDK{}, errCDKExhausted
	}
	if primary.AllowProxy != secondary.AllowProxy {
		return CDK{}, errCDKProxyMismatch
	}

	expiresAt := primary.ExpiresAt
	if secondary.ExpiresAt > expiresAt {
		expiresAt = secondary.ExpiresAt
	}
	if _, err := tx.Exec(
		`UPDATE cdks SET remaining_bytes=remaining_bytes+?, expires_at=? WHERE code=?`,
		secondary.RemainingBytes, expiresAt, primary.Code,
	); err != nil {
		return CDK{}, err
	}
	if _, err := tx.Exec(`DELETE FROM cdks WHERE code=?`, secondary.Code); err != nil {
		return CDK{}, err
	}

	merged, ok, err := load(primaryCode)
	if err != nil {
		return CDK{}, err
	}
	if !ok {
		return CDK{}, errCDKNotFound
	}
	if err := tx.Commit(); err != nil {
		return CDK{}, err
	}
	return merged, nil
}

// deleteExpired removes every CDK whose expiry is at or before now, returning
// how many were deleted.
func (s *cdkStore) deleteExpired(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM cdks WHERE expires_at<=?`, now.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// hasTraffic validates that a CDK exists, is not expired, and still has traffic
// remaining, without mutating it. Charging happens later (at resolve success)
// once the resource size is known. Returns a typed error otherwise.
func (s *cdkStore) hasTraffic(code string, now time.Time) (CDK, error) {
	c, ok, err := s.get(code)
	if err != nil {
		return CDK{}, err
	}
	switch {
	case !ok:
		return CDK{}, errCDKNotFound
	case c.ExpiresAt <= now.Unix():
		return CDK{}, errCDKExpired
	case c.RemainingBytes <= 0:
		return CDK{}, errCDKExhausted
	default:
		return c, nil
	}
}

// charge deducts bytes of downstream traffic from a CDK after a successful
// resolve. Remaining is clamped at zero; used accumulates the full amount.
func (s *cdkStore) charge(code string, bytes int64) error {
	if bytes <= 0 {
		return nil
	}
	_, err := s.db.Exec(
		`UPDATE cdks SET remaining_bytes=max(remaining_bytes-?, 0), used_bytes=used_bytes+? WHERE code=?`,
		bytes, bytes, code,
	)
	return err
}

func (s *cdkStore) chargeIfEnough(code string, bytes int64, now time.Time) error {
	if bytes < 0 {
		bytes = 0
	}
	res, err := s.db.Exec(
		`UPDATE cdks
		 SET remaining_bytes=remaining_bytes-?, used_bytes=used_bytes+?
		 WHERE code=? AND expires_at>? AND remaining_bytes>=?`,
		bytes, bytes, code, now.Unix(), bytes,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}

	c, ok, err := s.get(code)
	if err != nil {
		return err
	}
	if !ok {
		return errCDKNotFound
	}
	if c.ExpiresAt <= now.Unix() {
		return errCDKExpired
	}
	if c.RemainingBytes <= 0 && bytes > 0 {
		return errCDKExhausted
	}
	if c.RemainingBytes < bytes {
		return errCDKOverdraw{size: bytes, remaining: c.RemainingBytes}
	}
	return errCDKExhausted
}
