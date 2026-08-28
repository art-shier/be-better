package auth

import (
	"sync"
	"testing"
	"time"
)

func TestPasswordObserverSeesHashAndVerifyDuration(t *testing.T) {
	var lock sync.Mutex
	operations := make([]string, 0, 2)
	restore := SetPasswordObserver(func(operation string, elapsed time.Duration) {
		lock.Lock()
		defer lock.Unlock()
		if elapsed < 0 {
			t.Errorf("negative password duration: %s", elapsed)
		}
		operations = append(operations, operation)
	})
	t.Cleanup(restore)

	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword(hash, "correct horse battery staple"); err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}

	lock.Lock()
	defer lock.Unlock()
	if len(operations) != 2 || operations[0] != "hash" || operations[1] != "verify" {
		t.Fatalf("password operations = %#v", operations)
	}
}

func TestPasswordAndTokenPrimitives(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("expected password to verify: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong password")
	if err != nil || ok {
		t.Fatalf("expected wrong password to fail: ok=%v err=%v", ok, err)
	}
	if PasswordHashNeedsUpgrade(hash) {
		t.Fatal("new password hash should use current parameters")
	}

	token, tokenHash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || len(tokenHash) != 32 {
		t.Fatalf("unexpected token output: token=%q hash bytes=%d", token, len(tokenHash))
	}
	if string(tokenHash) != string(HashToken(token)) {
		t.Fatal("stored token hash does not match token")
	}
}
