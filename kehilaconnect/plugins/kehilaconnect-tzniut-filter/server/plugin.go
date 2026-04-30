package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

// TzniutPlugin enforces community modesty standards for KehilaConnect.
type TzniutPlugin struct {
	plugin.MattermostPlugin

	mu              sync.RWMutex
	blockedPatterns []*regexp.Regexp
}

// flaggedItem is stored in the KV store for the moderator dashboard.
type flaggedItem struct {
	PostID    string `json:"post_id"`
	UserID    string `json:"user_id"`
	ChannelID string `json:"channel_id"`
	Reason    string `json:"reason"`
	Reviewed  bool   `json:"reviewed"`
	Approved  bool   `json:"approved"`
}

func (p *TzniutPlugin) pluginConfig() (blockedTerms, imageScanMode, moderatorChannelID string, silentBlock bool) {
	cfg := p.API.GetPluginConfig()

	if v, ok := cfg["blocked_terms"]; ok {
		blockedTerms = fmt.Sprintf("%v", v)
	}
	imageScanMode = "flag"
	if v, ok := cfg["image_scan_mode"]; ok {
		imageScanMode = fmt.Sprintf("%v", v)
	}
	if v, ok := cfg["moderator_channel_id"]; ok {
		moderatorChannelID = fmt.Sprintf("%v", v)
	}
	if v, ok := cfg["silent_block"]; ok {
		silentBlock = fmt.Sprintf("%v", v) == "true"
	}
	return
}

// OnConfigurationChange recompiles blocked term regexps when settings change.
func (p *TzniutPlugin) OnConfigurationChange() error {
	blockedTerms, _, _, _ := p.pluginConfig()
	p.recompilePatterns(blockedTerms)
	return nil
}

func (p *TzniutPlugin) recompilePatterns(terms string) {
	var patterns []*regexp.Regexp
	for _, term := range strings.Split(terms, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		// Wrap in word-boundary anchors for whole-word matching; if the term
		// already contains regex metacharacters it is used as-is.
		pat := `(?i)\b` + regexp.QuoteMeta(term) + `\b`
		if r, err := regexp.Compile(pat); err == nil {
			patterns = append(patterns, r)
		}
	}
	p.mu.Lock()
	p.blockedPatterns = patterns
	p.mu.Unlock()
}

func (p *TzniutPlugin) containsBlockedTerm(text string) (bool, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, re := range p.blockedPatterns {
		if re.MatchString(text) {
			return true, re.String()
		}
	}
	return false, ""
}

func (p *TzniutPlugin) alertModerator(post *model.Post, reason string) {
	_, _, moderatorChannelID, _ := p.pluginConfig()

	if moderatorChannelID == "" {
		return
	}

	user, _ := p.API.GetUser(post.UserId)
	username := post.UserId
	if user != nil {
		username = user.Username
	}

	alert := &model.Post{
		ChannelId: moderatorChannelID,
		Message: fmt.Sprintf(
			":warning: **Tzniut Filter Alert**\n**User:** @%s\n**Channel:** %s\n**Reason:** %s\n**Content:** %s",
			username, post.ChannelId, reason, post.Message,
		),
	}
	p.API.CreatePost(alert) //nolint:errcheck

	// Persist the flagged item for the moderator dashboard.
	item := flaggedItem{
		PostID:    post.Id,
		UserID:    post.UserId,
		ChannelID: post.ChannelId,
		Reason:    reason,
	}
	if data, err := json.Marshal(item); err == nil {
		p.API.KVSet("flagged_"+post.Id, data) //nolint:errcheck
	}
}

// MessageWillBePosted scans the message text against the blocked term list.
func (p *TzniutPlugin) MessageWillBePosted(_ *plugin.Context, post *model.Post) (*model.Post, string) {
	if post.Message == "" {
		return post, ""
	}

	blocked, pattern := p.containsBlockedTerm(post.Message)
	if !blocked {
		return post, ""
	}

	_, _, _, silentBlock := p.pluginConfig()
	p.alertModerator(post, "blocked term: "+pattern)

	if silentBlock {
		return nil, plugin.DismissPostError
	}
	return nil, "Your message was blocked by the community content filter. Please review community guidelines."
}

// MessageWillBeUpdated applies the same check to edited messages.
func (p *TzniutPlugin) MessageWillBeUpdated(_ *plugin.Context, newPost *model.Post, _ *model.Post) (*model.Post, string) {
	return p.MessageWillBePosted(nil, newPost)
}

