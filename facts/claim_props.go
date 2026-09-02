package facts

import "encoding/json"

// ClaimProperties is the JSON shape stored in claims.properties for the
// subset of fields that consumers (claim dedup, profiles) care about.
type ClaimProperties struct {
	Details    string `json:"details"`
	ExactQuote string `json:"exact_quote"`
	Amount     string `json:"amount"`
	When       string `json:"when"`
	Conf       string `json:"conf"`
}

// ParseClaimProperties decodes the JSON object stored in claims.properties
// into a ClaimProperties. A malformed payload yields an empty struct rather
// than an error, so callers can treat missing fields as absent.
func ParseClaimProperties(raw string) ClaimProperties {
	var p ClaimProperties
	if raw == "" {
		return p
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p
}
