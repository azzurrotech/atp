module azzurrotech/atp

go 1.22

// atp is the orchestrator: it embeds song, pod and shepherd in-process so it
// can expose every one of their endpoints through a single API surface and
// enforce per-client scoping, usage logging and billing. All three are our
// own standard-library-only modules (no external dependencies anywhere).
require (
	azzurrotech/pod v0.0.0
	azzurrotech/shepherd v0.0.0
	azzurrotech/song v0.0.0
)

replace (
	azzurrotech/pod => ./pod
	azzurrotech/shepherd => ./shepherd
	azzurrotech/song => ./song
)