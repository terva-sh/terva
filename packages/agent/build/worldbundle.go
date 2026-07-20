package build

// The saved-World bundle (Worlds W5b): one JSON document that carries a World
// OFF this machine — the WorldDoc plus every roster character's card, each
// embedded as its ordinary export form (a CCv2 PNG when the card has an
// avatar, else the CCv2 JSON), plus the cover image. Import is therefore just
// cards.import per embedded card (idempotent by content) followed by an
// old-ref→new-id remap of the roster — no second card format, no archive.
//
// The bundle's world.id is provenance only: import ALWAYS mints a fresh id,
// so a bundle can never collide with (or silently overwrite) a local World.

// WorldBundleSpec identifies a World bundle document.
const WorldBundleSpec = "terva.world_bundle.v1"

// WorldBundle is the export/import document.
type WorldBundle struct {
	Spec  string   `json:"spec"`
	World WorldDoc `json:"world"`
	// Cards carries the roster's character cards. Bytes is a complete card
	// file (PNG or JSON — Mime says which), base64 in the JSON encoding.
	Cards []BundleCard `json:"cards,omitempty"`
	// Cover is the World's cover image (PNG), when it has one.
	Cover []byte `json:"cover,omitempty"`
}

// BundleCard is one embedded character card. Ref is the card's library id in
// the EXPORTING library — import remaps roster entries from it to the id the
// local import assigns. Name is display-only redundancy for humans reading
// the file.
type BundleCard struct {
	Ref   string `json:"ref"`
	Name  string `json:"name,omitempty"`
	Mime  string `json:"mime_type"`
	Bytes []byte `json:"bytes"`
}
