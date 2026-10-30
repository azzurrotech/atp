// shared/types.go — information model types shared across atp subprojects.
//
// This package defines the core record types (Item, Link, Entity, Insight, etc.)
// that are used by stenella's information model and can be imported by any
// atp sub-project (pod, song, shepherd) for consistent data handling.
//
// The types mirror stenella/model.go but are deliberately independent so they
// can be used without the full stenella platform.
package shared

import "time"

// RecordID is a typed identifier for information records.
type RecordID string

// RecordStatus is the status of a record through its lifecycle.
type RecordStatus string

const (
	// RecordStatusNew indicates a record that has been created but not yet processed.
	RecordStatusNew RecordStatus = "new"
	// RecordStatusDismissed indicates a record that has been dismissed/ignored.
	RecordStatusDismissed RecordStatus = "dismissed"
	// RecordStatusPromoted indicates a record that has been promoted to a higher-level container.
	RecordStatusPromoted RecordStatus = "promoted"
)

// Item is one collected, normalised entry — the atom of the platform.
// This is the shared version of stenella/model.go's Item type.
type Item struct {
	ID           RecordID     `json:"id"`
	SourceID     RecordID     `json:"sourceId"` // source name, the only provenance shown
	Collection   string       `json:"collection"`
	Title        string       `json:"title"`
	Summary      string       `json:"summary,omitempty"`
	Body         string       `json:"body,omitempty"`
	Categories   []string     `json:"categories,omitempty"`
	ACL          string       `json:"acl,omitempty"` // public | private | protected
	LinkHash     string       `json:"linkHash,omitempty"`
	GUIDHash     string       `json:"guidHash,omitempty"`
	ContentHash  string       `json:"contentHash,omitempty"`
	Attributions []string     `json:"attributions,omitempty"` // every source that carried the story
	Entities     []RecordID   `json:"entities,omitempty"`     // entity ids
	Geo          string       `json:"geo,omitempty"`          // "lat,lon" when the item carries coordinates
	Published    time.Time    `json:"published,omitempty"`
	Fetched      time.Time    `json:"fetched,omitempty"`
	DupOf        RecordID     `json:"dupOf,omitempty"` // the item this one collapsed into
	Links        []LinkRef    `json:"links,omitempty"`   // references to other items
}

// LinkRef is a reference between two items.
type LinkRef struct {
	From    RecordID `json:"from"`
	To      RecordID `json:"to"`
	Relation string   `json:"relation"` // supports | contradicts | duplicates | references | related
	Origin  string   `json:"origin"`   // auto | human
	Note    string   `json:"note,omitempty"`
}

// Entity is a person, organisation, place, handle or keyword extracted from items.
type Entity struct {
	ID        RecordID `json:"id"`
	Kind      string   `json:"kind"` // person | org | place | handle | keyword
	Name      string   `json:"name"`
	Count     int      `json:"count"`
	Items     []RecordID `json:"items,omitempty"`
	FirstSeen time.Time `json:"firstSeen,omitempty"`
	LastSeen  time.Time `json:"lastSeen,omitempty"`
}

// Insight is the output of a rule: a claim with its citations.
type Insight struct {
	ID       RecordID `json:"id"`
	Rule     RecordID `json:"rule"`
	Title    string   `json:"title"`
	Text     string   `json:"text"`
	Severity string   `json:"severity"`
	Items    []RecordID `json:"items,omitempty"` // citations: the items that fired the rule
	Sources  []string   `json:"sources,omitempty"`
	Count    int      `json:"count"`
	Status   RecordStatus `json:"status"` // new | dismissed | promoted
	Reason   string     `json:"reason,omitempty"`
	NoteID   RecordID   `json:"noteId,omitempty"`
	Created  time.Time  `json:"created,omitempty"`
	Updated  time.Time  `json:"updated,omitempty"`
}

// Group is a saved query or watchlist: the durable form of "what I am tracking".
type Group struct {
	ID      RecordID `json:"id"`
	Name    string   `json:"name"`
	Query   string   `json:"query,omitempty"` // a metadata query, e.g. "tag:critical source:CBC"
	Tags    []string   `json:"tags,omitempty"`
	Color   string     `json:"color,omitempty"`
	Created time.Time  `json:"created,omitempty"`
}

// Rule is one declarative insight rule. Its conditions are data, not code, so a
// rule can be created, edited and disabled at runtime and re-run over the whole collection.
type Rule struct {
	ID         RecordID `json:"id"`
	Name       string   `json:"name"`
	Text       string   `json:"text"`     // the human sentence the insight states
	Severity   string   `json:"severity"` // info | low | medium | high | critical
	Window     string   `json:"window,omitempty"`
	Enabled    bool     `json:"enabled"`
	Tags       []string   `json:"tags,omitempty"`
	Sources    []string   `json:"sources,omitempty"`
	Entities   []RecordID `json:"entities,omitempty"`
	MatchAll   bool      `json:"matchAll,omitempty"`   // every tag must match, not any
	MinItems   int       `json:"minItems,omitempty"`   // fire only with this many matching items
	MinSources int       `json:"minSources,omitempty"` // …and this many distinct sources
	Created    time.Time  `json:"created,omitempty"`
}

// QueryOptions configures how records are queried from the pod store.
type QueryOptions struct {
	OrderBy string `json:"orderBy,omitempty"`
	Desc    bool   `json:"desc,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// Condition is a predicate for filtering records.
type Condition interface {
	// Apply applies the condition to filter records.
	Apply() bool
}

// podQuery is a helper for building pod queries with the shared types.
type podQuery struct {
	pred string
	op     string
	value  string
	has    bool
}

// PredCond builds a pod condition from the shared types.
func Pred(field, value, op string, has bool) Condition {
	return &podQuery{pred: field, op: op, value: value, has: has}
}

// OpEq is the equality operator.
var OpEq = "eq"

// OpLike is the like/pattern operator.
var OpLike = "like"

// Apply implements the Condition interface.
func (q *podQuery) Apply() bool {
	// This is a placeholder - actual implementation depends on pod.QueryOptions
	return true
}

// BuildQuery builds a query string from a condition.
func BuildQuery(c Condition) string {
	if c == nil {
		return ""
	}
	q, ok := c.(*podQuery)
	if !ok {
		return ""
	}
	if q.has {
		return q.pred + "=" + q.value
	}
	return q.pred + ":" + q.op + ":" + q.value
}

// ItemRef is a lightweight reference to an item, used for links and citations.
type ItemRef struct {
	ID   RecordID `json:"id"`
	Type string   `json:"type"` // "item" | "entity" | "insight"
}

// ShareOptions options for creating shepherd capability tokens.
type ShareOptions struct {
	// Target is the kind and ID of the resource to grant access to.
	Target struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"target"`
	// TTL is the time-to-live for the capability token.
	TTL time.Duration `json:"ttl"`
	// Issuer is the entity requesting the capability.
	Issuer string `json:"issuer"`
	// Subject is the entity receiving the capability.
	Subject string `json:"subject"`
}

// ReferenceEnds returns the "other" end of a link, given one side.
func ReferenceEnds(link LinkRef, knownID RecordID) (other RecordID, kind string) {
	if link.From == knownID {
		return link.To, "item"
	}
	if link.To == knownID {
		return link.From, "item"
	}
	return "", ""
}