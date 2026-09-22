package paramops

import (
	"fmt"
	"strconv"
	"strings"
)

// 路径语法:
//   temperature              根级字段
//   messages.0.content       数组第 1 个元素的 content
//   messages.-1.content      数组最后一个元素的 content
//   metadata.user.name       嵌套对象
//
// 不支持通配和切片 —— 覆盖规则是给"已知结构的请求体"做定点修改,
// 通配会让规则的行为难以预测, 也不好排错。

func splitPath(path string) []string {
	return strings.Split(path, ".")
}

// sliceIndex 把路径段解析成切片下标。-1 表示最后一个元素。
func sliceIndex(seg string, n int) (int, error) {
	i, err := strconv.Atoi(seg)
	if err != nil {
		return 0, fmt.Errorf("path segment %q is not a valid array index", seg)
	}
	if i == -1 {
		i = n - 1
	}
	if i < 0 || i >= n {
		return 0, fmt.Errorf("array index %d out of range (len %d)", i, n)
	}
	return i, nil
}

// descend 从 cur 按 seg 往下走一层。cur 只能是 map 或 slice。
func descend(cur any, seg string) (any, bool) {
	switch c := cur.(type) {
	case map[string]any:
		v, ok := c[seg]
		return v, ok
	case []any:
		i, err := sliceIndex(seg, len(c))
		if err != nil {
			return nil, false
		}
		return c[i], true
	case []map[string]any:
		i, err := sliceIndex(seg, len(c))
		if err != nil {
			return nil, false
		}
		return c[i], true
	case []string:
		i, err := sliceIndex(seg, len(c))
		if err != nil {
			return nil, false
		}
		return c[i], true
	}
	return nil, false
}

// GetPath 取 path 处的值。找不到返回 ok=false。
func GetPath(root map[string]any, path string) (any, bool) {
	if root == nil || path == "" {
		return nil, false
	}
	segs := splitPath(path)
	var cur any = root
	for _, s := range segs {
		next, ok := descend(cur, s)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// resolveParent 走到 path 的**父容器**,并返回最后一段。
// 中间层缺失或类型不对时报错 —— 不自动造结构, 免得规则悄悄写出意外形状。
func resolveParent(root map[string]any, path string) (any, string, error) {
	segs := splitPath(path)
	if len(segs) == 0 {
		return nil, "", fmt.Errorf("empty path")
	}
	var cur any = root
	for _, s := range segs[:len(segs)-1] {
		next, ok := descend(cur, s)
		if !ok {
			return nil, "", fmt.Errorf("path %q: segment %q not found", path, s)
		}
		cur = next
	}
	return cur, segs[len(segs)-1], nil
}

// SetPath 把 path 处设为 v。父容器必须已存在且类型正确。
func SetPath(root map[string]any, path string, v any) error {
	parent, last, err := resolveParent(root, path)
	if err != nil {
		return err
	}
	switch p := parent.(type) {
	case map[string]any:
		p[last] = v
		return nil
	case []any:
		i, err := sliceIndex(last, len(p))
		if err != nil {
			return fmt.Errorf("path %q: %w", path, err)
		}
		p[i] = v
		return nil
	case []map[string]any:
		i, err := sliceIndex(last, len(p))
		if err != nil {
			return fmt.Errorf("path %q: %w", path, err)
		}
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("path %q: element type is map, got %T", path, v)
		}
		p[i] = m
		return nil
	case []string:
		i, err := sliceIndex(last, len(p))
		if err != nil {
			return fmt.Errorf("path %q: %w", path, err)
		}
		p[i] = fmt.Sprint(v)
		return nil
	}
	return fmt.Errorf("path %q: parent is %T, cannot set field", path, parent)
}

// DeletePath 删除 path 处的值。不存在返回 false。
func DeletePath(root map[string]any, path string) bool {
	parent, last, err := resolveParent(root, path)
	if err != nil {
		return false
	}
	switch p := parent.(type) {
	case map[string]any:
		if _, ok := p[last]; !ok {
			return false
		}
		delete(p, last)
		return true
	case []any:
		i, err := sliceIndex(last, len(p))
		if err != nil {
			return false
		}
		copy(p[i:], p[i+1:])
		p[len(p)-1] = nil
		return true
	case []map[string]any:
		i, err := sliceIndex(last, len(p))
		if err != nil {
			return false
		}
		copy(p[i:], p[i+1:])
		p[len(p)-1] = nil
		return true
	}
	return false
}
