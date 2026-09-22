// Package paramops 实现"高级参数覆盖"—— 按条件对出站请求体做定点改写。
//
// 背景: 不同上游 provider 对字段的要求千奇百怪(有的要求 max_tokens 必填、
// 有的不吃 temperature、有的要在最后一条消息追加提示语)。靠改代码适配不可持续,
// 所以把改写规则下沉到分组配置里。
//
// 设计取舍:
//   - 纯函数包: 不依赖 gin / DB / settings, 和 apicompat 一样可独立测试。
//   - 出错即整体放弃: 任何一步失败就返回原始 body + error, 绝不给上游发一个
//     改了一半的请求体。半应用的配置比不应用更难排查。
//   - 不自动造结构: 中间层缺失就报错, 规则不会悄悄写出意外形状。
package paramops

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Vars 条件里可用的内置变量, 不需要出现在请求体里。
type Vars struct {
	// Model 重定向后的目标模型(即请求体里当前的 model)。
	Model string
	// UpstreamModel 上游真实模型名。
	UpstreamModel string
	// OriginalModel 重定向前、客户端原始请求的模型名。
	OriginalModel string
}

// Spec 一整套覆盖规则。
type Spec struct {
	Operations []Operation `json:"operations"`
}

// Operation 单条操作。Mode 必填, 其余按 mode 生效。
type Operation struct {
	Mode string `json:"mode"`
	// Path 目标路径, set/delete/append 等定点操作用它。
	Path string `json:"path,omitempty"`
	// From / To 用于 move/copy/replace/regex_replace。
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Value 用于 set/append/prepend/trim_* /ensure_*。
	Value any `json:"value,omitempty"`
	// KeepOrigin set 时已有值则跳过; 对象合并的 append/prepend 也遵循。
	KeepOrigin bool        `json:"keep_origin,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	// Logic 条件间的逻辑关系: "AND"(默认) 或 "OR"。
	Logic string `json:"logic,omitempty"`
}

// Condition 单条条件。
type Condition struct {
	Path string `json:"path"`
	// Mode: full(默认) / prefix / suffix / contains / gt / gte / lt / lte
	Mode           string `json:"mode,omitempty"`
	Value          any    `json:"value,omitempty"`
	Invert         bool   `json:"invert,omitempty"`
	PassMissingKey bool   `json:"pass_missing_key,omitempty"`
}

// ParseSpec 从 JSONMap 解析规则。返回 nil 表示这不是 advanced 模式
// (没有 operations 键), 调用方应退回旧格式处理。
func ParseSpec(raw map[string]any) (*Spec, error) {
	if raw == nil {
		return nil, nil
	}
	opsRaw, ok := raw["operations"]
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(map[string]any{"operations": opsRaw})
	if err != nil {
		return nil, fmt.Errorf("paramops: marshal spec: %w", err)
	}
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("paramops: unmarshal spec: %w", err)
	}
	if len(s.Operations) == 0 {
		return nil, fmt.Errorf("paramops: spec has no operations")
	}
	for i, op := range s.Operations {
		if err := validateOperation(op); err != nil {
			return nil, fmt.Errorf("paramops: operations[%d]: %w", i, err)
		}
	}
	return &s, nil
}

// pathOptionalModes 这些模式用 from/to 表达, 不需要 path。
var pathOptionalModes = map[string]bool{"move": true, "copy": true, "replace": true, "regex_replace": true}

func validateOperation(op Operation) error {
	if op.Mode == "" {
		return fmt.Errorf("mode is required")
	}
	if !knownModes[op.Mode] {
		return fmt.Errorf("unknown mode %q", op.Mode)
	}
	if op.Path == "" && !pathOptionalModes[op.Mode] {
		return fmt.Errorf("mode %q requires path", op.Mode)
	}
	switch op.Mode {
	case "move", "copy":
		// 目标必须是有效路径, 空串没有意义。
		if op.From == "" || op.To == "" {
			return fmt.Errorf("mode %q requires both from and to", op.Mode)
		}
	case "replace", "regex_replace":
		// to 可以为空 —— 用空串替换就是"删掉匹配到的部分"。
		if op.From == "" {
			return fmt.Errorf("mode %q requires from", op.Mode)
		}
	case "trim_prefix", "trim_suffix", "ensure_prefix", "ensure_suffix":
		if _, ok := op.Value.(string); !ok {
			return fmt.Errorf("mode %q requires a string value", op.Mode)
		}
	}
	if op.Logic != "" && op.Logic != "AND" && op.Logic != "OR" {
		return fmt.Errorf("logic must be AND or OR, got %q", op.Logic)
	}
	for i, c := range op.Conditions {
		if err := validateCondition(c); err != nil {
			return fmt.Errorf("conditions[%d]: %w", i, err)
		}
	}
	return nil
}

func validateCondition(c Condition) error {
	if c.Path == "" {
		return fmt.Errorf("path is required")
	}
	switch c.Mode {
	case "", "full", "prefix", "suffix", "contains", "gt", "gte", "lt", "lte":
		return nil
	}
	return fmt.Errorf("unknown condition mode %q", c.Mode)
}

var knownModes = map[string]bool{
	"set": true, "delete": true, "move": true, "copy": true,
	"append": true, "prepend": true,
	"trim_prefix": true, "trim_suffix": true,
	"ensure_prefix": true, "ensure_suffix": true,
	"trim_space": true, "to_lower": true, "to_upper": true,
	"replace": true, "regex_replace": true,
}

// Apply 对 body 应用规则, 返回改写后的 body。
// body 不是 JSON 对象时原样返回。任一步出错返回 error, 调用方应丢弃结果用原 body。
func Apply(body []byte, spec *Spec, vars Vars) ([]byte, error) {
	if spec == nil || len(spec.Operations) == 0 || len(body) == 0 {
		return body, nil
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, fmt.Errorf("paramops: body is not a JSON object: %w", err)
	}
	if root == nil {
		return body, nil
	}

	for i, op := range spec.Operations {
		ok, err := evalConditions(root, op.Conditions, op.Logic, vars)
		if err != nil {
			return body, fmt.Errorf("paramops: operations[%d] condition: %w", i, err)
		}
		if !ok {
			continue
		}
		if err := runOperation(root, op); err != nil {
			return body, fmt.Errorf("paramops: operations[%d] (%s): %w", i, op.Mode, err)
		}
	}

	out, err := json.Marshal(root)
	if err != nil {
		return body, fmt.Errorf("paramops: marshal result: %w", err)
	}
	return out, nil
}

// evalConditions 空条件一律通过。多条件的默认关系是 AND。
func evalConditions(root map[string]any, conds []Condition, logic string, vars Vars) (bool, error) {
	if len(conds) == 0 {
		return true, nil
	}
	and := strings.EqualFold(logic, "AND") || logic == ""
	for _, c := range conds {
		ok, err := evalCondition(root, c, vars)
		if err != nil {
			return false, err
		}
		if and {
			if !ok {
				return false, nil // AND: 一个不满足即整体不满足
			}
			continue
		}
		if ok {
			return true, nil // OR: 一个满足即整体满足
		}
	}
	// 走到这里: AND 表示全部通过; OR 表示全部都没通过。
	return and, nil
}

func evalCondition(root map[string]any, c Condition, vars Vars) (bool, error) {
	got, found := resolveVar(root, c.Path, vars)
	if !found {
		return c.PassMissingKey, nil
	}
	matched, err := matchValue(got, c)
	if err != nil {
		return false, err
	}
	if c.Invert {
		return !matched, nil
	}
	return matched, nil
}

// resolveVar 先查内置变量, 再查请求体路径。
func resolveVar(root map[string]any, path string, vars Vars) (any, bool) {
	switch path {
	case "model":
		if vars.Model != "" {
			return vars.Model, true
		}
	case "upstream_model":
		if vars.UpstreamModel != "" {
			return vars.UpstreamModel, true
		}
	case "original_model":
		if vars.OriginalModel != "" {
			return vars.OriginalModel, true
		}
	}
	return GetPath(root, path)
}

func matchValue(got any, c Condition) (bool, error) {
	mode := c.Mode
	if mode == "" {
		mode = "full"
	}
	switch mode {
	case "gt", "gte", "lt", "lte":
		return matchNumeric(got, c.Value, mode)
	}

	gs, gok := toString(got)
	ws, wok := toString(c.Value)
	if !gok || !wok {
		return false, nil // 类型不匹配视为不匹配, 不算错误
	}
	switch mode {
	case "full":
		return gs == ws, nil
	case "prefix":
		return strings.HasPrefix(gs, ws), nil
	case "suffix":
		return strings.HasSuffix(gs, ws), nil
	case "contains":
		return strings.Contains(gs, ws), nil
	}
	return false, fmt.Errorf("unknown condition mode %q", mode)
}

func matchNumeric(got, want any, mode string) (bool, error) {
	gf, gok := toFloat(got)
	wf, wok := toFloat(want)
	if !gok || !wok {
		return false, nil
	}
	switch mode {
	case "gt":
		return gf > wf, nil
	case "gte":
		return gf >= wf, nil
	case "lt":
		return gf < wf, nil
	case "lte":
		return gf <= wf, nil
	}
	return false, fmt.Errorf("unknown numeric mode %q", mode)
}

func toString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	case nil:
		return "", false
	}
	return "", false
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}

// runOperation 执行单条操作, 就地修改 root。
func runOperation(root map[string]any, op Operation) error {
	switch op.Mode {
	case "set":
		if op.KeepOrigin {
			if _, ok := GetPath(root, op.Path); ok {
				return nil
			}
		}
		return SetPath(root, op.Path, op.Value)

	case "delete":
		DeletePath(root, op.Path)
		return nil

	case "move":
		v, ok := GetPath(root, op.From)
		if !ok {
			return fmt.Errorf("source %q not found", op.From)
		}
		if err := SetPath(root, op.To, v); err != nil {
			return err
		}
		DeletePath(root, op.From)
		return nil

	case "copy":
		v, ok := GetPath(root, op.From)
		if !ok {
			return fmt.Errorf("source %q not found", op.From)
		}
		return SetPath(root, op.To, v)

	case "append", "prepend":
		return runListOp(root, op)

	case "trim_prefix", "trim_suffix", "ensure_prefix", "ensure_suffix",
		"trim_space", "to_lower", "to_upper":
		return runStringOp(root, op)

	case "replace":
		return runReplace(root, op, false)
	case "regex_replace":
		return runReplace(root, op, true)
	}
	return fmt.Errorf("unknown mode %q", op.Mode)
}

func runListOp(root map[string]any, op Operation) error {
	cur, ok := GetPath(root, op.Path)
	if !ok {
		return fmt.Errorf("path %q not found", op.Path)
	}

	// 对象: 合并属性
	if m, isMap := cur.(map[string]any); isMap {
		add, ok := op.Value.(map[string]any)
		if !ok {
			return fmt.Errorf("path %q is an object; value must be an object too", op.Path)
		}
		for k, v := range add {
			if op.KeepOrigin {
				if _, exists := m[k]; exists {
					continue
				}
			}
			m[k] = v
		}
		return nil
	}

	// 数组: 可追加单个元素, 也可追加一个数组(全部元素依次加入)
	if arr, isArr := cur.([]any); isArr {
		var add []any
		if vs, isMulti := op.Value.([]any); isMulti {
			add = vs
		} else {
			add = []any{op.Value}
		}
		var out []any
		if op.Mode == "append" {
			out = append(arr, add...)
		} else {
			out = append(add, arr...)
		}
		return SetPath(root, op.Path, out)
	}

	// 字符串: 拼接
	if s, isStr := cur.(string); isStr {
		ws, ok := toString(op.Value)
		if !ok {
			return fmt.Errorf("path %q is a string; value must be a string", op.Path)
		}
		if op.Mode == "append" {
			return SetPath(root, op.Path, s+ws)
		}
		return SetPath(root, op.Path, ws+s)
	}

	return fmt.Errorf("path %q is %T; append/prepend needs string, array or object", op.Path, cur)
}

func runStringOp(root map[string]any, op Operation) error {
	cur, ok := GetPath(root, op.Path)
	if !ok {
		return fmt.Errorf("path %q not found", op.Path)
	}
	s, isStr := cur.(string)
	if !isStr {
		return fmt.Errorf("path %q is %T; mode %q needs a string", op.Path, cur, op.Mode)
	}
	var out string
	switch op.Mode {
	case "trim_prefix":
		out = strings.TrimPrefix(s, op.Value.(string))
	case "trim_suffix":
		out = strings.TrimSuffix(s, op.Value.(string))
	case "ensure_prefix":
		p := op.Value.(string)
		if strings.HasPrefix(s, p) {
			out = s
		} else {
			out = p + s
		}
	case "ensure_suffix":
		p := op.Value.(string)
		if strings.HasSuffix(s, p) {
			out = s
		} else {
			out = s + p
		}
	case "trim_space":
		out = strings.TrimSpace(s)
	case "to_lower":
		out = strings.ToLower(s)
	case "to_upper":
		out = strings.ToUpper(s)
	}
	return SetPath(root, op.Path, out)
}

func runReplace(root map[string]any, op Operation, regex bool) error {
	cur, ok := GetPath(root, op.Path)
	if !ok {
		return fmt.Errorf("path %q not found", op.Path)
	}
	s, isStr := cur.(string)
	if !isStr {
		return fmt.Errorf("path %q is %T; replace needs a string", op.Path, cur)
	}
	if regex {
		re, err := regexp.Compile(op.From)
		if err != nil {
			return fmt.Errorf("invalid regex %q: %w", op.From, err)
		}
		return SetPath(root, op.Path, re.ReplaceAllString(s, op.To))
	}
	return SetPath(root, op.Path, strings.ReplaceAll(s, op.From, op.To))
}
