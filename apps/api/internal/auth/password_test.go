package auth

import "testing"

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
