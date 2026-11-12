package k8splan

import (
	"fmt"

	"github.com/rancher/lasso/pkg/cache"
	"github.com/rancher/lasso/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newPlanSecretCacheFactory returns the cache factory backing the plan secret informer.
func newPlanSecretCacheFactory(clientFactory client.SharedClientFactory, namespace, secretName string) cache.SharedCacheFactory {
	return cache.NewSharedCachedFactory(clientFactory, &cache.SharedCacheFactoryOptions{
		DefaultNamespace: namespace,
		DefaultTweakList: planSecretListOptions(secretName),
	})
}

// planSecretListOptions limits every list and watch to the plan secret, and starts each of them from
// the API server's current state instead of a resourceVersion the reflector remembered.
//
// The reflector re-establishes a closed watch from the last resourceVersion it observed, and those
// are revisions of the etcd that served them. When the address the agent connects to moves to other
// storage (a Global DR switchover: etcd-sync copies the secret with its UID but under the target
// etcd's revisions; or an etcd restore), resuming asks the new API server for the changes since a
// revision from an unrelated numbering:
//
//   - if the new storage is ahead, its watch cache replays the versions written since that number,
//     including plans that were replaced long ago. In AIT-76767 a host re-ran the clean plan it had
//     received during a rolling update half an hour before the switchover and stopped its kubelet.
//   - if the new storage has passed that number by the time the agent reconnects, the changes in
//     between are skipped, and a plan written then is not seen until the secret changes again.
//
// A watch without a resourceVersion starts with the current object (a synthetic ADDED event) and
// continues with later changes, so nothing older than what the API server holds when the watch
// starts reaches the cache. The handler acts on the latest state and does not need the skipped
// intermediate versions. The cost is one extra delivery of the current object whenever the watch is
// re-established, and a deletion that happens while no watch is open is only observed at the next
// relist. Relists, which would otherwise ask for "not older than" the remembered resourceVersion,
// read the current state as well.
func planSecretListOptions(secretName string) cache.TweakListOptionsFunc {
	return func(options *metav1.ListOptions) {
		options.FieldSelector = fmt.Sprintf("metadata.name=%s", secretName)
		options.ResourceVersion = ""
	}
}
