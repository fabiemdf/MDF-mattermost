package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// defaultConfig returns a minimal plugin config map suitable for most tests.
func defaultConfig() map[string]interface{} {
	return map[string]interface{}{
		"shabbat_candle_offset_minutes":   "18",
		"shabbat_havdalah_offset_minutes": "42",
		"block_mode":                      "warn",
		"default_city":                    "New York",
	}
}

// newPlugin wires up a ShabbatPlugin with a mocked API.
func newPlugin(api *plugintest.API) *ShabbatPlugin {
	p := &ShabbatPlugin{}
	p.SetAPI(api)
	return p
}

// hebcalBody builds a minimal HebCal zmanim JSON body.
func hebcalBody(candleLighting, tzet time.Time) string {
	return fmt.Sprintf(`{"zmanim":{"candleLighting":%q,"tzet":%q}}`,
		candleLighting.Format(time.RFC3339),
		tzet.Format(time.RFC3339),
	)
}

// makeActiveWindow returns a shabbatWindow whose Shabbat is currently active.
func makeActiveWindow() *shabbatWindow {
	now := time.Now()
	return &shabbatWindow{
		Start:    now.Add(-1 * time.Hour),
		End:      now.Add(2 * time.Hour),
		Havdalah: "21:00",
	}
}

// injectWindowCache stores a shabbatWindow in the mock KV so windowForUser
// returns it without making a live HebCal call.
func injectWindowCache(api *plugintest.API, userID string, w *shabbatWindow) {
	key := "shabbat_window_" + userID + "_" + time.Now().UTC().Format("2006-01-02")
	data, _ := json.Marshal(w)
	api.On("KVGet", key).Return(data, (*model.AppError)(nil))
}

// ============================================================
// shabbatWindow.isActive()
// ============================================================

func TestShabbatWindow_IsActive_DuringShabbat(t *testing.T) {
	now := time.Now()
	w := shabbatWindow{Start: now.Add(-1 * time.Hour), End: now.Add(1 * time.Hour)}
	assert.True(t, w.isActive())
}

func TestShabbatWindow_IsActive_BeforeStart(t *testing.T) {
	now := time.Now()
	w := shabbatWindow{Start: now.Add(1 * time.Hour), End: now.Add(3 * time.Hour)}
	assert.False(t, w.isActive())
}

func TestShabbatWindow_IsActive_AfterEnd(t *testing.T) {
	now := time.Now()
	w := shabbatWindow{Start: now.Add(-3 * time.Hour), End: now.Add(-1 * time.Hour)}
	assert.False(t, w.isActive())
}

// ============================================================
// fetchHebcalWindowFromURL — fix #3 (URL encoding) & fix #4 (no double offset)
// ============================================================

// TestFetchHebcalWindow_URLEncoding verifies that a city name containing spaces
// ("New York") is percent-encoded in the outgoing request URL.
// Before fix #3, the literal space caused http.Get to return an error, and
// Shabbat detection silently returned false for all users using the default city.
func TestFetchHebcalWindow_URLEncoding(t *testing.T) {
	now := time.Now()
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		fmt.Fprint(w, hebcalBody(now.Add(1*time.Hour), now.Add(2*time.Hour)))
	}))
	defer srv.Close()

	p := &ShabbatPlugin{}
	_, err := p.fetchHebcalWindowFromURL(srv.URL, "New York", 42)
	require.NoError(t, err)

	// url.QueryEscape encodes spaces as "+" in query strings.
	assert.True(t,
		strings.Contains(capturedQuery, "New+York") || strings.Contains(capturedQuery, "New%20York"),
		"space in city must be percent-encoded; raw query was: %s", capturedQuery)
}

// TestFetchHebcalWindow_NoCandleOffsetDoubleSubtraction verifies fix #4:
// shabbatStart must equal candleLighting exactly. The candleOffset must NOT be
// subtracted from a value HebCal already computed with that offset applied.
func TestFetchHebcalWindow_NoCandleOffsetDoubleSubtraction(t *testing.T) {
	now := time.Now()
	candleTime := now.Add(30 * time.Minute)
	tzetTime := now.Add(90 * time.Minute)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, hebcalBody(candleTime, tzetTime))
	}))
	defer srv.Close()

	p := &ShabbatPlugin{}
	win, err := p.fetchHebcalWindowFromURL(srv.URL, "Jerusalem", 42)
	require.NoError(t, err)

	// With the old bug, Start would be candleTime − 18 min.
	assert.WithinDuration(t, candleTime, win.Start, time.Second,
		"Start must equal candleLighting; the candle offset must not be subtracted again")
}

