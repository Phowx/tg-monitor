package telegramauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const verifierBotToken = "123456:canary-verifier-token"

func signInitDataForTest(values url.Values, botToken string) string {
	copyValues := make(url.Values, len(values)+1)
	for key, entries := range values {
		copyValues[key] = append([]string(nil), entries...)
	}
	keys := make([]string, 0, len(copyValues))
	for key := range copyValues {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+copyValues.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(strings.Join(lines, "\n")))
	copyValues.Set("hash", hex.EncodeToString(check.Sum(nil)))
	return copyValues.Encode()
}

func validInitDataFields(now time.Time) url.Values {
	return url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"query_id":  {"AAE-canary-query"},
		"user":      {`{"id":101,"first_name":"管理员","username":"ops"}`},
	}
}

func TestVerifyInitDataAcceptsOfficialHMACWithUnicodeAndReorderedFields(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	raw := signInitDataForTest(validInitDataFields(now), verifierBotToken)
	user, err := VerifyInitData(raw, verifierBotToken, now, 5*time.Minute, []int64{101, 202})
	if err != nil {
		t.Fatalf("VerifyInitData() error = %v", err)
	}
	want := User{ID: 101, FirstName: "管理员", Username: "ops"}
	if user != want {
		t.Fatalf("User = %#v, want %#v", user, want)
	}
}

func TestVerifyInitDataRejectsMalformedUntrustedAndExpiredValues(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	valid := func() string { return signInitDataForTest(validInitDataFields(now), verifierBotToken) }
	tests := []struct {
		name   string
		raw    func() string
		admins []int64
		want   error
	}{
		{name: "oversized", raw: func() string { return strings.Repeat("x", (16<<10)+1) }, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "invalid percent encoding", raw: func() string { return "auth_date=%zz" }, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "duplicate required field", raw: func() string { return valid() + "&auth_date=1" }, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "duplicate unrelated field", raw: func() string { return valid() + "&query_id=second" }, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "missing hash", raw: func() string { return validInitDataFields(now).Encode() }, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "missing auth date", raw: func() string {
			fields := validInitDataFields(now)
			fields.Del("auth_date")
			return signInitDataForTest(fields, verifierBotToken)
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "missing user", raw: func() string {
			fields := validInitDataFields(now)
			fields.Del("user")
			return signInitDataForTest(fields, verifierBotToken)
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "malformed hash hex", raw: func() string {
			fields := validInitDataFields(now)
			fields.Set("hash", strings.Repeat("z", 64))
			return fields.Encode()
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "short hash", raw: func() string { fields := validInitDataFields(now); fields.Set("hash", "00"); return fields.Encode() }, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "altered signed field", raw: func() string {
			values, _ := url.ParseQuery(valid())
			values.Set("query_id", "altered")
			return values.Encode()
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "stale", raw: func() string {
			fields := validInitDataFields(now.Add(-5*time.Minute - time.Second))
			return signInitDataForTest(fields, verifierBotToken)
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "future", raw: func() string {
			fields := validInitDataFields(now.Add(31 * time.Second))
			return signInitDataForTest(fields, verifierBotToken)
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "zero user ID", raw: func() string {
			fields := validInitDataFields(now)
			fields.Set("user", `{"id":0,"first_name":"x"}`)
			return signInitDataForTest(fields, verifierBotToken)
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "unknown user field", raw: func() string {
			fields := validInitDataFields(now)
			fields.Set("user", `{"id":101,"first_name":"x","unexpected":true}`)
			return signInitDataForTest(fields, verifierBotToken)
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "trailing user JSON", raw: func() string {
			fields := validInitDataFields(now)
			fields.Set("user", `{"id":101} {}`)
			return signInitDataForTest(fields, verifierBotToken)
		}, admins: []int64{101}, want: ErrInvalidInitData},
		{name: "non administrator", raw: valid, admins: []int64{202}, want: ErrUnauthorizedUser},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.raw()
			_, err := VerifyInitData(raw, verifierBotToken, now, 5*time.Minute, tt.admins)
			if !errors.Is(err, tt.want) {
				t.Fatalf("VerifyInitData() error = %v, want %v", err, tt.want)
			}
			for _, canary := range []string{verifierBotToken, raw, `{"id":101`, "AAE-canary-query"} {
				if strings.Contains(err.Error(), canary) {
					t.Fatalf("error leaked %q: %v", canary, err)
				}
			}
		})
	}
}

func TestVerifyInitDataRejectsInvalidVerifierConfiguration(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	raw := signInitDataForTest(validInitDataFields(now), verifierBotToken)
	for _, input := range []struct {
		token  string
		maxAge time.Duration
	}{
		{token: "", maxAge: time.Minute},
		{token: verifierBotToken, maxAge: 0},
		{token: verifierBotToken, maxAge: -time.Second},
	} {
		if _, err := VerifyInitData(raw, input.token, now, input.maxAge, []int64{101}); !errors.Is(err, ErrInvalidInitData) {
			t.Fatalf("VerifyInitData(config) error = %v, want ErrInvalidInitData", err)
		}
	}
}
