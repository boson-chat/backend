package user

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateHandle_Valid(t *testing.T) {
	for _, h := range []string{
		"abc", "alice", "Alice-2", "a1b2c3", "[bot]", "_x_", "`ok`", "^^^", "{x}|y", `a\b`,
		strings.Repeat("a", HandleMaxLen), "rootbeer", "ChanServ2",
	} {
		assert.NoError(t, ValidateHandle(h), h)
	}
}

func TestValidateHandle_Invalid(t *testing.T) {
	for h, reason := range map[string]string{
		"":                                  "at least 3",
		"ab":                                "at least 3",
		strings.Repeat("a", HandleMaxLen+1): "at most 30",
		"1abc":                              "start with",
		"-abc":                              "start with",
		"ab c":                              "spaces",
		"ab.c":                              "only contain",
		"ab@c":                              "only contain",
		"ab~c":                              "only contain",
		"abc!":                              "only contain",
		"äbc":                               "start with",
		"abç":                               "only contain",
		"NickServ":                          "reserved",
		"chanserv":                          "reserved",
		"ROOT":                              "reserved",
		"irc":                               "reserved",
	} {
		err := ValidateHandle(h)
		require.Error(t, err, h)
		assert.ErrorIs(t, err, ErrHandleInvalid, h)
		var he *HandleError
		require.True(t, errors.As(err, &he), h)
		assert.Contains(t, he.Reason, reason, h)
	}
}

func TestUserService_Create_HandleNotANick(t *testing.T) {
	repo := newNotFoundRepo()
	svc := NewUserService(repo)
	_, err := svc.Create(context.Background(), CreateUserInput{ID: uuid.New(), Handle: "bad handle"})
	assert.ErrorIs(t, err, ErrHandleInvalid)
	assert.Empty(t, repo.createCalls, "invalid handles never reach the repository")
}

func TestUserService_UpdateHandle_NotANick(t *testing.T) {
	repo := newNotFoundRepo()
	svc := NewUserService(repo)
	for _, h := range []string{"9lives", "x.y.z", "NickServ", strings.Repeat("z", 31)} {
		_, err := svc.UpdateHandle(context.Background(), uuid.New(), h)
		assert.ErrorIs(t, err, ErrHandleInvalid, h)
	}
	assert.Empty(t, repo.updateHandleCalls)
}
