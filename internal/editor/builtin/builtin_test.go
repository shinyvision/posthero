package builtin

import (
	"testing"

	"github.com/shinyvision/posthero/internal/editor"
	"github.com/shinyvision/posthero/internal/editor/editortest"
	"github.com/shinyvision/posthero/internal/vim"
)

func start(opts editor.Options, w, h int) (editor.Backend, error) {
	return Start(opts, vim.NewRegisters(), w, h)
}

func TestModal(t *testing.T) { editortest.Modal(t, start) }

func TestMain(t *testing.T)      { editortest.Main(t, start) }
func TestSections(t *testing.T)  { editortest.Sections(t, start) }
func TestSwitching(t *testing.T) { editortest.Switching(t, start) }
func TestQuitting(t *testing.T)  { editortest.Quitting(t, start) }
func TestRewritten(t *testing.T) { editortest.Rewritten(t, start) }
