package v2ex

import (
	"encoding/json"
	"io"
)

// Member is the subset of a V2EX member we care about.
type Member struct {
	ID           int    `json:"id"`
	Username     string `json:"username"`
	URL          string `json:"url"`
	Tagline      string `json:"tagline"`
	AvatarNormal string `json:"avatar_normal"`
	AvatarLarge  string `json:"avatar_large"`
}

// Notification mirrors the objects returned by GET /notifications.
type Notification struct {
	ID              int    `json:"id"`
	MemberID        int    `json:"member_id"`
	ForMemberID     int    `json:"for_member_id"`
	Text            string `json:"text"`
	Payload         string `json:"payload"`
	PayloadRendered string `json:"payload_rendered"`
	Created         int64  `json:"created"`
	Member          Member `json:"member"`
}

// Topic mirrors the objects returned by GET /topics/:id.
type Topic struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

func decodeJSON(r io.Reader, out any) error {
	return json.NewDecoder(r).Decode(out)
}
