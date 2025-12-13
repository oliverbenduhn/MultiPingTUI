package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/sedwards2009/smidgen"
)

func RunConfigEditor(path string) error {
	if err := ensureConfigFile(path); err != nil {
		return err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	app := tview.NewApplication()
	tview.DoubleClickInterval = 0
	app.EnableMouse(true)

	status := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	status.SetBorder(false)

	setStatus := func(msg string) {
		status.SetText(fmt.Sprintf("[yellow]mping config[-]  Ctrl+S save · Esc close  |  %s", msg))
	}
	setStatus("ready")

	colorscheme, _ := smidgen.LoadInternalColorscheme("monokai")
	buffer := smidgen.NewBufferFromString(string(content), path)
	editor := smidgen.NewView(app, buffer)
	if colorscheme != nil {
		editor.SetColorscheme(colorscheme)
	}

	editor.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyCtrlS:
			candidate, err := parseUserSettings(buffer.Bytes())
			if err == nil {
				err = ValidateUserSettings(candidate)
			}
			if err != nil {
				setStatus("invalid config: " + err.Error())
				return nil
			}
			if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
				setStatus("save failed: " + err.Error())
				return nil
			}
			setStatus("saved at " + time.Now().Format("15:04:05"))
			return nil
		case tcell.KeyEscape:
			app.Stop()
			return nil
		}
		return event
	})

	root := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(status, 1, 0, false).
		AddItem(editor, 0, 1, true)

	app.SetRoot(root, true)
	app.SetFocus(editor)
	return app.Run()
}

func ensureConfigFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Default config file.
	return os.WriteFile(path, marshalUserSettingsYAML(DefaultUserSettings()), 0o600)
}
