package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWTRoundtripAndRejects(t *testing.T) {
	key := []byte("k1")
	token, err := Sign(key, 7, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(key, token)
	if err != nil || claims.UserID != 7 || !claims.Admin || claims.Ver != 3 {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	if _, err := Verify([]byte("k2"), token); err == nil {
		t.Fatal("换把 key 也该验不过")
	}
	if _, err := Verify(key, "garbage"); err == nil {
		t.Fatal("垃圾串也该验不过")
	}
	// 过期：手工签一枚 exp 已过的
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID: 1, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))},
	})
	raw, _ := expired.SignedString(key)
	if _, err := Verify(key, raw); err == nil {
		t.Fatal("过期令牌该验不过")
	}
}

func TestPasswordHashRoundtrip(t *testing.T) {
	hash, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "hunter22") {
		t.Fatal("对口令应通过")
	}
	if CheckPassword(hash, "hunter23") {
		t.Fatal("错口令不该通过")
	}
}
