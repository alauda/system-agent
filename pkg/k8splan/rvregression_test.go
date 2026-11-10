package k8splan

import (
	"errors"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func liveSecretAt(rv string) func() (*v1.Secret, error) {
	return func() (*v1.Secret, error) {
		return &v1.Secret{ObjectMeta: metav1.ObjectMeta{ResourceVersion: rv}}, nil
	}
}

func TestResourceVersionRegressed(t *testing.T) {
	for _, tc := range []struct {
		name        string
		lastApplied string
		getLive     func() (*v1.Secret, error)
		want        bool
		wantErr     bool
	}{
		{
			name:        "cache lags our own write, server is at that write",
			lastApplied: "209834",
			getLive:     liveSecretAt("209834"),
		},
		{
			name:        "cache lags our own write, server moved on",
			lastApplied: "209834",
			getLive:     liveSecretAt("209900"),
		},
		{
			// Values from AIT-76682: secret copied by etcd-sync to the new primary.
			name:        "server storage changed",
			lastApplied: "209834",
			getLive:     liveSecretAt("171913"),
			want:        true,
		},
		{
			name:        "server read fails, keep rejecting",
			lastApplied: "209834",
			getLive:     func() (*v1.Secret, error) { return nil, errors.New("dial tcp: i/o timeout") },
			wantErr:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := resourceVersionRegressed(tc.lastApplied, tc.getLive)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("regressed = %v, want %v", got, tc.want)
			}
		})
	}
}
