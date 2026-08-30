// Package profiles pre-computes and serves read-only entity profile documents:
// a long-form text snapshot of a single canonical entity and its surrounding
// knowledge graph, rendered for display (e.g. in the dashboard). Production code
// reads from the same sqlite database as the facts package and reuses facts'
// entity model.
package profiles

import (
	"database/sql"
	"fmt"
	"html"
	"log"
	"strings"
	"time"

	sq "github.com/bokwoon95/sq"
	facts "meo.ru/rolodex/facts"
	utils "meo.ru/rolodex/facts/utils"
)

// ENTITY_PROFILES describes the entity_profiles table: one pre-computed
// long-text document per canonical entity. The profile is a denormalized
// snapshot of the entity and its graph, rebuilt whenever the graph changes.
type ENTITY_PROFILES struct {
	sq.TableStruct
	EntityID  sq.NumberField `sq:"entity_id"`
	Profile   sq.StringField `sq:"profile"`
	UpdatedAt sq.TimeField   `sq:"updated_at"`
}

var EP = sq.New[ENTITY_PROFILES]("ep")

// EntityProfile is the Go model for a row in entity_profiles.
type EntityProfile struct {
	EntityID  int
	Profile   string
	UpdatedAt time.Time
}

// EntityProfileMapper scans a row from entity_profiles into an EntityProfile.
func EntityProfileMapper(row *sq.Row) EntityProfile {
	var p EntityProfile
	p.EntityID = row.Int("entity_id")
	p.Profile = row.String("profile")
	p.UpdatedAt = row.Time("updated_at")
	return p
}

// profileRelation is the per-relation data used to render a profile section.
// Direction records how the entity participates ("outgoing" = entity is the
// source, "incoming" = entity is the target).
type profileRelation struct {
	Type       string   // relation type, e.g. FOUNDED, EMPLOYED_AT
	Other      string   // the display name of the counterpart entity
	OtherTypes []string // types of the counterpart entity (for coloring)
	Direction  string   // "outgoing" or "incoming"
	Details    string   // human-written details from relation properties
	Quote      string   // exact_quote from relation properties
	Amount     string   // amount from relation properties
	When       string   // when from relation properties
	Confidence string   // confidence measure from relation properties
	SourceURL  string   // page URL the relation was extracted from
	ChunkText  string   // full text of the pass chunk the relation came from
}

// ProfilesRepository pre-computes and stores entity profile documents.
type ProfilesRepository struct {
	db *sql.DB
}

// NewProfilesRepository creates a profiles repository backed by the database.
func NewProfilesRepository(db *sql.DB) *ProfilesRepository {
	return &ProfilesRepository{db: db}
}

// GetProfile loads a stored profile for an entity. found is false when the
// entity has no profile row yet (not yet built).
func (r *ProfilesRepository) GetProfile(entityID int) (EntityProfile, bool, error) {
	p, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM entity_profiles WHERE entity_id = {}", entityID), EntityProfileMapper)
	if err != nil {
		if err == sql.ErrNoRows {
			return EntityProfile{}, false, nil
		}
		return EntityProfile{}, false, err
	}
	return p, true, nil
}

// ListProfiles returns every stored profile ordered by entity id, plus the
// display name of each entity (for rendering in a dashboard).
func (r *ProfilesRepository) ListProfiles() ([]struct {
	EntityID  int
	Name      string
	Profile   string
	UpdatedAt time.Time
}, error) {
	return sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT ep.entity_id, e.display_name AS name, ep.profile, ep.updated_at "+
			"FROM entity_profiles ep JOIN entities e ON e.id = ep.entity_id "+
			"ORDER BY ep.entity_id"),
		func(row *sq.Row) struct {
			EntityID  int
			Name      string
			Profile   string
			UpdatedAt time.Time
		} {
			return struct {
				EntityID  int
				Name      string
				Profile   string
				UpdatedAt time.Time
			}{EntityID: row.Int("entity_id"), Name: row.String("name"),
				Profile: row.String("profile"), UpdatedAt: row.Time("updated_at")}
		})
}

