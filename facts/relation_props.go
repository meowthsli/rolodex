package facts

import "encoding/json"

// RelationProperties is the JSON shape stored in relations.properties for the
// subset of fields that consumers (relation dedup, profiles) care about.
type RelationProperties struct {
	Details    string `json:"details"`
	ExactQuote string `json:"exact_quote"`
	Amount     string `json:"amount"`
	When       string `json:"when"`
	Conf       string `json:"conf"`
}

// ParseRelationProperties decodes the JSON object stored in relations.properties
// into a RelationProperties. A malformed payload yields an empty struct rather
// than an error, so callers can treat missing fields as absent.
func ParseRelationProperties(raw string) RelationProperties {
	var p RelationProperties
	if raw == "" {
		return p
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p
}
