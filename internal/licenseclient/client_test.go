package licenseclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestDefaultClientDoesNotReplayActivationAcrossRedirect(t *testing.T) {
	received := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	store, err := Open(filepath.Join(t.TempDir(), "license.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, nil)
	if _, err := service.Activate(context.Background(), redirector.URL, "BT-SECRET-CODE", "NAS"); err == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
	if received {
		t.Fatal("activation request was replayed to redirect destination")
	}
}

func TestActivateRejectsAmbiguousManagerURL(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "license.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, nil)
	for _, value := range []string{
		"https://user:pass@example.com", "https://example.com?next=other", "https://example.com/#fragment",
	} {
		if _, err := service.Activate(context.Background(), value, "code", "NAS"); err == nil {
			t.Fatalf("accepted unsafe manager URL %q", value)
		}
	}
}
