package editorgate

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestGateLifecycleAndWriterPreference(t *testing.T) {
	gate, err := New()
	if err != nil {
		t.Fatal(err)
	}
	initial := gate.Status()
	decoded, err := base64.RawURLEncoding.DecodeString(initial.InstanceID)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("instance id = %q err=%v", initial.InstanceID, err)
	}
	if initial.Version != 1 || initial.Revision != 0 || initial.Generation != 0 ||
		initial.CompletedGeneration != 0 || initial.Running || initial.LastRun != "" {
		t.Fatalf("initial status = %+v", initial)
	}

	releaseFirstEditor := gate.BeginEditor()
	producerAcquired := make(chan func(), 1)
	go func() {
		release, err := gate.BeginProducer()
		if err != nil {
			producerAcquired <- nil
			return
		}
		producerAcquired <- release
	}()

	deadline := time.Now().Add(time.Second)
	for !gate.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	running := gate.Status()
	if !running.Running || running.Generation != 1 || running.CompletedGeneration != 0 || running.Revision != 1 {
		t.Fatalf("running status = %+v", running)
	}
	if _, err := gate.BeginProducer(); !errors.Is(err, ErrProducerRunning) {
		t.Fatalf("duplicate producer error = %v", err)
	}
	if release, status, ok := gate.BeginStrictEditor(initial.InstanceID, initial.CompletedGeneration); ok || release != nil || !status.Running {
		t.Fatalf("strict editor crossed producer: ok=%v status=%+v", ok, status)
	}

	if release, err := gate.BeginEditorContext(context.Background()); !errors.Is(err, ErrProducerRunning) || release != nil {
		t.Fatalf("editor arriving after producer was not rejected: release=%v err=%v", release != nil, err)
	}
	releaseFirstEditor()
	var releaseProducer func()
	select {
	case releaseProducer = <-producerAcquired:
		if releaseProducer == nil {
			t.Fatal("producer acquisition failed")
		}
	case <-time.After(time.Second):
		t.Fatal("producer did not acquire after editor release")
	}
	releaseProducer()
	releaseSecondEditor, err := gate.BeginEditorContext(context.Background())
	if err != nil {
		t.Fatalf("editor after completed producer was rejected: %v", err)
	}
	releaseSecondEditor()

	completed := gate.Status()
	if completed.Running || completed.Generation != 1 || completed.CompletedGeneration != 1 ||
		completed.Revision != 2 || completed.LastRun == "" {
		t.Fatalf("completed status = %+v", completed)
	}
	if _, err := time.Parse(time.RFC3339Nano, completed.LastRun); err != nil {
		t.Fatalf("lastRun = %q: %v", completed.LastRun, err)
	}
}

func TestDrainRejectsNewProducerWithoutChangingGeneration(t *testing.T) {
	gate := MustNew()
	gate.Drain()
	gate.Drain()
	if _, err := gate.BeginProducer(); !errors.Is(err, ErrDraining) {
		t.Fatalf("producer after drain error = %v", err)
	}
	if status := gate.Status(); status.Generation != 0 || status.Running {
		t.Fatalf("drained gate status = %+v", status)
	}
}

func TestCanceledProducerWaitCompletesPublishedGeneration(t *testing.T) {
	gate := MustNew()
	releaseEditor := gate.BeginEditor()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := gate.BeginProducerContext(ctx)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for !gate.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled producer error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("producer wait ignored cancellation")
	}
	releaseEditor()
	status := gate.Status()
	if status.Running || status.Generation != 1 || status.CompletedGeneration != 1 {
		t.Fatalf("canceled generation status = %+v", status)
	}
}

func TestGateStrictStateFencesRestartAndStaleGeneration(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := New()
	if err != nil {
		t.Fatal(err)
	}
	loaded := first.Status()
	if loaded.InstanceID == second.Status().InstanceID {
		t.Fatal("independent process instances reused an id")
	}
	if release, _, ok := second.BeginStrictEditor(loaded.InstanceID, loaded.CompletedGeneration); ok || release != nil {
		t.Fatal("restart instance state was accepted")
	}
	releaseProducer, err := first.BeginProducer()
	if err != nil {
		t.Fatal(err)
	}
	releaseProducer()
	if release, status, ok := first.BeginStrictEditor(loaded.InstanceID, loaded.CompletedGeneration); ok || release != nil || status.CompletedGeneration != 1 {
		t.Fatalf("stale completed generation accepted: ok=%v status=%+v", ok, status)
	}
	current := first.Status()
	releaseEditor, _, ok := first.BeginStrictEditor(current.InstanceID, current.CompletedGeneration)
	if !ok || releaseEditor == nil {
		t.Fatal("current producer state was rejected")
	}
	releaseEditor()
}

func TestGateCountersNeverExceedJavaScriptSafeInteger(t *testing.T) {
	gate, err := New()
	if err != nil {
		t.Fatal(err)
	}
	gate.mu.Lock()
	gate.status.Generation = MaxSafeCounter - 1
	gate.status.CompletedGeneration = MaxSafeCounter - 1
	gate.status.Revision = MaxSafeCounter - 2
	gate.mu.Unlock()
	release, err := gate.BeginProducer()
	if err != nil {
		t.Fatal(err)
	}
	release()
	status := gate.Status()
	if status.Generation != MaxSafeCounter || status.CompletedGeneration != MaxSafeCounter || status.Revision != MaxSafeCounter {
		t.Fatalf("maximum safe status = %+v", status)
	}
	if _, err := gate.BeginProducer(); err == nil {
		t.Fatal("gate allowed counters to overflow JavaScript's safe range")
	}
	if after := gate.Status(); after != status {
		t.Fatalf("exhausted gate changed status: before=%+v after=%+v", status, after)
	}
}
