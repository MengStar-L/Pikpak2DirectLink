package app

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	errQuotaReservationConflict    = errors.New("quota reservation conflicts with an existing job reservation")
	errQuotaReservationInvalidated = errors.New("quota reservation was invalidated")
)

// A negative generation marks an allocation invalidated by source-CDK
// revocation or removal of its required proxy permission.
const invalidQuotaReservationGeneration int64 = -1

type quotaReservationReconcileResult struct {
	SettledJobs   int
	ReleasedJobs  int
	SettledBytes  int64
	ReleasedBytes int64
}

type quotaReservationBucket struct {
	subscriptionID  string
	userID          string
	reservedBytes   int64
	requireProxy    bool
	quotaGeneration int64
}

// reserveQuota atomically takes available bytes from the earliest-expiring
// eligible subscriptions and records the exact allocation for the job.
func (s *userStore) reserveQuota(jobID, userID string, bytes int64, requireProxy bool, now time.Time) error {
	jobID = strings.TrimSpace(jobID)
	userID = strings.TrimSpace(userID)
	if jobID == "" {
		return errors.New("job ID is required for quota reservation")
	}
	if userID == "" {
		return errors.New("user ID is required for quota reservation")
	}
	if bytes <= 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := loadQuotaReservationsTx(tx, jobID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		var reserved int64
		for _, allocation := range existing {
			if allocation.quotaGeneration < 0 {
				return errors.Join(errUserQuotaExhausted, errQuotaReservationInvalidated)
			}
			reserved += allocation.reservedBytes
			if allocation.userID != userID || allocation.requireProxy != requireProxy {
				return fmt.Errorf("%w: %s", errQuotaReservationConflict, jobID)
			}
		}
		if reserved != bytes {
			return fmt.Errorf("%w: %s already reserves %d bytes", errQuotaReservationConflict, jobID, reserved)
		}
		return nil
	}

	query := `SELECT id, remaining_bytes, quota_generation
		FROM user_subscriptions
		WHERE user_id=? AND expires_at>? AND remaining_bytes>0`
	args := []any{userID, now.Unix()}
	if requireProxy {
		query += ` AND allow_proxy=1`
	}
	query += ` ORDER BY expires_at ASC, created_at ASC, id`

	rows, err := tx.Query(query, args...)
	if err != nil {
		return err
	}
	type availableBucket struct {
		id              string
		remaining       int64
		quotaGeneration int64
	}
	var available []availableBucket
	var total int64
	for rows.Next() {
		var bucket availableBucket
		if err := rows.Scan(&bucket.id, &bucket.remaining, &bucket.quotaGeneration); err != nil {
			rows.Close()
			return err
		}
		available = append(available, bucket)
		total += bucket.remaining
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if total <= 0 {
		return errUserQuotaExhausted
	}
	if total < bytes {
		return errUserQuotaOverdraw{size: bytes, remaining: total}
	}

	left := bytes
	for _, bucket := range available {
		if left == 0 {
			break
		}
		take := bucket.remaining
		if take > left {
			take = left
		}
		result, err := tx.Exec(
			`UPDATE user_subscriptions
			 SET remaining_bytes=remaining_bytes-?
			 WHERE id=? AND user_id=? AND remaining_bytes>=? AND quota_generation=?`,
			take, bucket.id, userID, take, bucket.quotaGeneration,
		)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("subscription quota changed while reserving")
		}
		if _, err := tx.Exec(
			`INSERT INTO user_quota_reservations
			 (job_id, subscription_id, user_id, reserved_bytes, require_proxy, created_at, quota_generation)
			 VALUES(?,?,?,?,?,?,?)`,
			jobID, bucket.id, userID, take, b2i(requireProxy), now.Unix(), bucket.quotaGeneration,
		); err != nil {
			return err
		}
		left -= take
	}
	if left != 0 {
		return errUserQuotaOverdraw{size: bytes, remaining: bytes - left}
	}
	return tx.Commit()
}

