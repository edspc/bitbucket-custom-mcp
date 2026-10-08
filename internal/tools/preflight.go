package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func checkRepoPair(source, target string) error {
	if source == "" || target == "" {
		return errors.New("source_repo and target_repo are required")
	}
	if source == target {
		return errors.New("source_repo and target_repo must be different repositories")
	}
	return nil
}

// checkPipelines fails unless every repo has pipelines enabled, naming all that do not.
func (t *tools) checkPipelines(ctx context.Context, repos ...string) error {
	var disabled []string
	for _, repo := range repos {
		enabled, err := t.bb.PipelinesEnabled(ctx, repo)
		if err != nil {
			return err
		}
		if !enabled {
			disabled = append(disabled, repo)
		}
	}
	if len(disabled) > 0 {
		return fmt.Errorf("pipelines are not enabled in %s; enable them in Repository settings > Pipelines > Settings",
			strings.Join(disabled, ", "))
	}
	return nil
}
