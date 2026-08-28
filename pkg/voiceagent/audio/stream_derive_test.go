package audio

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Derive must carry every declared property. This walks streamProperties by
// reflection rather than naming the fields, so a property added later is
// covered without anyone remembering to extend the test.
func TestPCMStreamPropertiesAreDerived(t *testing.T) {
	source := NewPCMStream(context.Background())
	source.SetIncremental(true)
	source.SetStallBudget(3 * time.Second)
	if err := source.SetSampleRate(16000); err != nil {
		t.Fatal(err)
	}

	props := reflect.ValueOf(source.props)
	for i := range props.NumField() {
		if props.Field(i).IsZero() {
			t.Fatalf("streamProperties.%s is still zero: this test cannot prove a property it never set is carried",
				props.Type().Field(i).Name)
		}
	}

	derived := source.Derive(context.Background())
	if !reflect.DeepEqual(source.props, derived.props) {
		t.Fatalf("Derive dropped properties:\n source  = %+v\n derived = %+v", source.props, derived.props)
	}
}

// The guard that makes the struct structural rather than decorative: a new
// per-stream field added to PCMStream directly, instead of to streamProperties,
// is invisible to Derive. Anything not listed here has to be classified.
func TestPCMStreamHasNoUndeclaredProperties(t *testing.T) {
	// Lifecycle and payload: owned by each stream, never derived.
	own := map[string]bool{
		"mu": true, "ctx": true, "props": true,
		"err": true, "closed": true, "written": true,
		"ready": true, "frames": true,
	}
	typ := reflect.TypeOf(PCMStream{})
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if !own[name] {
			t.Errorf("PCMStream.%s is new. If it describes how the stream must be consumed it belongs in "+
				"streamProperties so Derive carries it; if it is this stream's own lifecycle, add it here.", name)
		}
	}
}

// A derived stream is a fresh pipe: it must not inherit the source's frames,
// its terminal error, or its written count.
func TestDeriveCarriesNoPayload(t *testing.T) {
	ctx := context.Background()
	source := NewPCMStream(ctx)
	if err := source.SetSampleRate(24000); err != nil {
		t.Fatal(err)
	}
	if err := source.Write(make([]float32, 8)); err != nil {
		t.Fatal(err)
	}

	derived := source.Derive(ctx)
	if derived.Written() != 0 {
		t.Errorf("derived Written() = %d, want 0", derived.Written())
	}
	if derived.Err() != nil {
		t.Errorf("derived Err() = %v, want nil", derived.Err())
	}
	if len(derived.Frames()) != 0 {
		t.Errorf("derived stream already holds %d frames", len(derived.Frames()))
	}

	// Closing the source with an error must not close the derived stream.
	source.CloseWithError(context.Canceled)
	if derived.Err() != nil {
		t.Errorf("closing the source failed the derived stream: %v", derived.Err())
	}
	if err := derived.Write(make([]float32, 4)); err != nil {
		t.Errorf("derived stream is unusable after the source closed: %v", err)
	}
}

// A source with no rate yet derives without one, and the derived stream can
// still be given one — the NewPCMStream contract, unchanged.
func TestDeriveWithoutASampleRate(t *testing.T) {
	ctx := context.Background()
	derived := NewPCMStream(ctx).Derive(ctx)
	if err := derived.SetSampleRate(48000); err != nil {
		t.Fatalf("SetSampleRate on a rate-less derived stream: %v", err)
	}
	if got, err := derived.WaitReady(ctx); err != nil || got != 48000 {
		t.Fatalf("WaitReady = %d (%v), want 48000", got, err)
	}
}
