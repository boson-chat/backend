package handlers

import (
	"context"
	"errors"
	stdhttp "net/http"
	"time"

	"github.com/boson-chat/backend/http/middleware"
	"github.com/boson-chat/backend/internal/services/authidentity"
	"github.com/boson-chat/backend/internal/services/avatar"
	"github.com/boson-chat/backend/internal/services/user"

	"github.com/google/uuid"
)

// PermUsersView is the auth-service permission that lets an admin read
// other people's accounts (the admin panel's user pages).
const PermUsersView = "users.view"

// IdentityFinder looks up an auth user's boson id without creating a
// mapping. Implemented by authidentity.Service.
type IdentityFinder interface {
	Find(ctx context.Context, authUserID string) (uuid.UUID, bool, error)
}

// AdminProfilesHandler serves GET /v1/admin/profiles/{auth_user_id}: the
// boson profile of an auth user, for the admin panel's user page. The panel
// calls it with the signed-in admin's own access token.
type AdminProfilesHandler struct {
	Users      user.UserServiceImpl
	Identities IdentityFinder
	// Avatar may be nil (R2 not configured): avatar_url is then omitted.
	Avatar avatar.ServiceImpl
}

func NewAdminProfilesHandler(users user.UserServiceImpl, identities IdentityFinder, avatars avatar.ServiceImpl) *AdminProfilesHandler {
	return &AdminProfilesHandler{Users: users, Identities: identities, Avatar: avatars}
}

func (h *AdminProfilesHandler) Register(mux *stdhttp.ServeMux) {
	mux.HandleFunc("GET /v1/admin/profiles/{auth_user_id}", h.get)
}

type adminProfile struct {
	UserID          uuid.UUID  `json:"user_id"`
	AuthUserID      string     `json:"auth_user_id"`
	Handle          string     `json:"handle"`
	DisplayName     *string    `json:"display_name,omitempty"`
	AvatarURL       string     `json:"avatar_url,omitempty"`
	IsDiscoverable  bool       `json:"is_discoverable"`
	HandleChangedAt *time.Time `json:"handle_changed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// adminProfileResponse: servers is always [] now. Networks people register
// live in the directory service (github.com/boson-chat/directory) since the
// backend's servers table was dropped; the field stays for the panel's shape.
type adminProfileResponse struct {
	Profile adminProfile `json:"profile"`
	Servers []struct{}   `json:"servers"`
}

func (h *AdminProfilesHandler) get(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	p := middleware.MustUser(r.Context())
	if !p.Can(PermUsersView) {
		writeError(w, stdhttp.StatusForbidden, "missing_permission")
		return
	}
	authUserID := r.PathValue("auth_user_id")
	if !authidentity.ValidSubject(authUserID) {
		writeError(w, stdhttp.StatusBadRequest, "invalid_auth_user_id")
		return
	}
	id, ok, err := h.Identities.Find(r.Context(), authUserID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok {
		writeError(w, stdhttp.StatusNotFound, "profile_not_found")
		return
	}
	u, err := h.Users.GetByID(r.Context(), id)
	if errors.Is(err, user.ErrNotFound) {
		writeError(w, stdhttp.StatusNotFound, "profile_not_found")
		return
	}
	if err != nil {
		writeInternalError(w, err)
		return
	}
	prof := adminProfile{
		UserID: u.ID, AuthUserID: authUserID, Handle: u.Handle, DisplayName: u.DisplayName,
		IsDiscoverable: u.IsDiscoverable, HandleChangedAt: u.HandleChangedAt, CreatedAt: u.CreatedAt,
	}
	if u.AvatarStorageKey != nil && h.Avatar != nil {
		prof.AvatarURL = h.Avatar.URLFor(*u.AvatarStorageKey)
	}
	writeJSON(w, stdhttp.StatusOK, adminProfileResponse{Profile: prof, Servers: []struct{}{}})
}
