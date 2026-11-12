# Hub Secret reference guard

A hub plan refuses a commit if its Dex values reference a Secret without a
matching manifest in the same installation's plan or recorded extras. Matching
checks the Secret name and the Dex namespace, `giantswarm`. Recorded resources
are read from the existing portal Secret path and followed through local
kustomization resources and components; encrypted values are never decrypted.
Kustomization namespace settings are respected.

The guard also checks that local resources and components referenced by rendered
kustomizations and includes have a planned or recorded file. A missing or
unreadable file holds the commit. Remote bases keep their existing resolution.

The refusal names the missing reference. Add and include its Secret first, or
render the Secret and its reference together. The comparison still runs and the
single-installation commit and wave use the same refusal. Pull request ordering
continues to put a Secret's repository before the Dex values that reference it.
Presence in Git is not proof that Flux has applied the Secret; rollout readiness
still has to be checked before the values are merged.
