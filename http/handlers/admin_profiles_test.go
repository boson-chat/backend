package handlers

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/boson-chat/backend/http/middleware"
	"github.com/boson-chat/backend/internal/services/user"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubIdentities struct {
	ids map[string]uuid.UUID
	err error
}

func (s stubIdentities) Find(_ context.Context, authUserID string) (uuid.UUID, bool, error) {
	if s.err != nil {
		return uuid.Nil, false, s.err
	}
	id, ok := s.ids[authUserID]
	return id, ok, nil
}

var (
	profAuthID = "usr_01JTESTPROFILE"
	profUserID = uuid.MustParse("6f1c2a43-5b7e-4d2a-9c11-0a1b2c3d4e5f")
	adminP     = middleware.Principal{UserID: uuid.New(), AuthUserID: "usr_admin", Role: "user", Perms: []string{"admin.panel", "users.view"}, HasPerms: true}
)

func callAdminProfile(t *testing.T, users user.UserServiceImpl, ids IdentityFinder, p middleware.Principal, authUserID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := stdhttp.NewServeMux()
	NewAdminProfilesHandler(users, ids, nil).Register(mux)
	req := httptest.NewRequest(stdhttp.MethodGet, "/v1/admin/profiles/"+authUserID, nil)
	req = req.WithContext(middleware.WithUser(context.Background(), p))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func foundUsers(u *user.User) *stubUserService {
	return &stubUserService{getByID: func(_ context.Context, id uuid.UUID) (*user.User, error) {
		if u != nil && id == u.ID {
			return u, nil
		}
		return nil, user.ErrNotFound
	}}
}

func TestAdminProfiles_Found(t *testing.T) {
	name := "The Lion"
	changed := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	u := &user.User{ID: profUserID, Handle: "thelion", DisplayName: &name, IsDiscoverable: true,
		HandleChangedAt: &changed, CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		EncryptedUserSecret: []byte("secret-wrap"), EncryptedUserSecretRecovery: []byte("recovery-wrap")}
	rr := callAdminProfile(t, foundUsers(u), stubIdentities{ids: map[string]uuid.UUID{profAuthID: profUserID}}, adminP, profAuthID)
	require.Equal(t, stdhttp.StatusOK, rr.Code, rr.Body.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	prof := got["profile"].(map[string]any)
	assert.Equal(t, profUserID.String(), prof["user_id"])
	assert.Equal(t, profAuthID, prof["auth_user_id"])
	assert.Equal(t, "thelion", prof["handle"])
	assert.Equal(t, "The Lion", prof["display_name"])
	assert.Equal(t, true, prof["is_discoverable"])
	assert.NotEmpty(t, prof["handle_changed_at"])
	assert.NotEmpty(t, prof["created_at"])
	assert.Equal(t, []any{}, got["servers"], "servers is always a list")
	// Never the user's encrypted secret wraps.
	assert.NotContains(t, rr.Body.String(), "encrypted_user_secret")
	assert.NotContains(t, rr.Body.String(), "c2VjcmV0")
}

func TestAdminProfiles_NoMappingIsProfileNotFound(t *testing.T) {
	rr := callAdminProfile(t, foundUsers(nil), stubIdentities{}, adminP, profAuthID)
	assert.Equal(t, stdhttp.StatusNotFound, rr.Code)
	assert.JSONEq(t, `{"error":"profile_not_found"}`, rr.Body.String())
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
}

func TestAdminProfiles_MappingWithoutUserRow(t *testing.T) {
	rr := callAdminProfile(t, foundUsers(nil), stubIdentities{ids: map[string]uuid.UUID{profAuthID: profUserID}}, adminP, profAuthID)
	assert.Equal(t, stdhttp.StatusNotFound, rr.Code)
	assert.JSONEq(t, `{"error":"profile_not_found"}`, rr.Body.String())
}

func TestAdminProfiles_Permission(t *testing.T) {
	ids := stubIdentities{ids: map[string]uuid.UUID{profAuthID: profUserID}}
	u := &user.User{ID: profUserID, Handle: "thelion"}
	cases := []struct {
		name string
		p    middleware.Principal
		want int
	}{
		{"users.view", adminP, stdhttp.StatusOK},
		{"perms without users.view", middleware.Principal{Role: "admin", Perms: []string{"admin.panel"}, HasPerms: true}, stdhttp.StatusForbidden},
		{"empty perms", middleware.Principal{Role: "admin", Perms: []string{}, HasPerms: true}, stdhttp.StatusForbidden},
		{"legacy admin role", middleware.Principal{Role: "admin"}, stdhttp.StatusOK},
		{"legacy user role", middleware.Principal{Role: "user"}, stdhttp.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := callAdminProfile(t, foundUsers(u), ids, tc.p, profAuthID)
			assert.Equal(t, tc.want, rr.Code, rr.Body.String())
			if tc.want == stdhttp.StatusForbidden {
				assert.JSONEq(t, `{"error":"missing_permission"}`, rr.Body.String())
			}
		})
	}
}

func TestAdminProfiles_BadID(t *testing.T) {
	rr := callAdminProfile(t, foundUsers(nil), stubIdentities{}, adminP, "not-a-usr-id")
	assert.Equal(t, stdhttp.StatusBadRequest, rr.Code)
}

func TestAdminProfiles_Errors(t *testing.T) {
	rr := callAdminProfile(t, foundUsers(nil), stubIdentities{err: errors.New("db down")}, adminP, profAuthID)
	assert.Equal(t, stdhttp.StatusInternalServerError, rr.Code)
	assert.NotContains(t, rr.Body.String(), "db down")

	boom := &stubUserService{getByID: func(context.Context, uuid.UUID) (*user.User, error) { return nil, errors.New("pq: boom") }}
	rr = callAdminProfile(t, boom, stubIdentities{ids: map[string]uuid.UUID{profAuthID: profUserID}}, adminP, profAuthID)
	assert.Equal(t, stdhttp.StatusInternalServerError, rr.Code)
	assert.NotContains(t, rr.Body.String(), "boom")
}