// DeleteProfile removes the stored profile for an entity, if any.
func (r *ProfilesRepository) DeleteProfile(entityID int) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"DELETE FROM entity_profiles WHERE entity_id = {}", entityID))
	return err
}

// SaveProfile upserts the rendered profile document for an entity and bumps
// its updated_at timestamp.
func (r *ProfilesRepository) SaveProfile(entityID int, profile string) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO entity_profiles (entity_id, profile, updated_at) VALUES ({}, {}, CURRENT_TIMESTAMP) "+
			"ON CONFLICT(entity_id) DO UPDATE SET profile = excluded.profile, updated_at = CURRENT_TIMESTAMP",
		entityID, profile))
	return err
}

// RebuildProfile re-renders and stores a single entity's profile from the
// current graph and returns the rendered document. It is a no-op that returns
// an empty string if the entity no longer exists.
func (r *ProfilesRepository) RebuildProfile(entityID int) (string, error) {
	text, err := r.BuildProfile(entityID)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", nil
	}
	if err := r.SaveProfile(entityID, text); err != nil {
		return "", err
	}
	return text, nil
}

// RebuildAll renders and stores a profile for every entity in the graph,
// returning the number rebuilt. It is safe to re-run; profiles that already
// reflect the current graph are simply overwritten.
func (r *ProfilesRepository) RebuildAll() (int, error) {
	ents, err := sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT id FROM entities"), func(row *sq.Row) int { return row.Int("id") })
	if err != nil {
		return 0, err
	}
	built := 0
	for _, id := range ents {
		text, rerr := r.RebuildProfile(id)
		if rerr != nil {
			log.Printf("rebuild profile entity %d: %v", id, rerr)
			continue
		}
		if text != "" {
			built++
		}
	}
	return built, nil
}

// BuildProfile renders the long-text profile document for one entity without
// persisting it. It returns an empty string when the entity does not exist.
// The document describes the entity (types, known flag, aliases) and then every
// relation it participates in, written in prose with the supporting exact quote
// and the source page URL where the fact was extracted.
func (r *ProfilesRepository) BuildProfile(entityID int) (string, error) {
	ent, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM entities WHERE id = {}", entityID), facts.EntityMapper)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}

	aliases, err := sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT DISTINCT raw_name FROM entity_aliases WHERE entity_id = {} ORDER BY raw_name",
		entityID), func(row *sq.Row) string { return row.String("raw_name") })
	if err != nil {
		return "", err
	}

	rels, err := r.profileRelations(entityID)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("# " + colorName(ent.DisplayName, ent.Types) + "\n\n")
	if len(ent.Types) > 0 {
		b.WriteString("**Types:** " + strings.Join(ent.Types, ", ") + "\n")
	}
	if ent.IsKnown {
		b.WriteString("**Known:** yes\n")
	}

	if len(aliases) > 0 {
		seen := make(map[string]bool)
		var uniq []string
		for _, a := range aliases {
			if a == ent.DisplayName || seen[a] {
				continue
			}
			seen[a] = true
			uniq = append(uniq, a)
		}
		if len(uniq) > 0 {
			b.WriteString("**Известно также:** ")
			b.WriteString(strings.Join(uniq, ", "))
			b.WriteString("\n")
		}
	}

	var outgoing, incoming []profileRelation
	for _, rel := range rels {
		if rel.Direction == "outgoing" {
			outgoing = append(outgoing, rel)
		} else {
			incoming = append(incoming, rel)
		}
	}

	var fn footnoteCollector
	if len(outgoing) > 0 {
		b.WriteString("\n## Что происходило\n")
		for _, rel := range outgoing {
			renderRelationSection(&b, ent.DisplayName, ent.Types, rel, &fn)
		}
	}
	if len(incoming) > 0 {
		b.WriteString("\n## Также важно\n")
		for _, rel := range incoming {
			renderRelationSection(&b, ent.DisplayName, ent.Types, rel, &fn)
		}
	}
	if len(outgoing) == 0 && len(incoming) == 0 {
		b.WriteString("\n*Связей не зарегистрировано*\n")
	}
	fn.render(&b)
	return b.String(), nil
}

