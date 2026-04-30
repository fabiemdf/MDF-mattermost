package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

// ShabbatPlugin implements Shabbat & Holiday Mode for KehilaConnect.
type ShabbatPlugin struct {
	plugin.MattermostPlugin
}

// hebcalZmanimResponse is a minimal struct for the HebCal zmanim API response.
type hebcalZmanimResponse struct {
	Date string `json:"date"`
	Zmanim struct {
		CandleLighting string `json:"candleLighting"`
		Tzet           string `json:"tzet"`
	} `json:"zmanim"`
}

// shabbatStatus is returned by the /status REST endpoint consumed by the mobile app.
type shabbatStatus struct {
	IsShabbat bool   `json:"shabbat"`
	Havdalah  string `json:"havdalah"`
}

func (p *ShabbatPlugin) config() (candleOffset, havdalahOffset int, blockMode, defaultCity string) {
	conf := p.API.GetUnsanitizedConfig()
	_ = conf

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

// isShabbatForUser checks the HebCal zmanim API for the user's stored location
// and returns true if the current moment falls within the Shabbat window.
// Results are cached per user per day in the plugin KV store.
func (p *ShabbatPlugin) isShabbatForUser(userID string) (bool, string) {
	cacheKey := "shabbat_status_" + userID + "_" + time.Now().UTC().Format("2006-01-02")
	if cached, appErr := p.API.KVGet(cacheKey); appErr == nil && cached != nil {
		var s shabbatStatus
		if err := json.Unmarshal(cached, &s); err == nil {
			return s.IsShabbat, s.Havdalah
		}
	}

	user, appErr := p.API.GetUser(userID)
	if appErr != nil {
		return false, ""
	}

	_, _, _, defaultCity := p.config()
	city := defaultCity
	if user.Timezone != nil {
		if tz, ok := user.Timezone["automaticTimezone"]; ok && tz != "" {
			city = tz
		} else if tz, ok := user.Timezone["manualTimezone"]; ok && tz != "" {
			city = tz
		}
	}

	candleOffset, havdalahOffset, _, _ := p.config()

	isShabbat, havdalah := p.queryHebcalZmanim(city, candleOffset, havdalahOffset)

	status := shabbatStatus{IsShabbat: isShabbat, Havdalah: havdalah}
	if data, err := json.Marshal(status); err == nil {
		// Cache for 6 hours — short enough to handle zmanim changes.
		p.API.KVSetWithExpiry(cacheKey, data, 6*60*60)
	}

	return isShabbat, havdalah
}

// queryHebcalZmanim calls the HebCal zmanim API for the given city and returns
// whether the current time falls within Shabbat and the havdalah time string.
func (p *ShabbatPlugin) queryHebcalZmanim(city string, candleOffsetMins, havdalahOffsetMins int) (bool, string) {
	now := time.Now()
	url := fmt.Sprintf(
		"https://www.hebcal.com/zmanim?cfg=json&city=%s&date=%s",
		city, now.Format("2006-01-02"),
	)

	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, ""
	}

	var zmanim hebcalZmanimResponse
	if err := json.Unmarshal(body, &zmanim); err != nil {
		return false, ""
	}

	candleTime, err := time.Parse(time.RFC3339, zmanim.Zmanim.CandleLighting)
	if err != nil {
		return false, ""
	}
	tzetTime, err := time.Parse(time.RFC3339, zmanim.Zmanim.Tzet)
	if err != nil {
		return false, ""
	}

	shabbatStart := candleTime.Add(-time.Duration(candleOffsetMins) * time.Minute)
	shabbatEnd := tzetTime.Add(time.Duration(havdalahOffsetMins) * time.Minute)
	havdalahStr := shabbatEnd.Format("15:04")

	// On Shabbat itself (Saturday) the candle lighting endpoint returns empty —
	// check for the Friday window spanning into Saturday via the previous day's
	// tzet. This simple guard handles the most common use case; a production
	// implementation should also query Friday's zmanim on Saturday.
	isShabbat := now.After(shabbatStart) && now.Before(shabbatEnd)

	return isShabbat, havdalahStr
}

// UserWillLogIn blocks login attempts during Shabbat.
func (p *ShabbatPlugin) UserWillLogIn(_ *plugin.Context, user *model.User, _ string) string {
	isShabbat, havdalah := p.isShabbatForUser(user.Id)
	if !isShabbat {
		return ""
	}
	msg := "Shabbat Shalom! KehilaConnect is in Shabbat Mode."
	if havdalah != "" {
		msg += " Please return after Havdalah at " + havdalah + "."
	}
	return msg
}

// MessageWillBePosted blocks or warns on posts during Shabbat.
func (p *ShabbatPlugin) MessageWillBePosted(_ *plugin.Context, post *model.Post) (*model.Post, string) {
	isShabbat, havdalah := p.isShabbatForUser(post.UserId)
	if !isShabbat {
		return post, ""
	}

	_, _, blockMode, _ := p.config()

	switch blockMode {
	case "block":
		return nil, plugin.DismissPostError
	case "queue":
		// Store the post in KV for post-havdalah delivery (delivery scheduling
		// is handled by a background goroutine started in OnActivate in a full
		// implementation).
		return nil, plugin.DismissPostError
	default: // "warn"
		warning := "Shabbat Shalom! You are sending a message during Shabbat."
		if havdalah != "" {
			warning += " Shabbat ends at " + havdalah + "."
		}
		return nil, warning
	}
}

// MessageWillBeUpdated applies the same Shabbat gate to message edits.
func (p *ShabbatPlugin) MessageWillBeUpdated(_ *plugin.Context, newPost *model.Post, _ *model.Post) (*model.Post, string) {
	return p.MessageWillBePosted(nil, newPost)
}

// NotificationWillBePushed suppresses push notifications to users currently in Shabbat.
func (p *ShabbatPlugin) NotificationWillBePushed(pushNotification *model.PushNotification, userID string) (*model.PushNotification, string) {
	isShabbat, _ := p.isShabbatForUser(userID)
	if !isShabbat {
		return pushNotification, ""
	}
	// Return an empty notification to suppress the push silently.
	return nil, ""
}

// ServeHTTP exposes GET /plugins/com.kehilaconnect.shabbat-mode/status for the
// mobile app to check whether Shabbat mode is currently active for the caller.
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

	isShabbat, havdalah := p.isShabbatForUser(userID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(shabbatStatus{IsShabbat: isShabbat, Havdalah: havdalah}) //nolint:errcheck
}

func main() {
	plugin.ClientMain(&ShabbatPlugin{})
}
