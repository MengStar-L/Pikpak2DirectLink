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
	errCDKNotFound  = errors.New("CDK not found")
	errCDKExpired   = errors.New("CDK expired")
	errCDKExhausted = errors.New("CDK traffic exhausted")
)

// CDK is an immutable grant snapshot once redeemed. Live quota and expiry are
// stored only in user_subscriptions.
type CDK struct {
	Code             string
	GrantBytes       int64
	DurationDays     int
	AllowProxy       bool
	CreatedAt        int64 // unix seconds
	RedeemedByUserID string
	RedeemedAt       int64 // unix seconds
	RevokedAt        int64 // unix seconds
}

type cdkView struct {
	Code             string `json:"code"`
	GrantBytes       int64  `json:"grant_bytes"`
	GrantLabel       string `json:"grant_label"`
	DurationDays     int    `json:"duration_days"`
	AllowProxy       bool   `json:"allow_proxy"`
	CreatedAt        string `json:"created_at"`
	Status           string `json:"status"`
	RedeemedByUserID string `json:"redeemed_by_user_id,omitempty"`
	RedeemedAt       string `json:"redeemed_at,omitempty"`
	RevokedAt        string `json:"revoked_at,omitempty"`
}

func toCDKView(c CDK) cdkView {
	status := "unredeemed"
	if c.RedeemedAt > 0 {
		status = "redeemed"
	} else if c.RevokedAt > 0 {
		status = "revoked"
	}
	view := cdkView{
		Code:             c.Code,
		GrantBytes:       c.GrantBytes,
		GrantLabel:       formatTrafficLabel(c.GrantBytes),
		DurationDays:     c.DurationDays,
		AllowProxy:       c.AllowProxy,
		CreatedAt:        time.Unix(c.CreatedAt, 0).UTC().Format(time.RFC3339),
		Status:           status,
		RedeemedByUserID: c.RedeemedByUserID,
	}
	if c.RedeemedAt > 0 {
		view.RedeemedAt = time.Unix(c.RedeemedAt, 0).UTC().Format(time.RFC3339)
	}
	if c.RevokedAt > 0 {
		view.RevokedAt = time.Unix(c.RevokedAt, 0).UTC().Format(time.RFC3339)
	}
	return view
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

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func (s *cdkStore) createBatch(count int, grantBytes int64, days int, allowProxy bool, now time.Time) ([]CDK, error) {
	if count < 1 {
		count = 1
	}
	if count > maxCDKBatch {
		count = maxCDKBatch
	}
	if grantBytes < 0 {
		grantBytes = 0
	}
	if days < 1 {
		days = 1
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	createdAt := now.Unix()
	out := make([]CDK, 0, count)
	for i := 0; i < count; i++ {
		var code string
		for attempt := 0; attempt < 6; attempt++ {
			candidate, err := generateCDKCode()
			if err != nil {
				return nil, err
			}
			_, err = tx.Exec(
				`INSERT INTO cdks(code, grant_bytes, duration_days, allow_proxy, created_at) VALUES(?,?,?,?,?)`,
				candidate, grantBytes, days, b2i(allowProxy), createdAt,
			)
			if err == nil {
				code = candidate
				break
			}
		}
		if code == "" {
			return nil, errors.New("unable to generate a unique CDK; please retry")
		}
		out = append(out, CDK{
			Code:         code,
			GrantBytes:   grantBytes,
			DurationDays: days,
			AllowProxy:   allowProxy,
			CreatedAt:    createdAt,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *cdkStore) list() ([]CDK, error) {
	rows, err := s.db.Query(cdkSelectSQL + ` ORDER BY created_at DESC, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CDK
	for rows.Next() {
		credential, err := scanCDK(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, credential)
	}
	return out, rows.Err()
}

func (s *cdkStore) get(code string) (CDK, bool, error) {
	credential, err := scanCDK(s.db.QueryRow(cdkSelectSQL+` WHERE code=?`, normalizeCode(code)))
	if errors.Is(err, sql.ErrNoRows) {
		return CDK{}, false, nil
	}
	if err != nil {
		return CDK{}, false, err
	}
	return credential, true, nil
}

type cdkScanner interface {
	Scan(dest ...any) error
}

const cdkSelectSQL = `SELECT
 code,
 grant_bytes,
 duration_days,
 allow_proxy,
 created_at,
 COALESCE(redeemed_by_user_id, ''),
 COALESCE(redeemed_at, 0),
 COALESCE(revoked_at, 0)
 FROM cdks`

func scanCDK(scanner cdkScanner) (CDK, error) {
	var credential CDK
	var allowProxy int
	if err := scanner.Scan(
		&credential.Code,
		&credential.GrantBytes,
		&credential.DurationDays,
		&allowProxy,
		&credential.CreatedAt,
		&credential.RedeemedByUserID,
		&credential.RedeemedAt,
		&credential.RevokedAt,
	); err != nil {
		return CDK{}, err
	}
	credential.AllowProxy = allowProxy != 0
	return credential, nil
}

func (s *cdkStore) update(code string, grantBytes int64, days int, allowProxy bool, _ time.Time) (CDK, bool, error) {
	code = normalizeCode(code)
	if grantBytes < 0 {
		grantBytes = 0
	}
	if days < 1 {
		days = 1
	}

	res, err := s.db.Exec(
		`UPDATE cdks
		 SET grant_bytes=?, duration_days=?, allow_proxy=?
		 WHERE code=?
		   AND (redeemed_at IS NULL OR redeemed_at=0)
		   AND (revoked_at IS NULL OR revoked_at=0)`,
		grantBytes, days, b2i(allowProxy), code,
	)
	if err != nil {
		return CDK{}, false, err
	}
	if changed, _ := res.RowsAffected(); changed > 0 {
		updated, _, err := s.get(code)
		return updated, true, err
	}
	return s.readOnlyResult(code)
}

func (s *cdkStore) revoke(code string, now time.Time) (CDK, bool, error) {
	code = normalizeCode(code)
	_, err := s.db.Exec(
		`UPDATE cdks
		 SET revoked_at=CASE WHEN revoked_at IS NULL OR revoked_at=0 THEN ? ELSE revoked_at END
		 WHERE code=? AND (redeemed_at IS NULL OR redeemed_at=0)`,
		now.Unix(), code,
	)
	if err != nil {
		return CDK{}, false, err
	}
	credential, ok, err := s.get(code)
	if err != nil || !ok {
		return credential, ok, err
	}
	if credential.RedeemedAt > 0 {
		return credential, true, errVoucherRedeemed
	}
	return credential, true, nil
}

func (s *cdkStore) readOnlyResult(code string) (CDK, bool, error) {
	credential, ok, err := s.get(code)
	if err != nil || !ok {
		return credential, ok, err
	}
	if credential.RedeemedAt > 0 {
		return credential, true, errVoucherRedeemed
	}
	if credential.RevokedAt > 0 {
		return credential, true, errVoucherRevoked
	}
	return credential, true, errors.New("CDK update lost a concurrent state change")
}
