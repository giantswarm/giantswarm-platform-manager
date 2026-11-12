# Dex app version prerequisites

The installation record reads the dex-app source from the referenced collection
base. It applies the installation's version patches to that resource kind:
`App.spec.version` or `OCIRepository.spec.ref.tag`. The base can contain several
YAML documents. For an OCI source, the reader follows the referenced collection's
resources and components in [Kustomize's order](https://github.com/kubernetes-sigs/kustomize/blob/master/api/internal/target/kusttarget.go#L190) before applying the installation's components and pins.
Inherited semver selectors therefore take precedence over a tag unless a later
patch clears them. An App patch does not pin an OCIRepository, and vice versa.

Only exact semantic versions reach `DexAppTakes` and the Dex prerequisite checks.
An OCIRepository digest or semver constraint takes precedence over its tag in
[Flux](https://fluxcd.io/flux/components/source/ocirepositories/#reference).
Neither identifies an exact chart version in the Git record, so the reader reports
an error and holds a version-dependent commit instead of treating a tag or a
constraint's lower bound as the running version. Pin `spec.ref.tag` and clear
`spec.ref.semver` and `spec.ref.digest` to
make these prerequisites comparable. This reader does not resolve registry tags
or observe the live Helm release.

Collection replacements, external transformers, legacy patch fields and additional
patch selectors whose effect cannot be established also hold the prerequisite.