// FileWillBeUploaded handles image moderation on upload.
func (p *TzniutPlugin) FileWillBeUploaded(_ *plugin.Context, info *model.FileInfo, _ io.Reader, _ io.Writer) (*model.FileInfo, string) {
	_, imageScanMode, moderatorChannelID, _ := p.pluginConfig()

	if imageScanMode == "off" {
		return info, ""
	}

	if !strings.HasPrefix(info.MimeType, "image/") {
		return info, ""
	}

	if imageScanMode == "block" {
		return nil, "Image uploads require moderator approval on this server. Please contact your community administrator."
	}

	// imageScanMode == "flag": allow upload but notify moderator.
	if moderatorChannelID != "" {
		alert := &model.Post{
			ChannelId: moderatorChannelID,
			Message: fmt.Sprintf(
				":frame_with_picture: **Image Upload for Review**\n**File:** %s\n**Type:** %s\n**Uploader:** %s",
				info.Name, info.MimeType, info.CreatorId,
			),
		}
		p.API.CreatePost(alert) //nolint:errcheck
	}

	return info, ""
}

// MessagesWillBeConsumed replaces content of flagged-but-unreviewed posts
// before they are delivered to the client.
func (p *TzniutPlugin) MessagesWillBeConsumed(posts []*model.Post) []*model.Post {
	for _, post := range posts {
		data, appErr := p.API.KVGet("flagged_" + post.Id)
		if appErr != nil || data == nil {
			continue
		}
		var item flaggedItem
		if err := json.Unmarshal(data, &item); err != nil || item.Reviewed {
			continue
		}
		post.Message = "[Removed by community filter — pending moderator review]"
		post.FileIds = nil
	}
	return posts
}

// isModerator returns true when the caller is authorized to access the
// moderation API. Authorization requires either:
//   - system admin role (PermissionManageSystem), OR
//   - explicit membership in the configured moderator channel.
//
// Fix #6: the previous implementation only checked that the caller was
// authenticated (non-empty Mattermost-User-Id). Any authenticated user could
// call /review with approved:false to delete arbitrary posts.
func (p *TzniutPlugin) isModerator(userID string) bool {
	if p.API.HasPermissionTo(userID, model.PermissionManageSystem) {
		return true
	}
	_, _, moderatorChannelID, _ := p.pluginConfig()
	if moderatorChannelID == "" {
		return false
	}
	_, appErr := p.API.GetChannelMember(moderatorChannelID, userID)
	return appErr == nil
}

// ServeHTTP provides the moderator dashboard REST API.
func (p *TzniutPlugin) ServeHTTP(_ *plugin.Context, w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/flagged" && r.Method == http.MethodGet:
		p.handleListFlagged(w, r)
	case r.URL.Path == "/review" && r.Method == http.MethodPost:
		p.handleReview(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *TzniutPlugin) handleListFlagged(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-Id")
	if userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// fix #6: gate on moderator role, not just authentication
	if !p.isModerator(userID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	keys, appErr := p.API.KVList(0, 100)
	if appErr != nil {
		http.Error(w, appErr.Error(), http.StatusInternalServerError)
		return
	}

	var items []flaggedItem
	for _, key := range keys {
		if !strings.HasPrefix(key, "flagged_") {
			continue
		}
		data, err := p.API.KVGet(key)
		if err != nil || data == nil {
			continue
		}
		var item flaggedItem
		if err := json.Unmarshal(data, &item); err == nil {
			items = append(items, item)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items) //nolint:errcheck
}

func (p *TzniutPlugin) handleReview(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("Mattermost-User-Id")
	if userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// fix #6: gate on moderator role before allowing post deletion
	if !p.isModerator(userID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		PostID   string `json:"post_id"`
		Approved bool   `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	key := "flagged_" + req.PostID
	data, appErr := p.API.KVGet(key)
	if appErr != nil || data == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var item flaggedItem
	if err := json.Unmarshal(data, &item); err != nil {
		http.Error(w, "corrupt data", http.StatusInternalServerError)
		return
	}

	item.Reviewed = true
	item.Approved = req.Approved

	if updated, err := json.Marshal(item); err == nil {
		p.API.KVSet(key, updated) //nolint:errcheck
	}

	if !req.Approved {
		p.API.DeletePost(req.PostID) //nolint:errcheck
	}

	w.WriteHeader(http.StatusOK)
}

func main() {
	plugin.ClientMain(&TzniutPlugin{})
}