// TestFetchHebcalWindow_HavdalahOffset confirms havdalahOffsetMins is added to tzet.
func TestFetchHebcalWindow_HavdalahOffset(t *testing.T) {
	now := time.Now()
	tzet := now.Add(2 * time.Hour)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, hebcalBody(now.Add(1*time.Hour), tzet))
	}))
	defer srv.Close()

	p := &ShabbatPlugin{}
	win, err := p.fetchHebcalWindowFromURL(srv.URL, "London", 42)
	require.NoError(t, err)

	assert.WithinDuration(t, tzet.Add(42*time.Minute), win.End, time.Second)
}

// TestFetchHebcalWindow_ServerError confirms an error is returned — not a panic
// or a zero-value window — when the API is unavailable.
func TestFetchHebcalWindow_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "oops")
	}))
	defer srv.Close()

	p := &ShabbatPlugin{}
	_, err := p.fetchHebcalWindowFromURL(srv.URL, "Brooklyn", 42)
	assert.Error(t, err)
}

// ============================================================
// MessageWillBePosted — fix #2 (warn mode allows post)
// ============================================================

// TestMessageWillBePosted_WarnMode_AllowsPost verifies fix #2:
// "warn" must return (post, "") so the message is delivered, and send an
// ephemeral notice only to the sender.
func TestMessageWillBePosted_WarnMode_AllowsPost(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(map[string]interface{}{
		"block_mode":                      "warn",
		"shabbat_candle_offset_minutes":   "18",
		"shabbat_havdalah_offset_minutes": "42",
		"default_city":                    "New York",
	})
	api.On("SendEphemeralPost", "user1", mock.Anything).Return((*model.Post)(nil))

	p := newPlugin(api)
	injectWindowCache(api, "user1", makeActiveWindow())

	post := &model.Post{UserId: "user1", ChannelId: "chan1", Message: "Good Shabbos!"}
	returned, errStr := p.MessageWillBePosted(&plugin.Context{}, post)

	assert.NotNil(t, returned, "warn mode: post must be returned so it is delivered")
	assert.Empty(t, errStr, "warn mode: error string must be empty")
	api.AssertExpectations(t)
}

// TestMessageWillBePosted_BlockMode_RejectsPost confirms "block" returns a
// non-empty error string, which the Mattermost server treats as a rejection.
func TestMessageWillBePosted_BlockMode_RejectsPost(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(map[string]interface{}{
		"block_mode":                      "block",
		"shabbat_candle_offset_minutes":   "18",
		"shabbat_havdalah_offset_minutes": "42",
		"default_city":                    "New York",
	})

	p := newPlugin(api)
	injectWindowCache(api, "user1", makeActiveWindow())

	post := &model.Post{UserId: "user1", ChannelId: "chan1", Message: "Hello"}
	returned, errStr := p.MessageWillBePosted(&plugin.Context{}, post)

	assert.Nil(t, returned)
	assert.NotEmpty(t, errStr)
}

// TestMessageWillBePosted_OutsideShabbat_PassesThrough confirms no blocking
// occurs when the Shabbat window is not yet active.
func TestMessageWillBePosted_OutsideShabbat_PassesThrough(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(map[string]interface{}{
		"block_mode":                      "block",
		"shabbat_candle_offset_minutes":   "18",
		"shabbat_havdalah_offset_minutes": "42",
		"default_city":                    "New York",
	})

	p := newPlugin(api)
	now := time.Now()
	injectWindowCache(api, "user1", &shabbatWindow{
		Start: now.Add(2 * time.Hour),
		End:   now.Add(4 * time.Hour),
	})

	post := &model.Post{UserId: "user1", ChannelId: "chan1", Message: "Hello"}
	returned, errStr := p.MessageWillBePosted(&plugin.Context{}, post)

	assert.Equal(t, post, returned)
	assert.Empty(t, errStr)
}

// ============================================================
// UserWillLogIn — fix #1 (correct signature)
// ============================================================

// TestUserWillLogIn_BlocksDuringShabbat is also a compile-time signature check:
// if this file compiles, UserWillLogIn has the correct two-parameter signature
// matching the Hooks interface (fix #1).
func TestUserWillLogIn_BlocksDuringShabbat(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultConfig())

	p := newPlugin(api)
	injectWindowCache(api, "user1", makeActiveWindow())

	result := p.UserWillLogIn(&plugin.Context{}, &model.User{Id: "user1"})

	assert.NotEmpty(t, result)
	assert.Contains(t, result, "Shabbat")
}

func TestUserWillLogIn_AllowsOutsideShabbat(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultConfig())

	p := newPlugin(api)
	now := time.Now()
	injectWindowCache(api, "user1", &shabbatWindow{
		Start: now.Add(3 * time.Hour),
		End:   now.Add(5 * time.Hour),
	})

	result := p.UserWillLogIn(&plugin.Context{}, &model.User{Id: "user1"})
	assert.Empty(t, result)
}
