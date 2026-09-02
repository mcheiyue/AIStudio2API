package buildapp

import (
	"context"
	"fmt"
)

func (s *Session) AppletShowsRequest(ctx context.Context, method, path string) (bool, error) {
	contexts, err := s.cam.AllContexts(ctx)
	if err != nil {
		return false, err
	}
	marker := fmt.Sprintf("Received request: %s %s", method, path)
	expression := fmt.Sprintf(`document.body ? document.body.innerText.includes(%s) : false`, MustJSON(marker))
	for _, contextID := range contexts {
		result, evalErr := s.cam.EvaluateInContext(ctx, contextID, expression)
		if evalErr != nil {
			continue
		}
		matched, _ := result["value"].(bool)
		if matched {
			return true, nil
		}
	}
	return false, nil
}
