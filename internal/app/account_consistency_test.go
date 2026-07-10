package app

import "testing"

func TestAccountPoolRollsBackMemoryWhenDatabaseWriteFails(t *testing.T) {
	db := openAccountStoreTestDatabase(t)
	store := newAccountStore(db, newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef")))
	record := testAccountRecord("consistent", "consistent@example.com", "password", 1)
	if err := store.Insert(record); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	pool, err := NewAccountPool(AccountPoolConfig{Store: store})
	if err != nil {
		t.Fatalf("NewAccountPool: %v", err)
	}

	before := pool.List()[0]
	if err := db.Close(); err != nil {
		t.Fatalf("Close database: %v", err)
	}

	if err := pool.SetTrafficLimit(record.ID, before.TrafficLimit+1); err == nil {
		t.Fatal("SetTrafficLimit succeeded after the database was closed")
	}
	if got := pool.List()[0].TrafficLimit; got != before.TrafficLimit {
		t.Fatalf("traffic limit in memory = %d, want %d", got, before.TrafficLimit)
	}

	pool.MarkFailed(record.ID, errTestAccountWrite)
	afterFailure := pool.List()[0]
	if afterFailure.Status != before.Status || afterFailure.LastError != before.LastError {
		t.Fatalf("failed write changed in-memory status: before=%+v after=%+v", before, afterFailure)
	}

	if err := pool.Delete(record.ID); err == nil {
		t.Fatal("Delete succeeded after the database was closed")
	}
	if _, ok := pool.Summary(record.ID); !ok {
		t.Fatal("failed delete removed the account from memory")
	}
}

type accountWriteTestError string

func (e accountWriteTestError) Error() string { return string(e) }

const errTestAccountWrite accountWriteTestError = "write failed"
