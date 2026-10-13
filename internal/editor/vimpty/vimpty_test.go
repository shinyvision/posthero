package vimpty

import (
	"testing"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/editor/editortest"
)

func start(t *testing.T) editortest.Start {
	if !Available() {
		t.Skip("vim not installed")
	}
	t.Setenv("HOME", t.TempDir()) // no user vimrc: predictable keys
	return func(opts editor.Options, w, h int) (editor.Backend, error) { return Start(opts, w, h) }
}

func TestModal(t *testing.T) { editortest.Modal(t, start(t)) }

func TestMain(t *testing.T)      { editortest.Main(t, start(t)) }
func TestSections(t *testing.T)  { editortest.Sections(t, start(t)) }
func TestSwitching(t *testing.T) { editortest.Switching(t, start(t)) }
func TestQuitting(t *testing.T)  { editortest.Quitting(t, start(t)) }
func TestRewritten(t *testing.T) { editortest.Rewritten(t, start(t)) }
