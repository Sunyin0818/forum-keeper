// Package libra talks to the 2libra open API (v1) with an OAuth token.
//
// The API wraps every response in {c,m,d,t}; the client hides that envelope
// and turns non-zero codes into *APIError. Tokens are obtained once with
// --libra-auth, persisted next to the state database and refreshed in place.
package libra

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Token is the persisted OAuth state. RefreshToken rotates on every refresh,
// so the stored copy must always be the newest one.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// User is the public user object embedded in notifications.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
	IsPro     bool   `json:"is_pro"`
}

// Identity is the result of GET /v1/me/summary.
type Identity struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Level    int    `json:"level"`
	IsPro    bool   `json:"is_pro"`
}

// NotificationPayload is the union of the fields the 2libra web client reads
// from a notification. The DTO uses camelCase here, unlike the rest of the
// API; the JSON tags are the contract.
type NotificationPayload struct {
	PostID       string  `json:"postId"`
	PostShortID  string  `json:"postShortId"`
	PostSlug     string  `json:"postSlug"`
	PostNodeSlug string  `json:"postNodeSlug"`
	PostTitle    string  `json:"postTitle"`
	CommentID    string  `json:"commentId"`
	CommentText  string  `json:"commentText"`
	CommentLevel *int    `json:"commentLevel"`
	Target       string  `json:"target"`
	ArticleTitle string  `json:"articleTitle"`
	Path         string  `json:"path"`
	Amount       float64 `json:"amount"`
	RewardType   string  `json:"rewardType"`
	ReactionType string  `json:"reactionType"`
	ContentTitle string  `json:"contentTitle"`
	ContentType  string  `json:"contentType"`
	AliasName    string  `json:"alias_name"`
	AliasAvatar  string  `json:"alias_avatar_url"`
	// FollowerUsername is what the web client shows for a follow event.
	FollowerUsername string `json:"followerUsername"`
	FollowerUserID   string `json:"followerUserId"`

	// Live payloads also carry a human-readable message and the fields below.
	// They are type-specific; the renderer uses whichever is present.
	Message      string          `json:"message"`
	ClientID     string          `json:"clientId"`
	ClientName   string          `json:"clientName"`
	EventCode    string          `json:"event_code"`
	EventName    string          `json:"event_name"`
	EventTitle   string          `json:"event_title"`
	Rarity       string          `json:"rarity"`
	TierCode     string          `json:"tier_code"`
	RolledValue  json.RawMessage `json:"rolled_value"`
	BadgeCode    string          `json:"badge_code"`
	BadgeName    string          `json:"badge_name"`
	BadgeIconURL string          `json:"badge_icon_url"`
	PostNodeName string          `json:"postNodeName"`
	FlatFloor    int             `json:"flatFloor"`
	LocatedFloor int             `json:"locatedFloor"`
	ReplyParent  string          `json:"replyParentId"`
}

// Notification mirrors GET /v1/me/notifications' list entries.
type Notification struct {
	ID        string              `json:"id"`
	Type      string              `json:"type"`
	Payload   NotificationPayload `json:"payload"`
	IsRead    bool                `json:"is_read"`
	CreatedAt string              `json:"created_at"`
	FromUser  *User               `json:"from_user"`
	RelatedID string              `json:"related_id"`
}

// NotificationPage is the paginated notification list. The list field is
// named `list`, not `items`; that inconsistency is part of the API.
type NotificationPage struct {
	List  []Notification `json:"list"`
	Total int            `json:"total"`
	Page  int            `json:"page"`
	Limit int            `json:"limit"`
}

// UnmarshalJSON tolerates a payload that is null or not an object: one odd
// notification must not poison the whole page.
func (n *Notification) UnmarshalJSON(data []byte) error {
	var w struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
		IsRead    bool            `json:"is_read"`
		CreatedAt string          `json:"created_at"`
		FromUser  *User           `json:"from_user"`
		RelatedID string          `json:"related_id"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*n = Notification{
		ID:        w.ID,
		Type:      w.Type,
		IsRead:    w.IsRead,
		CreatedAt: w.CreatedAt,
		FromUser:  w.FromUser,
		RelatedID: w.RelatedID,
	}
	if len(w.Payload) > 0 && w.Payload[0] == '{' {
		if err := json.Unmarshal(w.Payload, &n.Payload); err != nil {
			n.Payload = NotificationPayload{}
		}
	}
	return nil
}

// Created parses created_at, tolerating the millisecond UTC form the API uses.
func (n Notification) Created() time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, n.CreatedAt); err == nil {
			return t
		}
	}
	return time.Time{}
}

// Actor is the name shown next to the action: a masked alias wins over the
// real account, matching how the web client renders anonymous activity.
func (n Notification) Actor() string {
	if n.Payload.AliasName != "" {
		return n.Payload.AliasName
	}
	if n.FromUser != nil && n.FromUser.Username != "" {
		return n.FromUser.Username
	}
	return n.Payload.FollowerUsername
}

// Link builds an absolute 2libra URL for the notification, or "" when the
// payload carries no usable target. Only same-site paths are accepted, so a
// notification can never turn a card button into an off-site link.
func (n Notification) Link(base string) string {
	base = strings.TrimRight(base, "/")
	p := n.Payload
	if p.Path != "" && strings.HasPrefix(p.Path, "/") && !strings.HasPrefix(p.Path, "//") {
		if u, err := url.Parse(base + p.Path); err == nil && u.Host != "" {
			return u.String()
		}
	}
	if p.PostShortID == "" {
		return ""
	}
	node := p.PostNodeSlug
	if node == "" {
		node = "_"
	}
	u := base + "/post/" + url.PathEscape(node) + "/" + url.PathEscape(p.PostShortID)
	if p.CommentID != "" {
		u += "?commentId=" + url.QueryEscape(p.CommentID)
	}
	return u
}

// notificationTypes are the type values the 2libra API emits, taken from the
// web client's label map. Unknown values are still displayed (with the raw
// type as the label), but a filter can only name a known one.
var notificationTypes = map[string]struct{}{
	"reply": {}, "reply_mention": {}, "mention": {}, "reaction": {},
	"system": {}, "pandora": {}, "level_up": {}, "get_reward": {},
	"favorite": {}, "content_check": {}, "badge_unlocked": {}, "follow": {},
	"comment_pin": {},
}

// KnownNotificationType reports whether t is a notification type this client
// knows how to label.
func KnownNotificationType(t string) bool {
	_, ok := notificationTypes[t]
	return ok
}

// ErrUnauthorized means the refresh token can no longer be used. The only fix
// is a new authorization; retrying is pointless.
var ErrUnauthorized = errors.New("libra: authorization is no longer valid; run --libra-auth")

// ErrInsufficientScope means the user revoked one of the granted scopes. Same
// fix as ErrUnauthorized, but a different message in the card.
var ErrInsufficientScope = errors.New("libra: the application is missing a required scope")

// APIError is a non-zero {c, code, m} response.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("libra: HTTP %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("libra: HTTP %d: %s", e.Status, e.Message)
}

// Unwrap maps the two terminal auth failures onto the sentinel errors so
// callers can use errors.Is without matching Chinese message text.
func (e *APIError) Unwrap() error {
	switch {
	case e.Status == 401:
		return ErrUnauthorized
	case e.Code == "insufficient_scope":
		return ErrInsufficientScope
	default:
		return nil
	}
}
