package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	modChannelID = "mod-channel-001"
	sysAdminID   = "sysadmin-user-001"
	modMemberID  = "mod-member-user-001"
	plainUserID  = "plain-user-001"
)

// newTzniutPlugin wires up a TzniutPlugin with a mocked API.
func newTzniutPlugin(api *plugintest.API) *TzniutPlugin {
	p := &TzniutPlugin{}
	p.SetAPI(api)
	return p
}

// defaultTzniutConfig returns a config map with the moderator channel set.
func defaultTzniutConfig() map[string]interface{} {
	return map[string]interface{}{
		"blocked_terms":        "",
		"image_scan_mode":      "flag",
		"moderator_channel_id": modChannelID,
		"silent_block":         "false",
	}
}

// ============================================================
// isModerator — fix #6 (authorization gate)
// ============================================================

// TestIsModerator_SystemAdmin verifies that a user with PermissionManageSystem
// is recognized as a moderator even if not in the moderator channel.
func TestIsModerator_SystemAdmin(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultTzniutConfig())
	api.On("HasPermissionTo", sysAdminID, model.PermissionManageSystem).Return(true)

	p := newTzniutPlugin(api)
	assert.True(t, p.isModerator(sysAdminID))
	api.AssertExpectations(t)
}

// TestIsModerator_ChannelMember verifies that a non-admin user who is a member
// of the moderator channel is granted access.
func TestIsModerator_ChannelMember(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultTzniutConfig())
	api.On("HasPermissionTo", modMemberID, model.PermissionManageSystem).Return(false)
	api.On("GetChannelMember", modChannelID, modMemberID).
		Return(&model.ChannelMember{ChannelId: modChannelID, UserId: modMemberID}, (*model.AppError)(nil))

	p := newTzniutPlugin(api)
	assert.True(t, p.isModerator(modMemberID))
	api.AssertExpectations(t)
}

// TestIsModerator_PlainUser verifies that an ordinary authenticated user
// (neither system admin nor channel member) is denied access.
func TestIsModerator_PlainUser(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultTzniutConfig())
	api.On("HasPermissionTo", plainUserID, model.PermissionManageSystem).Return(false)
	api.On("GetChannelMember", modChannelID, plainUserID).
		Return((*model.ChannelMember)(nil), model.NewAppError("GetChannelMember", "not_found", nil, "", http.StatusNotFound))

	p := newTzniutPlugin(api)
	assert.False(t, p.isModerator(plainUserID))
	api.AssertExpectations(t)
}

// TestIsModerator_NoModeratorChannelConfigured verifies that when no moderator
// channel is set, only system admins are granted access.
func TestIsModerator_NoModeratorChannelConfigured(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(map[string]interface{}{
		"blocked_terms":        "",
		"image_scan_mode":      "flag",
		"moderator_channel_id": "",
		"silent_block":         "false",
	})
	api.On("HasPermissionTo", plainUserID, model.PermissionManageSystem).Return(false)

	p := newTzniutPlugin(api)
	assert.False(t, p.isModerator(plainUserID),
		"without a moderator channel, non-admin must be denied")
	// GetChannelMember must NOT be called when channel ID is empty.
	api.AssertNotCalled(t, "GetChannelMember", mock.Anything, mock.Anything)
}

// ============================================================
// handleReview — fix #6 (403 for non-moderator)
// ============================================================

// httpRecorder wraps httptest.NewRecorder and serves as a convenience helper.
func doReviewRequest(t *testing.T, p *TzniutPlugin, callerUserID string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/review", bytes.NewReader(b))
	req.Header.Set("Mattermost-User-Id", callerUserID)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	p.ServeHTTP(&plugin.Context{}, rr, req)
	return rr
}

// TestHandleReview_ForbiddenForPlainUser verifies fix #6: a non-moderator
// receives 403 Forbidden and DeletePost is never called.
func TestHandleReview_ForbiddenForPlainUser(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultTzniutConfig())
	api.On("HasPermissionTo", plainUserID, model.PermissionManageSystem).Return(false)
	api.On("GetChannelMember", modChannelID, plainUserID).
		Return((*model.ChannelMember)(nil), model.NewAppError("", "not_found", nil, "", http.StatusNotFound))

	p := newTzniutPlugin(api)
	rr := doReviewRequest(t, p, plainUserID, map[string]interface{}{
		"post_id":  "post-abc",
		"approved": false,
	})

	assert.Equal(t, http.StatusForbidden, rr.Code)
	api.AssertNotCalled(t, "KVGet", mock.Anything)
	api.AssertNotCalled(t, "DeletePost", mock.Anything)
}

