package k8splan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rancher/lasso/pkg/client"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

const (
	testPlanNamespace = "fleet-default"
	testPlanSecret    = "plan-1"
)

func TestPlanSecretListOptions(t *testing.T) {
	timeout := int64(300)
	for _, rv := range []string{"", "0", "621343"} {
		opts := metav1.ListOptions{ResourceVersion: rv, TimeoutSeconds: &timeout, AllowWatchBookmarks: true}
		planSecretListOptions(testPlanSecret)(&opts)
		if opts.ResourceVersion != "" {
			t.Errorf("resourceVersion %q: got %q, want it cleared", rv, opts.ResourceVersion)
		}
		if want := "metadata.name=" + testPlanSecret; opts.FieldSelector != want {
			t.Errorf("resourceVersion %q: field selector %q, want %q", rv, opts.FieldSelector, want)
		}
		if opts.TimeoutSeconds != &timeout || !opts.AllowWatchBookmarks {
			t.Errorf("resourceVersion %q: other options changed: %+v", rv, opts)
		}
	}
}

// planSecretAPIServer serves the plan secret to the informer the way kube-apiserver does: a list,
// and watches that each deliver one event and then end, as when the API server closes a watch at
// its timeout or a switchover drops the connection. It records the resourceVersion each list and
// watch request asks for.
type planSecretAPIServer struct {
	t       *testing.T
	mu      sync.Mutex
	rv      int
	lists   []string
	watches []string
}

func (s *planSecretAPIServer) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/version" {
		// lasso's cache factory waits for the API server to answer this before starting informers.
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"major":"1","minor":"33","gitVersion":"v1.33.0"}`))
		return
	}
	query := req.URL.Query()
	if req.URL.Path != "/api/v1/namespaces/"+testPlanNamespace+"/secrets" || query.Get("fieldSelector") != "metadata.name="+testPlanSecret {
		s.t.Errorf("unexpected request %s", req.URL)
		http.NotFound(rw, req)
		return
	}
	watching := query.Get("watch") == "true"

	s.mu.Lock()
	s.rv++
	if watching {
		s.watches = append(s.watches, query.Get("resourceVersion"))
	} else {
		s.lists = append(s.lists, query.Get("resourceVersion"))
	}
	secret := v1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            testPlanSecret,
			Namespace:       testPlanNamespace,
			UID:             "plan-uid",
			ResourceVersion: strconv.Itoa(s.rv),
		},
	}
	s.mu.Unlock()

	rw.Header().Set("Content-Type", "application/json")
	if !watching {
		if err := json.NewEncoder(rw).Encode(v1.SecretList{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"},
			ListMeta: metav1.ListMeta{ResourceVersion: secret.ResourceVersion},
			Items:    []v1.Secret{secret},
		}); err != nil {
			s.t.Errorf("writing list: %v", err)
		}
		return
	}
	object, err := json.Marshal(secret)
	if err != nil {
		s.t.Errorf("encoding secret: %v", err)
		return
	}
	// A watch without a resourceVersion starts with the current object; a resumed one gets the
	// changes after the requested revision.
	eventType := "MODIFIED"
	if query.Get("resourceVersion") == "" {
		eventType = "ADDED"
	}
	if _, err := fmt.Fprintf(rw, `{"type":%q,"object":%s}`+"\n", eventType, object); err != nil {
		s.t.Errorf("writing watch event: %v", err)
	}
	// Returning ends the watch stream.
}

func (s *planSecretAPIServer) requests() (lists, watches []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lists...), append([]string(nil), s.watches...)
}

// TestPlanSecretWatchRestartsFromCurrentState drives the informer the agent uses (lasso cache and
// client-go reflector over the real REST client) and checks the resourceVersion of the requests that
// reach the API server. Without planSecretListOptions clearing it, the first watch would resume from
// the list's resourceVersion and every re-established watch from the last event's, which after a
// switchover belongs to the previous storage.
func TestPlanSecretWatchRestartsFromCurrentState(t *testing.T) {
	apiServer := &planSecretAPIServer{t: t}
	server := httptest.NewServer(apiServer)
	defer server.Close()

	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(v1.SchemeGroupVersion.WithKind("Secret"), meta.RESTScopeNamespace)
	clientFactory, err := client.NewSharedClientFactory(&rest.Config{Host: server.URL}, &client.SharedClientFactoryOptions{Mapper: mapper})
	if err != nil {
		t.Fatal(err)
	}
	cacheFactory := newPlanSecretCacheFactory(clientFactory, testPlanNamespace, testPlanSecret)
	if _, err := cacheFactory.ForKind(v1.SchemeGroupVersion.WithKind("Secret")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cacheFactory.Start(ctx); err != nil {
		t.Fatal(err)
	}

	const wantWatches = 3
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, watches := apiServer.requests(); len(watches) >= wantWatches {
			break
		}
		if time.Now().After(deadline) {
			_, watches := apiServer.requests()
			t.Fatalf("saw %d watch requests, want at least %d", len(watches), wantWatches)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	lists, watches := apiServer.requests()
	if len(lists) == 0 {
		t.Fatal("no list request")
	}
	for i, rv := range lists {
		if rv != "" {
			t.Errorf("list %d asked for resourceVersion %q, want the current state", i, rv)
		}
	}
	for i, rv := range watches {
		if rv != "" {
			t.Errorf("watch %d resumed from resourceVersion %q, want it to start from the current state", i, rv)
		}
	}
}
