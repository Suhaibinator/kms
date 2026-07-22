package configkms

import (
	"testing"
	"time"

	fixtureconfig "github.com/Suhaibinator/kms/internal/configstorefixture/config"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

var (
	benchmarkSnapshot        Snapshot
	benchmarkView            DatabaseHealthView
	benchmarkPersistenceView PersistenceHandlerView
	benchmarkDuration        time.Duration
	benchmarkInteger         int
	benchmarkEndpoint        fixtureconfig.Endpoint
	benchmarkSecret          kmsclient.Secret
)

func getterFixture() (*Store, Snapshot) {
	configuration := fixtureconfig.Defaults()
	configuration.Password = kmsclient.NewSecret([]byte("benchmark-password"))
	configuration.RuntimeToken = kmsclient.NewSecret([]byte("benchmark-token"))
	store := &Store{}
	store.active.Store(&immutableGeneration{config: configuration})
	return store, store.Current()
}

func TestScalarReadPathAllocations(t *testing.T) {
	store, snapshot := getterFixture()
	view := snapshot.DatabaseHealth()

	if got := testing.AllocsPerRun(1_000, func() { benchmarkSnapshot = store.Current() }); got != 0 {
		t.Fatalf("Current allocations = %v, want 0", got)
	}
	if got := testing.AllocsPerRun(1_000, func() { benchmarkView = snapshot.DatabaseHealth() }); got != 0 {
		t.Fatalf("view construction allocations = %v, want 0", got)
	}
	if got := testing.AllocsPerRun(1_000, func() { benchmarkView = store.Current().DatabaseHealth() }); got != 0 {
		t.Fatalf("Current plus view capture allocations = %v, want 0", got)
	}
	if got := testing.AllocsPerRun(1_000, func() {
		benchmarkDuration = view.Timeout()
		benchmarkInteger = view.MaxOpen()
	}); got != 0 {
		t.Fatalf("scalar getter allocations = %v, want 0", got)
	}
	if got := testing.AllocsPerRun(100, func() { benchmarkEndpoint = view.Endpoint() }); got == 0 {
		t.Fatal("composite getter unexpectedly returned mutable storage without allocating a clone")
	}
	if got := testing.AllocsPerRun(100, func() { benchmarkSecret = snapshot.PersistenceHandler().Password() }); got == 0 {
		t.Fatal("secret getter unexpectedly returned plaintext storage without allocating a clone")
	}
}

func BenchmarkCurrent(b *testing.B) {
	store, _ := getterFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkSnapshot = store.Current()
	}
}

func BenchmarkViewCapture(b *testing.B) {
	store, _ := getterFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkPersistenceView = store.Current().PersistenceHandler()
	}
}

func BenchmarkMaxOpenGetter(b *testing.B) {
	_, snapshot := getterFixture()
	view := snapshot.DatabaseHealth()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkInteger = view.MaxOpen()
	}
}

func BenchmarkTimeoutGetter(b *testing.B) {
	_, snapshot := getterFixture()
	view := snapshot.DatabaseHealth()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkDuration = view.Timeout()
	}
}

func BenchmarkCompositeGetter(b *testing.B) {
	_, snapshot := getterFixture()
	view := snapshot.DatabaseHealth()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkEndpoint = view.Endpoint()
	}
}

func BenchmarkSecretGetter(b *testing.B) {
	_, snapshot := getterFixture()
	view := snapshot.PersistenceHandler()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		benchmarkSecret = view.Password()
	}
}
