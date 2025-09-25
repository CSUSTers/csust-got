package chat

import (
	"csust-got/config"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

// mockContext is a mock implementation of tb.Context for testing
type mockContext struct {
	chat   *tb.Chat
	sender *tb.User
}

func (m *mockContext) Chat() *tb.Chat {
	return m.chat
}

func (m *mockContext) Sender() *tb.User {
	return m.sender
}

// Add other required methods as no-op implementations
func (m *mockContext) Message() *tb.Message                         { return nil }
func (m *mockContext) Callback() *tb.Callback                       { return nil }
func (m *mockContext) Query() *tb.Query                             { return nil }
func (m *mockContext) InlineResult() *tb.InlineResult               { return nil }
func (m *mockContext) ShippingQuery() *tb.ShippingQuery             { return nil }
func (m *mockContext) PreCheckoutQuery() *tb.PreCheckoutQuery       { return nil }
func (m *mockContext) Poll() *tb.Poll                               { return nil }
func (m *mockContext) PollAnswer() *tb.PollAnswer                   { return nil }
func (m *mockContext) ChatMember() *tb.ChatMemberUpdate             { return nil }
func (m *mockContext) MenuButton() *tb.MenuButton                   { return nil }
func (m *mockContext) BoostUpdated() *tb.BoostUpdated               { return nil }
func (m *mockContext) BoostRemoved() *tb.BoostRemoved               { return nil }
func (m *mockContext) Bot() *tb.Bot                                 { return nil }
func (m *mockContext) Update() tb.Update                            { return tb.Update{} }
func (m *mockContext) Get(key string) interface{}                   { return nil }
func (m *mockContext) Set(key string, val interface{})              {}
func (m *mockContext) Reply(what interface{}, opts ...interface{}) error {
	return nil
}
func (m *mockContext) Accept(opts ...string) error                  { return nil }
func (m *mockContext) Answer(resp *tb.QueryResponse) error          { return nil }
func (m *mockContext) Respond(resp ...*tb.CallbackResponse) error   { return nil }
func (m *mockContext) Notify(action tb.ChatAction) error            { return nil }
func (m *mockContext) Ship(opts ...interface{}) error {
	return nil
}
func (m *mockContext) Checkout(ok bool, opts ...interface{}) error {
	return nil
}
func (m *mockContext) Acknowledge() error {
	return nil
}
func (m *mockContext) Delete() error {
	return nil
}
func (m *mockContext) Send(what interface{}, opts ...interface{}) error {
	return nil
}
func (m *mockContext) Edit(what interface{}, opts ...interface{}) error {
	return nil
}
func (m *mockContext) Forward(msg tb.Editable, opts ...interface{}) error {
	return nil
}
func (m *mockContext) Pin() error {
	return nil
}
func (m *mockContext) Unpin() error {
	return nil
}
func (m *mockContext) StopPoll() (*tb.Poll, error) {
	return nil, nil
}
func (m *mockContext) Leave() error {
	return nil
}
func (m *mockContext) SetAdminRights(rights *tb.Rights) error {
	return nil
}
func (m *mockContext) SetMenuButton(button *tb.MenuButton) error {
	return nil
}
func (m *mockContext) GetMenuButton() (*tb.MenuButton, error) {
	return nil, nil
}
func (m *mockContext) Ban(user *tb.User, opts ...interface{}) error {
	return nil
}
func (m *mockContext) Unban(user *tb.User) error {
	return nil
}
func (m *mockContext) Promote(user *tb.User, rights *tb.Rights) error {
	return nil
}
func (m *mockContext) TransferOwnership(user *tb.User, password string) error {
	return nil
}
func (m *mockContext) InviteLink() (string, error) {
	return "", nil
}
func (m *mockContext) RevokeInviteLink() (string, error) {
	return "", nil
}
func (m *mockContext) SetStickerSet(name string) error {
	return nil
}
func (m *mockContext) DeleteStickerSet() error {
	return nil
}
func (m *mockContext) CreateInviteLink(opts ...interface{}) (*tb.ChatInviteLink, error) {
	return nil, nil
}
func (m *mockContext) EditInviteLink(link string, opts ...interface{}) (*tb.ChatInviteLink, error) {
	return nil, nil
}
func (m *mockContext) RevokeInviteLink2(link string) (*tb.ChatInviteLink, error) {
	return nil, nil
}
func (m *mockContext) ApproveJoinRequest(user *tb.User) error {
	return nil
}
func (m *mockContext) DeclineJoinRequest(user *tb.User) error {
	return nil
}

func (m *mockContext) Args() []string {
	return nil
}

func (m *mockContext) Boost() *tb.BoostUpdated {
	return nil
}

func (m *mockContext) ChatJoinRequest() *tb.ChatJoinRequest {
	return nil
}

func (m *mockContext) Topic() *tb.Topic {
	return nil
}

func (m *mockContext) Migration() (int64, int64) {
	return 0, 0
}

func (m *mockContext) Recipient() tb.Recipient {
	return nil
}

func (m *mockContext) Text() string {
	return ""
}

func (m *mockContext) Entities() tb.Entities {
	return nil
}

func (m *mockContext) Data() string {
	return ""
}

func (m *mockContext) SendAlbum(a tb.Album, opts ...interface{}) error {
	return nil
}

func (m *mockContext) EditOrSend(what interface{}, opts ...interface{}) error {
	return nil
}

func (m *mockContext) EditOrReply(what interface{}, opts ...interface{}) error {
	return nil
}

func (m *mockContext) DeleteAfter(d time.Duration) *time.Timer {
	return nil
}

func (m *mockContext) RespondText(text string) error {
	return nil
}

func (m *mockContext) RespondAlert(text string) error {
	return nil
}

func (m *mockContext) EditCaption(caption string, opts ...interface{}) error {
	return nil
}

func (m *mockContext) ForwardTo(to tb.Recipient, opts ...interface{}) error {
	return nil
}

func TestWhitelistFilter(t *testing.T) {
	// Create a whitelist filter configuration
	filterConfig := &config.ChatFilterConfig{
		Type:      "whitelist",
		Whitelist: []int64{12345, 67890},
	}

	// Create the filter
	filter := newWhitelistFilter(filterConfig)

	// Create mock context with chat ID in whitelist
	ctx1 := &mockContext{
		chat: &tb.Chat{ID: 12345},
		sender: &tb.User{ID: 11111},
	}

	// Create mock context with sender ID in whitelist
	ctx2 := &mockContext{
		chat: &tb.Chat{ID: 54321},
		sender: &tb.User{ID: 67890},
	}

	// Create mock context with neither chat ID nor sender ID in whitelist
	ctx3 := &mockContext{
		chat: &tb.Chat{ID: 54321},
		sender: &tb.User{ID: 11111},
	}

	// Create a dummy chat config
	chatConfig := &config.ChatConfigSingle{}

	// Test ProcessIncoming
	assert.Equal(t, FilterAllow, filter.ProcessIncoming(ctx1, chatConfig), "Expected FilterAllow for chat ID in whitelist")
	assert.Equal(t, FilterAllow, filter.ProcessIncoming(ctx2, chatConfig), "Expected FilterAllow for sender ID in whitelist")
	assert.Equal(t, FilterDeny, filter.ProcessIncoming(ctx3, chatConfig), "Expected FilterDeny for IDs not in whitelist")

	// Test ProcessOutgoing
	response := "test response"
	assert.Equal(t, response, filter.ProcessOutgoing(response, ctx1, chatConfig), "Expected same response from ProcessOutgoing")

	// Test ProcessPromptData
	promptData := &promptData{
		Input: "test input",
	}
	resultData := filter.ProcessPromptData(promptData, ctx1, chatConfig)
	assert.Equal(t, promptData, resultData, "Expected same promptData from ProcessPromptData")

	}

func TestProcessFilters(t *testing.T) {
	// Create a chat configuration with a whitelist filter
	chatConfig := &config.ChatConfigSingle{
		Filters: config.ChatFilterSetting{
			Filters: []config.ChatFilterConfig{
				{
					Type:      "whitelist",
					Whitelist: []int64{12345},
				},
			},
		},
	}

	// Create mock context with chat ID in whitelist
	ctx1 := &mockContext{
		chat: &tb.Chat{ID: 12345},
		sender: &tb.User{ID: 11111},
	}

	// Create mock context with chat ID not in whitelist
	ctx2 := &mockContext{
		chat: &tb.Chat{ID: 54321},
		sender: &tb.User{ID: 11111},
	}

	// Test cases
	assert.Equal(t, FilterAllow, ProcessFilters(ctx1, chatConfig), "Expected FilterAllow for chat ID in whitelist")
	assert.Equal(t, FilterDeny, ProcessFilters(ctx2, chatConfig), "Expected FilterDeny for chat ID not in whitelist")
}