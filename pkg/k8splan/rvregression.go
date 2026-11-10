package k8splan

import v1 "k8s.io/api/core/v1"

// resourceVersionRegressed is consulted when the informer delivers the plan secret with a
// resourceVersion lower than the one returned by the agent's own last write.
//
// On a single storage backend that only happens while the informer cache lags behind that write:
// a quorum read of the object returns at least lastApplied, and the delivery must be dropped as
// before. If the API server itself reports a lower resourceVersion, it is now backed by different
// storage (a Global DR switchover through etcd-sync copies the secret with its UID but under the
// target etcd's revisions; an etcd restore rewinds them), so lastApplied belongs to a revision
// space that no longer exists and has to be discarded. Otherwise every later plan is rejected
// until the new storage's revision happens to overtake the old value.
//
// A failed read returns the error and regressed=false so the caller keeps the original behaviour.
func resourceVersionRegressed(lastApplied string, getLive func() (*v1.Secret, error)) (regressed bool, liveRV string, err error) {
	live, err := getLive()
	if err != nil {
		return false, "", err
	}
	return toInt(live.ResourceVersion) < toInt(lastApplied), live.ResourceVersion, nil
}
