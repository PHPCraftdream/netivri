package subscription

import "time"

// Quota is the traffic quota and expiry a provider reports in the
// subscription-userinfo response header. A zero field is unknown.
type Quota struct {
	Upload   int64     `json:"upload"`
	Download int64     `json:"download"`
	Total    int64     `json:"total"`
	Expire   time.Time `json:"expire"`
}
