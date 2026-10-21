package datastore

import (
	"reflect"
	"testing"
)

func TestHistorySyncSnapshotKeysRoundTrip(t *testing.T) {
	for _, keys := range [][]string{{}, {"movie:tmdb:movie:2", "episode:tmdb:tv:1:s01e02", "movie:tmdb:movie:1"}} {
		body, err := encodeHistorySyncSnapshotKeys(keys)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeHistorySyncSnapshotKeys(body)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{}
		for _, key := range keys {
			want[key] = true
		}
		got := map[string]bool{}
		for _, key := range decoded {
			got[key] = true
		}
		if len(decoded) != len(keys) || !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded %v, want %v", decoded, keys)
		}
	}
}
