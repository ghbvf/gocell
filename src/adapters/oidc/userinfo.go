package oidc

// UserInfo represents the response from the OIDC UserInfo endpoint.
type UserInfo struct {
	Subject       string `json:"sub"`
	Name          string `json:"name,omitempty"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Picture       string `json:"picture,omitempty"`
	Locale        string `json:"locale,omitempty"`
}
