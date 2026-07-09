package app

import (
	"errors"
	"testing"
	"time"
)

func TestUserRedeemAndChargeSubscriptionsByExpiry(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Unix(1_700_000_000, 0)
	users := newUserStore(db)
	cdk := newCDKStore(db)
	user, err := users.createEmailUser("quota@example.com", "secret-pass", now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	short, err := cdk.createBatch(1, bytesPerGB, 7, true, now)
	if err != nil {
		t.Fatalf("create short cdk: %v", err)
	}
	long, err := cdk.createBatch(1, bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create long cdk: %v", err)
	}
	if _, err := users.redeemCDK(user.ID, short[0].Code, now); err != nil {
		t.Fatalf("redeem short: %v", err)
	}
	if _, err := users.redeemCDK(user.ID, long[0].Code, now); err != nil {
		t.Fatalf("redeem long: %v", err)
	}
	if _, err := users.redeemCDK(user.ID, long[0].Code, now); !errors.Is(err, errVoucherRedeemed) {
		t.Fatalf("repeat redeem err = %v, want errVoucherRedeemed", err)
	}

	if err := users.chargeIfEnough(user.ID, bytesPerGB+bytesPerGB/2, false, now); err != nil {
		t.Fatalf("charge across subscriptions: %v", err)
	}
	subs, err := users.listSubscriptions(user.ID, now)
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("subscriptions = %+v, want 2", subs)
	}
	if subs[0].RemainingBytes != 0 || subs[1].RemainingBytes != bytesPerGB/2 {
		t.Fatalf("remaining after charge = %d/%d, want 0/%d", subs[0].RemainingBytes, subs[1].RemainingBytes, bytesPerGB/2)
	}
}

func TestUserProxyChargeRequiresProxySubscription(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Unix(1_700_000_000, 0)
	users := newUserStore(db)
	cdk := newCDKStore(db)
	user, err := users.createEmailUser("proxy@example.com", "secret-pass", now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	directOnly, err := cdk.createBatch(1, bytesPerGB, 30, false, now)
	if err != nil {
		t.Fatalf("create direct-only cdk: %v", err)
	}
	if _, err := users.redeemCDK(user.ID, directOnly[0].Code, now); err != nil {
		t.Fatalf("redeem direct-only: %v", err)
	}
	if err := users.hasQuota(user.ID, 1, true, now); !errors.Is(err, errUserQuotaExhausted) {
		t.Fatalf("proxy quota err = %v, want errUserQuotaExhausted", err)
	}
	if err := users.hasQuota(user.ID, 1, false, now); err != nil {
		t.Fatalf("direct quota should be available: %v", err)
	}
}
