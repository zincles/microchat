// Package auth：登录凭据的两件工具 —— **JWT 签发/校验**（HS256）与**口令哈希**（bcrypt）。
//
// 口径见 DEFINE.md「用户与鉴权」：登录换 30 天定长 JWT；吊销靠 `ver`（口令版本）对不上，
// 或换 jwt_key（全员失效）。签名/校验**不手搓**（golang-jwt），口令哈希**不手搓**（bcrypt）。
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// TTL：登录令牌的寿命（定长；到期重登）。
const TTL = 30 * 24 * time.Hour

// MinPassword：口令最短（注册/改口令都按它拦）。
const MinPassword = 6

// Claims：JWT 里装的三样 —— sub（用户 id）/ adm（是不是 admin）/ ver（口令版本）。
type Claims struct {
	UserID int64 `json:"sub"`
	Admin  bool  `json:"adm"`
	Ver    int64 `json:"ver"`
	jwt.RegisteredClaims
}

// Sign：给一个人签一枚 30 天定长的 JWT。
func Sign(key []byte, userID int64, admin bool, ver int64) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID, Admin: admin, Ver: ver,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(TTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
}

// Verify：验签名与时效，回 claims；任何不对（换过头、过期、签错）都是一个 error。
func Verify(key []byte, raw string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("签名方式不对：%v", t.Header["alg"])
		}
		return key, nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("令牌无效")
	}
	return claims, nil
}

// HashPassword / CheckPassword：bcrypt 两件套（口令只以哈希落库）。
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
