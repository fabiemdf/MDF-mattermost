package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

// ShabbatPlugin implements Shabbat & Holiday Mode for KehilaConnect.
type ShabbatPlugin struct {
	plugin.MattermostPlugin
}

// shabbatWindow holds the computed start/end of the current Shabbat window
// alongside the havdalah display string. Caching the window (not just a bool)
// lets every call to isActive() use the current clock — a cache hit at 3 pm
// (isActive=false) still correctly returns true once Shabbat begins at 5 pm,
// because the Start/End timestamps survive in the cached entry.
type shabbatWindow struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Havdalah string    `json:"havdalah"`
}

func (w *shabbatWindow) isActive() bool {
	now := time.Now()
	return now.After(w.Start) && now.Before(w.End)
}

// shabbatStatus is returned by the /status REST endpoint consumed by the mobile app.
type shabbatStatus struct {
	IsShabbat bool   `json:"shabbat"`
	Havdalah  string `json:"havdalah"`
}

// hebcalZmanimResponse is a minimal struct for the HebCal zmanim API response.
type hebcalZmanimResponse struct {
	Zmanim struct {
		CandleLighting string `json:"candleLighting"`
		Tzet           string `json:"tzet"`
	} `json:"zmanim"`
}

func (p *ShabbatPlugin) pluginConfig() (candleOffset, havdalahOffset int, blockMode, defaultCity string) {
	cfg := p.API.GetPluginConfig()

	candleOffset = 18
	if v, ok := cfg["shabbat_candle_offset_minutes"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%v", v)); err == nil {
			candleOffset = n
		}
	}

	havdalahOffset = 42
	if v, ok := cfg["shabbat_havdalah_offset_minutes"]; ok {
		if n, err := strconv.Atoi(fmt.Sprintf("%v", v)); err == nil {
			havdalahOffset = n
		}
	}

	blockMode = "warn"
	if v, ok := cfg["block_mode"]; ok {
		blockMode = fmt.Sprintf("%v", v)
	}

	defaultCity = "New York"
	if v, ok := cfg["default_city"]; ok {
		defaultCity = fmt.Sprintf("%v", v)
	}

	return
}

// windowForUser returns the cached shabbatWindow for the user, refreshing from
// the HebCal API when the cache is absent or stale.
//
// Cache TTL is 4 minutes — short enough to handle Shabbat start/end promptly,
// long enough to avoid hammering the HebCal API under load.
//
// Fix #5: previously we cached a boolean (isShabbat), which allowed a 3 pm
// cache hit of false to serve stale results until 9 pm. Now we cache the
// window timestamps and call isActive() on each request so the answer is
// always evaluated against the current clock.
func (p *ShabbatPlugin) windowForUser(userID string) (*shabbatWindow, error) {
	const cacheTTLSecs = 4 * 60

	cacheKey := "shabbat_window_" + userID + "_" + time.Now().UTC().Format("2006-01-02")
	if cached, appErr := p.API.KVGet(cacheKey); appErr == nil && cached != nil {
		var w shabbatWindow
		if err := json.Unmarshal(cached, &w); err == nil {
			return &w, nil
		}
	}

	user, appErr := p.API.GetUser(userID)
	if appErr != nil {
		return nil, fmt.Errorf("get user: %w", appErr)
	}

	_, _, _, defaultCity := p.pluginConfig()
	city := defaultCity
	if user.Timezone != nil {
		if tz := user.Timezone["automaticTimezone"]; tz != "" {
			city = tz
		} else if tz := user.Timezone["manualTimezone"]; tz != "" {
			city = tz
		}
	}

	_, havdalahOffset, _, _ := p.pluginConfig()
	w, err := p.fetchHebcalWindow(city, havdalahOffset)
	if err != nil {
		return nil, err
	}

	if data, err := json.Marshal(w); err == nil {
		p.API.KVSetWithExpiry(cacheKey, data, cacheTTLSecs) //nolint:errcheck
	}

	return w, nil
}