// TestHandleReview_AllowedForSystemAdmin verifies that a system admin can
// reject (delete) a flagged post via /review.
func TestHandleReview_AllowedForSystemAdmin(t *testing.T) {
	item := flaggedItem{PostID: "post-abc", UserID: "author", ChannelID: "chan1", Reason: "blocked term"}
	itemData, _ := json.Marshal(item)

	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultTzniutConfig())
	api.On("HasPermissionTo", sysAdminID, model.PermissionManageSystem).Return(true)
	api.On("KVGet", "flagged_post-abc").Return(itemData, (*model.AppError)(nil))
	api.On("KVSet", "flagged_post-abc", mock.Anything).Return((*model.AppError)(nil))
	api.On("DeletePost", "post-abc").Return((*model.AppError)(nil))

	p := newTzniutPlugin(api)
	rr := doReviewRequest(t, p, sysAdminID, map[string]interface{}{
		"post_id":  "post-abc",
		"approved": false,
	})

	assert.Equal(t, http.StatusOK, rr.Code)
	api.AssertCalled(t, "DeletePost", "post-abc")
}

// TestHandleReview_Unauthenticated verifies that a request without a
// Mattermost-User-Id header returns 401.
func TestHandleReview_Unauthenticated(t *testing.T) {
	api := &plugintest.API{}
	p := newTzniutPlugin(api)

	req := httptest.NewRequest(http.MethodPost, "/review", bytes.NewReader([]byte(`{}`)))
	rr := httptest.NewRecorder()
	p.ServeHTTP(&plugin.Context{}, rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ============================================================
// handleListFlagged — fix #6 (403 for non-moderator)
// ============================================================

func TestHandleListFlagged_ForbiddenForPlainUser(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultTzniutConfig())
	api.On("HasPermissionTo", plainUserID, model.PermissionManageSystem).Return(false)
	api.On("GetChannelMember", modChannelID, plainUserID).
		Return((*model.ChannelMember)(nil), model.NewAppError("", "not_found", nil, "", http.StatusNotFound))

	p := newTzniutPlugin(api)

	req := httptest.NewRequest(http.MethodGet, "/flagged", nil)
	req.Header.Set("Mattermost-User-Id", plainUserID)
	rr := httptest.NewRecorder()
	p.ServeHTTP(&plugin.Context{}, rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	api.AssertNotCalled(t, "KVList", mock.Anything, mock.Anything)
}

// ============================================================
// MessageWillBePosted — content filtering
// ============================================================

// TestMessageWillBePosted_BlockedTerm_RejectsPost verifies that a message
// containing a blocked term is rejected and the moderator is alerted.
func TestMessageWillBePosted_BlockedTerm_RejectsPost(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(map[string]interface{}{
		"blocked_terms":        "badword",
		"image_scan_mode":      "flag",
		"moderator_channel_id": modChannelID,
		"silent_block":         "false",
	})
	api.On("GetUser", "user1").Return(&model.User{Id: "user1", Username: "testuser"}, (*model.AppError)(nil))
	api.On("CreatePost", mock.Anything).Return(&model.Post{}, (*model.AppError)(nil))
	api.On("KVSet", mock.Anything, mock.Anything).Return((*model.AppError)(nil))

	p := newTzniutPlugin(api)
	p.recompilePatterns("badword")

	post := &model.Post{Id: "post1", UserId: "user1", ChannelId: "chan1", Message: "this is a badword test"}
	returned, errStr := p.MessageWillBePosted(&plugin.Context{}, post)

	assert.Nil(t, returned)
	assert.NotEmpty(t, errStr)
	api.AssertCalled(t, "CreatePost", mock.Anything) // moderator alert posted
}

// TestMessageWillBePosted_CleanMessage_PassesThrough confirms an unblocked
// message is returned unchanged.
func TestMessageWillBePosted_CleanMessage_PassesThrough(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(defaultTzniutConfig())

	p := newTzniutPlugin(api)
	p.recompilePatterns("badword")

	post := &model.Post{UserId: "user1", ChannelId: "chan1", Message: "Shabbat Shalom everyone!"}
	returned, errStr := p.MessageWillBePosted(&plugin.Context{}, post)

	assert.Equal(t, post, returned)
	assert.Empty(t, errStr)
}

// TestMessageWillBePosted_SilentBlock_DismissesWithoutError confirms that
// silent_block=true uses DismissPostError (no user-visible message).
func TestMessageWillBePosted_SilentBlock_DismissesWithoutError(t *testing.T) {
	api := &plugintest.API{}
	api.On("GetPluginConfig").Return(map[string]interface{}{
		"blocked_terms":        "secret",
		"image_scan_mode":      "off",
		"moderator_channel_id": "",
		"silent_block":         "true",
	})
	api.On("GetUser", "user1").Return(&model.User{Id: "user1", Username: "u"}, (*model.AppError)(nil))
	api.On("KVSet", mock.Anything, mock.Anything).Return((*model.AppError)(nil))

	p := newTzniutPlugin(api)
	p.recompilePatterns("secret")

	post := &model.Post{UserId: "user1", ChannelId: "chan1", Message: "my secret plan"}
	returned, errStr := p.MessageWillBePosted(&plugin.Context{}, post)

	assert.Nil(t, returned)
	assert.Equal(t, plugin.DismissPostError, errStr)
}
