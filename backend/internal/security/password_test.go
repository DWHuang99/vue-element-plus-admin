package security

import "testing"

func TestHashAndVerify(t *testing.T) {
	passwordHash, err := Hash("Test123456!")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}

	if !Verify("Test123456!", passwordHash) {
		t.Fatal("Verify() rejected the correct password")
	}
	if Verify("wrong-password", passwordHash) {
		t.Fatal("Verify() accepted an incorrect password")
	}
}
