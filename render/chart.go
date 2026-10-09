package render

// ReleaseTagFilter is the tag filter a definition patches onto an
// OCIRepository beside a range that admits release candidates: stable releases
// and release candidates only. Giant Swarm's charts push their branch builds to
// the same repository with a pre-release of their own
// (X.Y.Z-r<hash>t<time>h<sha>), which the range alone would select.
const ReleaseTagFilter = `^v?[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`
