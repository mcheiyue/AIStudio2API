package buildapp

import (
	"context"
	"fmt"
)

func (s *Session) LocateLaunch(ctx context.Context) (LaunchHit, error) {
	raw, err := s.cam.EvaluateString(ctx, LaunchHitExpression())
	if err != nil {
		return LaunchHit{}, fmt.Errorf("定位 Launch!: %w", err)
	}
	// 必须用带校验的 ParseLaunchHit：Launch! 尚未渲染时 JS 返回 {"found":false}（零矩形），
	// 若用宽松解析会把 (0,0) 当成有效命中，导致点击落在窗口原点并误报成功、点击循环提前退出。
	return ParseLaunchHit(raw)
}

func (s *Session) LocateLaunchNode(ctx context.Context) (string, error) {
	return s.cam.EvaluateNode(ctx, LaunchNodeExpression())
}

func (s *Session) TrustedClickLaunch(ctx context.Context, sharedID string) error {
	if sharedID == "" {
		return fmt.Errorf("Launch! DOM 节点没有 sharedId")
	}
	if err := s.cam.Activate(ctx); err != nil {
		return fmt.Errorf("激活 Launch! 页面: %w", err)
	}
	if err := s.cam.RestoreClientWindow(ctx); err != nil {
		return fmt.Errorf("恢复 Camoufox client window: %w", err)
	}
	if err := s.cam.PerformElementPointerClick(ctx, sharedID); err != nil {
		return fmt.Errorf("真实点击 Launch!: %w", err)
	}
	if err := s.cam.ReleaseActions(ctx); err != nil {
		return fmt.Errorf("释放鼠标动作: %w", err)
	}
	return nil
}