// fetchHebcalWindow calls the HebCal zmanim API and returns the Shabbat window.
//
// Fix #3: city is URL-encoded so spaces ("New York") don't produce a malformed
// URL — previously http.Get silently failed and Shabbat detection returned
// false for every user with a space in their city name.
//
// Fix #4: HebCal's candleLighting field is already the candle-lighting time
// (typically 18 min before sunset). The code previously subtracted the
// configured offset *again*, activating Shabbat 36 min before sunset instead
// of 18. We now use candleTime directly as shabbatStart without subtracting.
func (p *ShabbatPlugin) fetchHebcalWindow(city string, havdalahOffsetMins int) (*shabbatWindow, error) {
	apiURL := fmt.Sprintf(
		"https://www.hebcal.com/zmanim?cfg=json&city=%s&date=%s",
		url.QueryEscape(city), // fix #3
		time.Now().Format("2006-01-02"),
	)

	resp, err := http.Get(apiURL) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("hebcal request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hebcal read body: %w", err)
	}

	var z hebcalZmanimResponse
	if err := json.Unmarshal(body, &z); err != nil {
		return nil, fmt.Errorf("hebcal parse: %w", err)
	}

	candleTime, err := time.Parse(time.RFC3339, z.Zmanim.CandleLighting)
	if err != nil {
		return nil, fmt.Errorf("parse candleLighting %q: %w", z.Zmanim.CandleLighting, err)
	}
	tzetTime, err := time.Parse(time.RFC3339, z.Zmanim.Tzet)
	if err != nil {
		return nil, fmt.Errorf("parse tzet %q: %w", z.Zmanim.Tzet, err)
	}

	// fix #4: candleTime is already the candle-lighting time; use it as-is.
	shabbatEnd := tzetTime.Add(time.Duration(havdalahOffsetMins) * time.Minute)

	return &shabbatWindow{
		Start:    candleTime,
		End:      shabbatEnd,
		Havdalah: shabbatEnd.Format("15:04"),
	}, nil
}

// UserWillLogIn blocks login during Shabbat.
//
// Fix #1: signature corrected to match the Hooks interface exactly:
//
//	UserWillLogIn(c *Context, user *model.User) string
//
// The old signature had an extra string parameter. server/public/plugin/
// client_rpc.go's Implemented() compares NumIn() and found a mismatch
// (4 != 3), so the hook was silently never registered.
func (p *ShabbatPlugin) UserWillLogIn(_ *plugin.Context, user *model.User) string {
	w, err := p.windowForUser(user.Id)
	if err != nil || !w.isActive() {
		return ""
	}
	msg := "Shabbat Shalom! KehilaConnect is in Shabbat Mode."
	if w.Havdalah != "" {
		msg += " Please return after Havdalah at " + w.Havdalah + "."
	}
	return msg
}

// MessageWillBePosted enforces the configured block_mode during Shabbat.
//
// Fix #2: "warn" mode now allows the post (returns post, "") and delivers an
// ephemeral notice to the sender. The previous code returned (nil, warning)
// in the "warn" case; a non-empty error string always rejects the post
// regardless of whether the first return is nil, so the post was silently
// dropped — contradicting the plugin.json help text.
func (p *ShabbatPlugin) MessageWillBePosted(c *plugin.Context, post *model.Post) (*model.Post, string) {
	w, err := p.windowForUser(post.UserId)
	if err != nil || !w.isActive() {
		return post, ""
	}

	_, _, blockMode, _ := p.pluginConfig()

	switch blockMode {
	case "block":
		return nil, "KehilaConnect is in Shabbat Mode. Posting is disabled until Havdalah."

	case "queue":
		return nil, plugin.DismissPostError

	default: // "warn" — let the post through, notify sender only
		warning := "✡ Shabbat Shalom! You are sending a message during Shabbat."
		if w.Havdalah != "" {
			warning += fmt.Sprintf(" Shabbat ends at %s.", w.Havdalah)
		}
		// fix #2: ephemeral post visible only to the sender, not the channel.
		p.API.SendEphemeralPost(post.UserId, &model.Post{
			ChannelId: post.ChannelId,
			Message:   warning,
		})
		return post, "" // allow the original post through
	}
}

// MessageWillBeUpdated applies the same gate to edits.
func (p *ShabbatPlugin) MessageWillBeUpdated(c *plugin.Context, newPost, oldPost *model.Post) (*model.Post, string) {
	return p.MessageWillBePosted(c, newPost)
}

// NotificationWillBePushed suppresses push notifications during Shabbat.
func (p *ShabbatPlugin) NotificationWillBePushed(pushNotification *model.PushNotification, userID string) (*model.PushNotification, string) {
	w, err := p.windowForUser(userID)
	if err != nil || !w.isActive() {
		return pushNotification, ""
	}
	return nil, ""
}

// ServeHTTP exposes GET /plugins/com.kehilaconnect.shabbat-mode/status.
func (p *ShabbatPlugin) ServeHTTP(_ *plugin.Context, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/status" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}

	userID := r.Header.Get("Mattermost-User-Id")
	if userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	win, err := p.windowForUser(userID)
	var status shabbatStatus
	if err == nil {
		status = shabbatStatus{IsShabbat: win.isActive(), Havdalah: win.Havdalah}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status) //nolint:errcheck
}

func main() {
	plugin.ClientMain(&ShabbatPlugin{})
}
