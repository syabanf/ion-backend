package domain

import (
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"
)

func TestExtractMentions_HappyPath(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"none", "no mentions here", nil},
		{"single", "Please look @alice when you can", []string{"alice"}},
		{"multiple", "ping @noc_engineer and @field.tech please", []string{"noc_engineer", "field.tech"}},
		{"start of string", "@bob hello", []string{"bob"}},
		{"dedupe + case", "@Alice and @alice should dedupe", []string{"alice"}},
		{"trailing punct", "hey @charlie, fix this", []string{"charlie"}},
		{"do not match email", "send to user@example.com", nil},
		{"hyphen and dot", "ping @noc-lead.shift1", []string{"noc-lead.shift1"}},
		{"underscores ok", "@_invalid", nil}, // must start with a letter
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractMentions(tc.body)
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ExtractMentions(%q) = %v, want %v", tc.body, got, want)
			}
		})
	}
}

func TestNewComment_Validation(t *testing.T) {
	ticketID := uuid.New()
	authorID := uuid.New()
	cases := []struct {
		name      string
		ticketID  uuid.UUID
		authorID  uuid.UUID
		body      string
		wantOK    bool
		wantCode  string
	}{
		{"ok", ticketID, authorID, "hello", true, ""},
		{"missing ticket", uuid.Nil, authorID, "x", false, "cs.comment.ticket_required"},
		{"missing author", ticketID, uuid.Nil, "x", false, "cs.comment.author_required"},
		{"empty body", ticketID, authorID, "   ", false, "cs.comment.body_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewComment(tc.ticketID, tc.authorID, "cs_agent", tc.body, false, nil)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %q", tc.wantCode)
			}
			if got := errCodeOf(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

func TestCommentEdit_StampsEditedAt(t *testing.T) {
	c, err := NewComment(uuid.New(), uuid.New(), "cs_agent", "original", false, nil)
	if err != nil {
		t.Fatalf("NewComment: %v", err)
	}
	if c.EditedAt != nil {
		t.Fatal("EditedAt should be nil pre-edit")
	}
	if err := c.Edit("updated body"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if c.EditedAt == nil {
		t.Fatal("EditedAt should be set post-edit")
	}
	if c.Body != "updated body" {
		t.Fatalf("body = %q want %q", c.Body, "updated body")
	}
}

func TestCommentDelete_SoftDeletes(t *testing.T) {
	c, _ := NewComment(uuid.New(), uuid.New(), "cs_agent", "original", false, nil)
	c.Delete()
	if c.DeletedAt == nil {
		t.Fatal("DeletedAt should be set after Delete")
	}
	// Edit should refuse a deleted comment
	if err := c.Edit("nope"); err == nil {
		t.Fatal("expected Edit to refuse on deleted comment")
	}
}

func TestNewMention_Validation(t *testing.T) {
	m, err := NewMention(uuid.New(), uuid.New(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("NewMention happy path: %v", err)
	}
	if !m.IsUnread() {
		t.Fatal("fresh mention should be unread")
	}

	if _, err := NewMention(uuid.Nil, uuid.New(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("expected error on missing ticket id")
	}
	if _, err := NewMention(uuid.New(), uuid.New(), uuid.Nil, uuid.New()); err == nil {
		t.Fatal("expected error on missing mentioned_user_id")
	}
	if _, err := NewMention(uuid.New(), uuid.New(), uuid.New(), uuid.Nil); err == nil {
		t.Fatal("expected error on missing mentioned_by_user_id")
	}
}

func TestNewChannel_Validation(t *testing.T) {
	c, err := NewChannel("portal", "Portal", ChannelKindInbound, true, nil)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	if !c.IsInbound() {
		t.Fatal("portal should be inbound")
	}

	if _, err := NewChannel("", "Portal", ChannelKindInbound, true, nil); err == nil {
		t.Fatal("expected error on empty code")
	}
	if _, err := NewChannel("portal", "", ChannelKindInbound, true, nil); err == nil {
		t.Fatal("expected error on empty name")
	}
	if _, err := NewChannel("portal", "Portal", ChannelKind("xxx"), true, nil); err == nil {
		t.Fatal("expected error on bad kind")
	}
}

func TestChannelUpdate_PartialApply(t *testing.T) {
	c, _ := NewChannel("portal", "Portal", ChannelKindInbound, true, nil)
	newName := "Customer Portal"
	disabled := false
	if err := c.Update(&newName, nil, &disabled, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if c.Name != newName {
		t.Fatalf("name = %q want %q", c.Name, newName)
	}
	if c.IsActive {
		t.Fatal("expected is_active=false after update")
	}
	if c.Kind != ChannelKindInbound {
		t.Fatalf("kind changed unexpectedly: %v", c.Kind)
	}
}
