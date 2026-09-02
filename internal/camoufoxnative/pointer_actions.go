package camoufoxnative

import (
	"context"
	"errors"
)

func pointerElementClickParams(contextID, sharedID string) map[string]any {
	return map[string]any{
		"context": contextID,
		"actions": []any{
			map[string]any{
				"type":       "pointer",
				"id":         "mouse",
				"parameters": map[string]any{"pointerType": "mouse"},
				"actions": []any{
					map[string]any{
						"type":   "pointerMove",
						"origin": map[string]any{"type": "element", "element": map[string]any{"sharedId": sharedID}},
						"x":      0,
						"y":      0,
					},
					map[string]any{"type": "pointerDown", "button": 0},
					map[string]any{"type": "pointerUp", "button": 0},
				},
			},
		},
	}
}

func (s *Session) Activate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}
	_, err := s.client.command(ctx, "browsingContext.activate", map[string]any{"context": s.contextID})
	return err
}

func (s *Session) PerformElementPointerClick(ctx context.Context, sharedID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}
	_, err := s.client.command(ctx, "input.performActions", pointerElementClickParams(s.contextID, sharedID))
	return err
}

func (s *Session) ReleaseActions(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}
	_, err := s.client.command(ctx, "input.releaseActions", map[string]any{"context": s.contextID})
	return err
}
