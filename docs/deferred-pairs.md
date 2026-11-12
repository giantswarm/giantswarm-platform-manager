# Deferred shared values

An enable or reconcile plan only creates a new paired credential when both
installations are in the planned set. If neither side is on record and the other
installation is outside that set, the plan reports the pair under `deferredPairs`
with its files and the reason. The definition omits the pair and its consumers,
including Secret references and kustomization entries, from both the dry run and
the commit render. Other pairs in the wave can still be created together. The
Action records the deferred names so its rollout checks the committed configuration.

Existing values are kept. If the peer already holds the value, the person must
supply it as before. An existing pair's rotation, an explicitly requested draw,
an unreadable peer, or an unresolved installation is never deferred. These retain
their refusal or supply requirements. A full verification comparison still shows
the definition's complete desired state, including pairs deferred by an earlier
commit.
