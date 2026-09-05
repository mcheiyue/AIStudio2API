package buildapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// LaunchHitExpression 返回在顶层 AI Studio 页面定位 Launch! 控件的表达式。
// 该表达式只搜索当前 document 及其 open Shadow DOM，不访问 run.app 子帧。
func LaunchHitExpression() string {
	return `(function(){
  const normalize = value => (value || '').replace(/\s+/g, ' ').trim().toLowerCase();
  const candidates = [];
  const allowed = new Set(['BUTTON', 'A', 'P', 'SPAN', 'DIV']);
  const visible = element => {
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 1 && rect.height > 1 && style.display !== 'none' && style.visibility !== 'hidden';
  };
  const matches = element => {
    if (!allowed.has(element.tagName) && element.getAttribute('role') !== 'button') return false;
    const text = normalize(element.innerText || element.textContent || '');
    return text.includes('launch!') && visible(element);
  };
  const walk = (root, inShadow) => {
    for (const element of root.querySelectorAll('*')) {
      if (matches(element)) {
        const rect = element.getBoundingClientRect();
        candidates.push({
          element,
          text: (element.innerText || element.textContent || '').trim(),
          tag: element.tagName,
          inShadow,
          rect,
          area: rect.width * rect.height,
          exact: normalize(element.innerText || element.textContent || '').endsWith('launch!')
        });
      }
      if (element.shadowRoot) walk(element.shadowRoot, true);
    }
  };
  walk(document, false);
  candidates.sort((left, right) => {
    if (left.exact !== right.exact) return left.exact ? -1 : 1;
    if (left.area !== right.area) return left.area - right.area;
    return left.text.length - right.text.length;
  });
  const hit = candidates[0];
  if (!hit) return JSON.stringify({found:false});
  const {x, y, width, height} = hit.rect;
  return JSON.stringify({found:true,text:hit.text,tag:hit.tag,inShadow:hit.inShadow,x,y,w:width,h:height,cx:x + width / 2,cy:y + height / 2});
})()`
}

// LaunchNodeExpression 返回顶层页面中 Launch! 控件的 DOM 节点。
// 结果由 BiDi 以 sharedId 序列化，供 input.performActions 使用 element-origin。
func LaunchNodeExpression() string {
	return `(function(){
  const normalize = value => (value || '').replace(/\s+/g, ' ').trim().toLowerCase();
  const candidates = [];
  const allowed = new Set(['BUTTON', 'A', 'P', 'SPAN', 'DIV']);
  const visible = element => {
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 1 && rect.height > 1 && style.display !== 'none' && style.visibility !== 'hidden';
  };
  const walk = root => {
    for (const element of root.querySelectorAll('*')) {
      const text = normalize(element.innerText || element.textContent || '');
      if ((allowed.has(element.tagName) || element.getAttribute('role') === 'button') && text.includes('launch!') && visible(element)) {
        const rect = element.getBoundingClientRect();
        candidates.push({element, exact: text.endsWith('launch!'), area: rect.width * rect.height, textLength: text.length});
      }
      if (element.shadowRoot) walk(element.shadowRoot);
    }
  };
  walk(document);
  candidates.sort((left, right) => {
    if (left.exact !== right.exact) return left.exact ? -1 : 1;
    if (left.area !== right.area) return left.area - right.area;
    return left.textLength - right.textLength;
  });
  return candidates[0] ? candidates[0].element : null;
})()`
}

// LaunchHit 是顶层 AI Studio 页面中 Launch! 控件的可点击矩形。
type LaunchHit struct {
	Found    bool    `json:"found"`
	Text     string  `json:"text"`
	Tag      string  `json:"tag"`
	InShadow bool    `json:"inShadow"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	CX       float64 `json:"cx"`
	CY       float64 `json:"cy"`
}

func ParseLaunchHit(raw string) (LaunchHit, error) {
	if strings.TrimSpace(raw) == "" {
		return LaunchHit{}, errors.New("Launch! 定位结果为空")
	}
	var hit LaunchHit
	if err := json.Unmarshal([]byte(raw), &hit); err != nil {
		return LaunchHit{}, fmt.Errorf("解析 Launch! 定位结果: %w", err)
	}
	if !hit.Found {
		return LaunchHit{}, errors.New("未找到 Launch! 控件")
	}
	if hit.W <= 1 || hit.H <= 1 {
		return LaunchHit{}, errors.New("Launch! 控件没有可点击矩形")
	}
	return hit, nil
}
