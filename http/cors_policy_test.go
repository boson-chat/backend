package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/boson-chat/backend/config"
)

// newCORSPolicy with "directory" follows ORIGINS_URL in the background and
// picks up changes on the refresh; fixed entries work throughout.
func TestNewCORSPolicy_FollowsDirectory(t *testing.T) {
	var body atomic.Value
	body.Store(`{"origins":["https://chat.boson.ircforever.org"]}`)
	auth := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		assert.Equal(t, "/v1/web-origins", r.URL.Path)
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer auth.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := newCORSPolicy(ctx, config.AppConfig{
		AllowedOrigins: "directory, https://chat.boson.chat, not-an-origin",
		OriginsURL:     auth.URL + "/v1/web-origins",
		OriginsRefresh: 20 * time.Millisecond,
	}, nil)

	assert.True(t, p.Allowed("https://chat.boson.chat"))
	assert.Eventually(t, func() bool { return p.Allowed("https://chat.boson.ircforever.org") }, 5*time.Second, 5*time.Millisecond)
	assert.False(t, p.Allowed("not-an-origin"))

	body.Store(`{"origins":["https://chat.internetneko.dev"]}`)
	assert.Eventually(t, func() bool {
		return p.Allowed("https://chat.internetneko.dev") && !p.Allowed("https://chat.boson.ircforever.org")
	}, 5*time.Second, 5*time.Millisecond)
	assert.True(t, p.Allowed("https://chat.boson.chat"))
}

func TestNewCORSPolicy_FixedOnly(t *testing.T) {
	p := newCORSPolicy(context.Background(), config.AppConfig{AllowedOrigins: "https://chat.boson.chat"}, nil)
	assert.True(t, p.Allowed("https://chat.boson.chat"))
	assert.False(t, p.Allowed("https://chat.boson.ircforever.org"))
}
