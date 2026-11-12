package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
	sessionTTL   = 30 * 24 * time.Hour
)

// HashSecret 用 Argon2id 生成 PHC 字符串，运行期只保存这个结果。
func HashSecret(secret string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func VerifySecret(encoded, secret string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(secret), salt, timeCost, memory, threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

// Sessions 签发与校验无状态会话令牌：<base64url(exp|nonce)>.<base64url(HMAC)>。
type Sessions struct {
	key []byte
	now func() time.Time
}

func NewSessions(key []byte) *Sessions {
	return &Sessions{key: key, now: time.Now}
}

func (s *Sessions) Issue() (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	expire := s.now().Add(sessionTTL).Unix()
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(expire, 10) + "|" + base64.RawURLEncoding.EncodeToString(nonce)),
	)
	return payload + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload)), nil
}

// Valid 先验签名再验过期，签名不符时不暴露载荷内容。
func (s *Sessions) Valid(token string) bool {
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(s.sign(payload), provided) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	expireText, _, ok := strings.Cut(string(raw), "|")
	if !ok {
		return false
	}
	expire, err := strconv.ParseInt(expireText, 10, 64)
	if err != nil {
		return false
	}
	return s.now().Unix() < expire
}

func (s *Sessions) sign(payload string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("v1|" + payload))
	return mac.Sum(nil)
}