// profileRelations loads every relation the entity participates in (as source
// or target) with the counterpart name and the source page URL, in one query.
func (r *ProfilesRepository) profileRelations(entityID int) ([]profileRelation, error) {
	rows, err := sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT r.id, r.type, r.properties, r.confidence, r.source_id, r.target_id, r.link_id, "+
			"CASE WHEN r.source_id = {} THEN 'outgoing' ELSE 'incoming' END AS direction, "+
			"CASE WHEN r.source_id = {} THEN t.display_name ELSE s.display_name END AS other, "+
			"CASE WHEN r.source_id = {} THEN t.types ELSE s.types END AS other_types, "+
			"lq.url AS src_url, p.chunk_text AS chunk_text "+
			"FROM relations r "+
			"JOIN entities s ON s.id = r.source_id "+
			"JOIN entities t ON t.id = r.target_id "+
			"LEFT JOIN link_queue lq ON lq.id = r.link_id "+
			"LEFT JOIN passes p ON p.id = r.pass_id "+
			"WHERE r.source_id = {} OR r.target_id = {} "+
			"ORDER BY r.id", entityID, entityID, entityID, entityID, entityID),
		func(row *sq.Row) profileRelation {
			var p profileRelation
			p.Type = row.String("type")
			p.Other = row.String("other")
			p.OtherTypes = utils.UnmarshalTypes(row.String("other_types"))
			p.Direction = row.String("direction")
			p.SourceURL = row.String("src_url")
			p.ChunkText = row.String("chunk_text")
			props := facts.ParseRelationProperties(row.String("properties"))
			p.Details = props.Details
			p.Quote = props.ExactQuote
			p.Amount = props.Amount
			p.When = props.When
			p.Confidence = props.Conf
			return p
		})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// renderRelationSection appends one relation to the profile as a short prose
// block: the relationship, the details, and (when present) the exact quote and
// the source page the fact was taken from. Entity names are colored by type —
// a Person name is red, any other type is green — so they stand out in the
// rendered profile.
func renderRelationSection(b *strings.Builder, self string, selfTypes []string, rel profileRelation, fn *footnoteCollector) {
	// Humanize: "Alice Основание Acme", with each entity name colored by its own
	// type and the relation type as a neutral noun.
	relNoun := relationTypeNoun(rel.Type)
	var sentence string
	if rel.Direction == "outgoing" {
		sentence = colorName(self, selfTypes) + " " + relNoun + " " + colorName(rel.Other, rel.OtherTypes)
	} else {
		sentence = colorName(rel.Other, rel.OtherTypes) + " " + relNoun + " " + colorName(self, selfTypes)
	}
	b.WriteString("\n### ")
	b.WriteString(sentence)
	b.WriteString("\n")
	if rel.Details != "" {
		b.WriteString(rel.Details)
		b.WriteString("\n")
	}
	// When, amount and confidence (plus the source page) are grouped together
	// inside one set of parentheses, so the metadata reads as a single compact
	// attribution line.
	var extras []string
	if rel.When != "" && rel.When != "~" {
		extras = append(extras, "when: "+rel.When)
	}
	if rel.Amount != "" && rel.Amount != "~" {
		extras = append(extras, "amount: "+rel.Amount)
	}
	if rel.Confidence != "" {
		extras = append(extras, "confidence: "+rel.Confidence)
	}
	if rel.SourceURL != "" {
		extras = append(extras, "source"+fn.reference(rel.ChunkText)+": "+rel.SourceURL)
	}
	if len(extras) > 0 {
		b.WriteString("(")
		b.WriteString(strings.Join(extras, " · "))
		b.WriteString(")\n")
	}
	if rel.Quote != "" {
		b.WriteString("\n> “")
		b.WriteString(rel.Quote)
		b.WriteString("”\n")
	}
}

