package telegramauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxInitDataBytes = 16 << 10

var (
	ErrInvalidInitData  = errors.New("invalid Telegram init data")
	ErrUnauthorizedUser = errors.New("Telegram user is not authorized")
)

type User struct {
	ID                    int64  `json:"id"`
	IsBot                 bool   `json:"is_bot,omitempty"`
	FirstName             string `json:"first_name,omitempty"`
	LastName              string `json:"last_name,omitempty"`
	Username              string `json:"username,omitempty"`
	LanguageCode          string `json:"language_code,omitempty"`
	IsPremium             bool   `json:"is_premium,omitempty"`
	AddedToAttachmentMenu bool   `json:"added_to_attachment_menu,omitempty"`
	AllowsWriteToPM       bool   `json:"allows_write_to_pm,omitempty"`
	PhotoURL              string `json:"photo_url,omitempty"`
}

func VerifyInitData(raw, botToken string, now time.Time, maxAge time.Duration, adminIDs []int64) (User, error) {
	if raw == "" || len(raw) > maxInitDataBytes || strings.TrimSpace(botToken) == "" || maxAge <= 0 {
		return User{}, ErrInvalidInitData
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return User{}, ErrInvalidInitData
	}
	for _, entries := range values {
		if len(entries) != 1 {
			return User{}, ErrInvalidInitData
		}
	}
	for _, required := range []string{"hash", "auth_date", "user"} {
		if _, exists := values[required]; !exists || values.Get(required) == "" {
			return User{}, ErrInvalidInitData
		}
	}

	suppliedHash, err := hex.DecodeString(values.Get("hash"))
	if err != nil || len(suppliedHash) != sha256.Size {
		return User{}, ErrInvalidInitData
	}
	keys := make([]string, 0, len(values)-1)
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(strings.Join(lines, "\n")))
	if !hmac.Equal(check.Sum(nil), suppliedHash) {
		return User{}, ErrInvalidInitData
	}

	authSeconds, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || authSeconds <= 0 {
		return User{}, ErrInvalidInitData
	}
	authTime := time.Unix(authSeconds, 0)
	if now.Sub(authTime) > maxAge || authTime.After(now.Add(30*time.Second)) {
		return User{}, ErrInvalidInitData
	}

	decoder := json.NewDecoder(bytes.NewBufferString(values.Get("user")))
	decoder.DisallowUnknownFields()
	var user User
	if err := decoder.Decode(&user); err != nil || user.ID <= 0 {
		return User{}, ErrInvalidInitData
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return User{}, ErrInvalidInitData
	}
	for _, adminID := range adminIDs {
		if adminID == user.ID {
			return user, nil
		}
	}
	return User{}, ErrUnauthorizedUser
}
