package workflow

import (
	"context"
	"log/slog"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jordan/go-symphony/internal/model"
)

// Watch watches the workflow file for changes and calls onChange when it is modified.
// It debounces rapid writes by 200ms.
func Watch(ctx context.Context, path string, onChange func(*model.WorkflowDefinition), logger *slog.Logger) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	if err := watcher.Add(path); err != nil {
		watcher.Close()
		return err
	}

	go func() {
		defer watcher.Close()
		var debounce *time.Timer

		for {
			select {
			case <-ctx.Done():
				if debounce != nil {
					debounce.Stop()
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					if debounce != nil {
						debounce.Stop()
					}
					debounce = time.AfterFunc(200*time.Millisecond, func() {
						wf, err := Load(path)
						if err != nil {
							logger.Error("workflow reload failed, keeping last good config",
								"error", err,
								"path", path,
							)
							return
						}
						logger.Info("workflow reloaded", "path", path)
						onChange(wf)
					})
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				logger.Error("workflow watcher error", "error", err)
			}
		}
	}()

	return nil
}