func (s *userStore) settleQuotaReservation(jobID string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	settled, err := s.settleQuotaReservationTx(tx, jobID)
	if err != nil {
		if errors.Is(err, errQuotaReservationInvalidated) {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return 0, errors.Join(err, fmt.Errorf("roll back invalidated quota settlement: %w", rollbackErr))
			}
			if _, releaseErr := s.releaseQuotaReservation(jobID); releaseErr != nil {
				return 0, errors.Join(err, fmt.Errorf("clean invalidated quota reservation: %w", releaseErr))
			}
		}
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return settled, nil
}

func (s *userStore) settleQuotaReservationTx(tx *sql.Tx, jobID string) (int64, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return 0, errors.New("job ID is required to settle quota reservation")
	}
	reservations, err := loadQuotaReservationsTx(tx, jobID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, reservation := range reservations {
		if reservation.quotaGeneration < 0 {
			return 0, errors.Join(errUserQuotaExhausted, errQuotaReservationInvalidated)
		}
		result, err := tx.Exec(
			`UPDATE user_subscriptions
			 SET used_bytes=used_bytes+?
			 WHERE id=? AND user_id=? AND quota_generation=?`,
			reservation.reservedBytes, reservation.subscriptionID, reservation.userID, reservation.quotaGeneration,
		)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed == 0 {
			generation, err := loadSubscriptionQuotaGenerationTx(tx, reservation.subscriptionID, reservation.userID)
			if err != nil {
				return 0, err
			}
			if generation == reservation.quotaGeneration {
				return 0, fmt.Errorf("reserved subscription %s could not be settled", reservation.subscriptionID)
			}
			// A CDK PATCH establishes a new available-remaining baseline. The old
			// debit is absorbed by that reset, while a successful job still counts
			// toward the subscription's actual used bytes.
			result, err = tx.Exec(
				`UPDATE user_subscriptions
				 SET used_bytes=used_bytes+?
				 WHERE id=? AND user_id=? AND quota_generation=?`,
				reservation.reservedBytes, reservation.subscriptionID, reservation.userID, generation,
			)
			if err != nil {
				return 0, err
			}
			if changed, err = result.RowsAffected(); err != nil {
				return 0, err
			}
			if changed != 1 {
				return 0, fmt.Errorf("reserved subscription %s changed while settling", reservation.subscriptionID)
			}
		} else if changed != 1 {
			return 0, fmt.Errorf("settled %d copies of reserved subscription %s", changed, reservation.subscriptionID)
		}
		total += reservation.reservedBytes
	}
	if _, err := tx.Exec(`DELETE FROM user_quota_reservations WHERE job_id=?`, jobID); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *userStore) releaseQuotaReservation(jobID string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	released, err := s.releaseQuotaReservationTx(tx, jobID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return released, nil
}

func (s *userStore) releaseQuotaReservationTx(tx *sql.Tx, jobID string) (int64, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return 0, errors.New("job ID is required to release quota reservation")
	}
	reservations, err := loadQuotaReservationsTx(tx, jobID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, reservation := range reservations {
		if reservation.quotaGeneration < 0 {
			continue
		}
		result, err := tx.Exec(
			`UPDATE user_subscriptions
			 SET remaining_bytes=remaining_bytes+?
			 WHERE id=? AND user_id=? AND quota_generation=?`,
			reservation.reservedBytes, reservation.subscriptionID, reservation.userID, reservation.quotaGeneration,
		)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed == 0 {
			generation, err := loadSubscriptionQuotaGenerationTx(tx, reservation.subscriptionID, reservation.userID)
			if err != nil {
				return 0, err
			}
			if generation == reservation.quotaGeneration {
				return 0, fmt.Errorf("reserved subscription %s could not be released", reservation.subscriptionID)
			}
			continue
		} else if changed != 1 {
			return 0, fmt.Errorf("released %d copies of reserved subscription %s", changed, reservation.subscriptionID)
		}
		total += reservation.reservedBytes
	}
	if _, err := tx.Exec(`DELETE FROM user_quota_reservations WHERE job_id=?`, jobID); err != nil {
		return 0, err
	}
	return total, nil
}

// reconcileQuotaReservations resolves crash leftovers from durable job state.
// Completed jobs consume their reservation; missing, failed, or expired jobs
// release it. Live nonterminal jobs are left untouched.
func (s *userStore) reconcileQuotaReservations(now time.Time) (quotaReservationReconcileResult, error) {
	var reconciled quotaReservationReconcileResult
	tx, err := s.db.Begin()
	if err != nil {
		return reconciled, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT reservations.job_id, COALESCE(jobs.status, ''),
		        COALESCE(jobs.record_expires_at, 0), COALESCE(jobs.charged_bytes, 0)
		 FROM (SELECT DISTINCT job_id FROM user_quota_reservations) AS reservations
		 LEFT JOIN resolve_jobs AS jobs ON jobs.id=reservations.job_id
		 ORDER BY reservations.job_id`,
	)
	if err != nil {
		return reconciled, err
	}
	type reservationJob struct {
		id              string
		status          string
		recordExpiresAt int64
		chargedBytes    int64
	}
	var jobs []reservationJob
	for rows.Next() {
		var job reservationJob
		if err := rows.Scan(&job.id, &job.status, &job.recordExpiresAt, &job.chargedBytes); err != nil {
			rows.Close()
			return reconciled, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return reconciled, err
	}

	for _, job := range jobs {
		switch {
		case job.status == string(JobCompleted):
			reservations, err := loadQuotaReservationsTx(tx, job.id)
			if err != nil {
				return reconciled, err
			}
			var reservedBytes int64
			for _, reservation := range reservations {
				reservedBytes += reservation.reservedBytes
			}
			if reservedBytes != job.chargedBytes {
				return reconciled, fmt.Errorf(
					"completed job %s charged %d bytes but reserved %d",
					job.id, job.chargedBytes, reservedBytes,
				)
			}
			settled, err := s.settleQuotaReservationTx(tx, job.id)
			if err != nil {
				return reconciled, err
			}
			reconciled.SettledJobs++
			reconciled.SettledBytes += settled
		case job.status == "", job.status == string(JobFailed), job.recordExpiresAt <= now.Unix():
			released, err := s.releaseQuotaReservationTx(tx, job.id)
			if err != nil {
				return reconciled, err
			}
			reconciled.ReleasedJobs++
			reconciled.ReleasedBytes += released
		}
	}
	if err := tx.Commit(); err != nil {
		return quotaReservationReconcileResult{}, err
	}
	return reconciled, nil
}

func loadQuotaReservationsTx(tx *sql.Tx, jobID string) ([]quotaReservationBucket, error) {
	rows, err := tx.Query(
		`SELECT subscription_id, user_id, reserved_bytes, require_proxy, quota_generation
		 FROM user_quota_reservations
		 WHERE job_id=?
		 ORDER BY subscription_id`,
		strings.TrimSpace(jobID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reservations []quotaReservationBucket
	for rows.Next() {
		var reservation quotaReservationBucket
		var requireProxy int
		if err := rows.Scan(
			&reservation.subscriptionID,
			&reservation.userID,
			&reservation.reservedBytes,
			&requireProxy,
			&reservation.quotaGeneration,
		); err != nil {
			return nil, err
		}
		reservation.requireProxy = requireProxy != 0
		reservations = append(reservations, reservation)
	}
	return reservations, rows.Err()
}

func loadSubscriptionQuotaGenerationTx(tx *sql.Tx, subscriptionID, userID string) (int64, error) {
	var generation int64
	err := tx.QueryRow(
		`SELECT quota_generation FROM user_subscriptions WHERE id=? AND user_id=?`,
		subscriptionID, userID,
	).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("reserved subscription %s no longer exists", subscriptionID)
	}
	return generation, err
}