// footnoteItem is one unique pass chunk rendered as a numbered footnote.
type footnoteItem struct {
	id    int
	chunk string
}

// footnoteCollector accumulates the distinct pass chunks referenced by a
// profile's relations and renders them as numbered footnotes. Identical chunks
// map to a single footnote, so a repeated chunk reuses the same reference.
type footnoteCollector struct {
	byChunk map[string]int // chunk text -> footnote id (1-based)
	items   []footnoteItem // ordered unique footnotes
}

// reference returns the inline footnote reference for a chunk, registering the
// chunk (and assigning it the next footnote id) on first use. An empty chunk
// yields nothing.
func (f *footnoteCollector) reference(chunk string) string {
	if chunk == "" {
		return ""
	}
	if id, ok := f.byChunk[chunk]; ok {
		return fmt.Sprintf(`<sup><a href="#fn-%d" id="fnref-%d">[%d]</a></sup>`, id, id, id)
	}
	id := len(f.items) + 1
	if f.byChunk == nil {
		f.byChunk = make(map[string]int)
	}
	f.byChunk[chunk] = id
	f.items = append(f.items, footnoteItem{id, chunk})
	return fmt.Sprintf(`<sup><a href="#fn-%d" id="fnref-%d">[%d]</a></sup>`, id, id, id)
}

// render appends the collected footnotes as an HTML ordered list. Each entry is
// anchored so clicking a reference jumps to it, and a back-link returns from the
// footnote to its first reference.
func (f *footnoteCollector) render(b *strings.Builder) {
	if len(f.items) == 0 {
		return
	}
	b.WriteString("\n## Источники\n")
	b.WriteString("<ol>\n")
	for _, it := range f.items {
		fmt.Fprintf(b, "<li id=\"fn-%d\">%s <a href=\"#fnref-%d\" title=\"back\">↩</a></li>\n",
			it.id, html.EscapeString(it.chunk), it.id)
	}
	b.WriteString("</ol>\n")
}

// colorName wraps an entity name in an HTML span colored by its type: a Person
// renders red, any other type renders green. Standard Markdown has no color
// support, so the profile document carries this inline HTML and the dashboard
// renders it with unsafe_allow_html. The name is HTML-escaped to avoid breaking
// the span.
func colorName(name string, types []string) string {
	color := "#2e7d32" // green: default for non-Person entities
	for _, t := range types {
		if strings.EqualFold(t, "Person") {
			color = "#d33" // red: Person entities
			break
		}
	}
	return "<span style='color:" + color + "'>" + htmlEscape(name) + "</span>"
}

// htmlEscape escapes <, >, & and quotes in a display name so it is safe to
// place inside an inline HTML span.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// relationTypeNoun maps a raw relation type (e.g. "INVESTED_IN", "FOUNDED") to
// a neutral Russian noun so the profile prose reads naturally ("Name Инвестиции
// Other"). Unknown types fall back to the raw token so nothing is ever hidden.
func relationTypeNoun(typ string) string {
	switch typ {
	case "FOUNDED", "FOUNDED/COFOUNDED":
		return "Основание"
	case "COFOUNDED":
		return "Сооснование"
	case "ESTABLISHED_IN":
		return "Создание"
	case "INVESTED_IN":
		return "Инвестиции"
	case "SEEDED":
		return "Посевные инвестиции"
	case "EMPLOYED_AT":
		return "Сотрудничество/вовлечение"
	case "VALUED":
		return "Оценка"
	case "ACQUIRED":
		return "Приобретение"
	case "SOLD":
		return "Продажа"
	case "LAUNCHED":
		return "Запуск"
	default:
		return typ
	}
}
