package services

import (
	"context"
	"fmt"
	"strings"

	app_errors "autogateway/internal/errors"
	"autogateway/internal/models"

	"gorm.io/gorm"
)

// ReservedAliases is the fixed list of aliases used by the smart "auto"
// routing path. They are seeded as placeholder rows (group_id=0) so the
// frontend always shows them, and the alias CRUD service refuses to
// rename / delete them.
//
// Names mirror router_engine.Tier (simple/medium/complex) — the historical
// "auto-*" prefix was dropped in v1.x because the smart-routing trigger is
// the model == "auto" keyword, not the alias name itself. Existing rows
// are renamed on startup (see EnsureReservedSeeded).
var ReservedAliases = []string{"simple", "medium", "complex"}

// legacyReservedRenameMap maps old reserved alias names to new ones so we
// can rename DB rows in place on startup, preserving any user-attached
// (group_id, real_model) candidates.
var legacyReservedRenameMap = map[string]string{
	"auto-simple":  "simple",
	"auto-medium":  "medium",
	"auto-complex": "complex",
}

// defaultAliasPriority 是候选 priority 的缺省值。数值越小越优先, 但在这个
// 维度上 priority 只作 SWRR 累加值打平时的 tie-break (见 router_engine.swrr),
// 所以缺省值统一取 100 —— 与模型列的 gorm default 一致。
const defaultAliasPriority = 100

// AliasService manages CRUD on model_aliases plus seeding of reserved
// aliases. Routing decisions live in internal/router_engine — this
// service is purely the persistence layer.
type AliasService struct {
	db *gorm.DB
}

func NewAliasService(db *gorm.DB) *AliasService {
	return &AliasService{db: db}
}

// EnsureReservedSeeded inserts the three reserved aliases as placeholder
// rows if they are missing, and renames any legacy "auto-*" rows to the
// new short names so user-attached candidates survive the rename.
// Called once on app start.
func (s *AliasService) EnsureReservedSeeded(ctx context.Context) error {
	// 1. Migrate legacy "auto-{tier}" rows (placeholder + user-attached) to
	//    the new short names. Doing this before seeding avoids duplicate
	//    placeholders when both old and new exist.
	for oldName, newName := range legacyReservedRenameMap {
		// If a new-name placeholder already exists, drop the old placeholder
		// (group_id=0) outright but still rename any user-attached rows.
		var newPlaceholderCount int64
		if err := s.db.WithContext(ctx).Model(&models.ModelAlias{}).
			Where("alias = ? AND is_reserved = ? AND group_id = 0", newName, true).
			Count(&newPlaceholderCount).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if newPlaceholderCount > 0 {
			if err := s.db.WithContext(ctx).
				Where("alias = ? AND is_reserved = ? AND group_id = 0", oldName, true).
				Delete(&models.ModelAlias{}).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
		}
		// Rename remaining rows (placeholder OR user-attached) in place.
		if err := s.db.WithContext(ctx).Model(&models.ModelAlias{}).
			Where("alias = ?", oldName).
			Update("alias", newName).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
	}

	// 2. Seed missing placeholders.
	for _, name := range ReservedAliases {
		var count int64
		if err := s.db.WithContext(ctx).Model(&models.ModelAlias{}).
			Where("alias = ? AND is_reserved = ?", name, true).
			Count(&count).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
		if count > 0 {
			continue
		}
		seed := &models.ModelAlias{
			Alias:      name,
			GroupID:    0,
			RealModel:  "",
			Weight:     1,
			Priority:   100,
			Enabled:    true,
			IsReserved: true,
		}
		if err := s.db.WithContext(ctx).Create(seed).Error; err != nil {
			return app_errors.ParseDBError(err)
		}
	}
	return nil
}

// ListAll returns every row, sorted alias asc, weight desc, priority asc.
func (s *AliasService) ListAll(ctx context.Context) ([]models.ModelAlias, error) {
	var rows []models.ModelAlias
	if err := s.db.WithContext(ctx).
		Order("alias asc, weight desc, priority asc, id asc").
		Find(&rows).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return rows, nil
}

// ListEnabledAliasNames returns distinct user-callable alias names. Placeholder
// rows have group_id=0 and are intentionally excluded; an alias appears only
// after it has at least one enabled destination candidate.
func (s *AliasService) ListEnabledAliasNames(ctx context.Context) ([]string, error) {
	var names []string
	if err := s.db.WithContext(ctx).
		Model(&models.ModelAlias{}).
		Distinct("alias").
		Where("enabled = ? AND group_id <> 0 AND alias <> ''", true).
		Order("alias asc").
		Pluck("alias", &names).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return names, nil
}

// ListByAlias returns the candidate pool for a given alias (excludes the
// reserved placeholder row with group_id=0).
func (s *AliasService) ListByAlias(ctx context.Context, alias string) ([]models.ModelAlias, error) {
	var rows []models.ModelAlias
	if err := s.db.WithContext(ctx).
		Where("alias = ? AND group_id <> 0", alias).
		Order("weight desc, priority asc, id asc").
		Find(&rows).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return rows, nil
}

// ListEnabledByAlias 是 P4 智能路由专用 -- 只返回 enabled=true 的候选,
// 按 SWRR 排序参数 (weight desc, priority asc) 返回. proxy 入口收到
// model name 后查这个判断是否走 alias 路径.
func (s *AliasService) ListEnabledByAlias(ctx context.Context, alias string) ([]models.ModelAlias, error) {
	var rows []models.ModelAlias
	if err := s.db.WithContext(ctx).
		Where("alias = ? AND group_id <> 0 AND enabled = ?", alias, true).
		Order("weight desc, priority asc, id asc").
		Find(&rows).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return rows, nil
}

// CreateRequest is the JSON payload for POST /api/aliases.
type AliasCreateRequest struct {
	Alias     string `json:"alias"`
	GroupID   uint   `json:"group_id"`
	RealModel string `json:"real_model"`
	Weight    int    `json:"weight"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
}

func (s *AliasService) Create(ctx context.Context, req AliasCreateRequest) (*models.ModelAlias, error) {
	return s.createOnDB(ctx, s.db, req)
}

// CreateMany inserts every request atomically in a single transaction. On
// the first failure the entire batch is rolled back — no partial state. The
// returned error is an *errors.APIError (Code is stable, Message is human
// readable and identifies which candidate failed).
func (s *AliasService) CreateMany(ctx context.Context, reqs []AliasCreateRequest) ([]models.ModelAlias, error) {
	if len(reqs) == 0 {
		return nil, app_errors.NewAPIError(app_errors.ErrBadRequest, "no candidates")
	}
	rows := make([]models.ModelAlias, 0, len(reqs))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i, req := range reqs {
			row, err := s.createOnDB(ctx, tx, req)
			if err != nil {
				// Wrap the APIError so the client can show which candidate broke
				// the batch without leaking raw DB error text.
				if apiErr, ok := err.(*app_errors.APIError); ok {
					return app_errors.NewAPIError(apiErr,
						fmt.Sprintf("candidate %d (%s → %s in group %d): %s",
							i+1, req.Alias, req.RealModel, req.GroupID, apiErr.Message))
				}
				return err
			}
			rows = append(rows, *row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// createOnDB is the shared implementation used by Create (s.db) and CreateMany
// (tx). It must not start its own transaction.
func (s *AliasService) createOnDB(ctx context.Context, db *gorm.DB, req AliasCreateRequest) (*models.ModelAlias, error) {
	alias := strings.TrimSpace(req.Alias)
	real := strings.TrimSpace(req.RealModel)
	if alias == "" || real == "" || req.GroupID == 0 {
		return nil, app_errors.NewAPIError(app_errors.ErrValidation,
			"alias, group_id and real_model are required")
	}
	row := &models.ModelAlias{
		Alias:     alias,
		GroupID:   req.GroupID,
		RealModel: real,
		Weight:    nonZero(req.Weight, 1),
		Priority:  nonZero(req.Priority, defaultAliasPriority),
		Enabled:   true,
	}
	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return row, nil
}

// AliasUpdateRequest is the JSON payload for PUT /api/aliases/{id}.
//
// group_id / real_model 曾经是不可变的("要改就删了重建")。但那样改一个目标会
// 丢掉行的 id, 前端还得先删后建、中途失败会留一个空窗。现在允许直接改 ——
// 唯一索引 idx_alias_group_model 会挡住改成重复组合, ParseDBError 会把它翻成
// "已存在", 不会静默产生两行 (那会让 SWRR 权重翻倍)。
type AliasUpdateRequest struct {
	GroupID   *uint   `json:"group_id"`
	RealModel *string `json:"real_model"`
	Weight    *int    `json:"weight"`
	Priority  *int    `json:"priority"`
	Enabled   *bool   `json:"enabled"`
}

func (s *AliasService) Update(ctx context.Context, id uint, req AliasUpdateRequest) (*models.ModelAlias, error) {
	var row models.ModelAlias
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	updates := map[string]any{}
	if req.GroupID != nil {
		if *req.GroupID == 0 {
			return nil, app_errors.NewAPIError(app_errors.ErrValidation, "group_id cannot be 0")
		}
		updates["group_id"] = *req.GroupID
	}
	if req.RealModel != nil {
		real := strings.TrimSpace(*req.RealModel)
		if real == "" {
			return nil, app_errors.NewAPIError(app_errors.ErrValidation, "real_model cannot be empty")
		}
		updates["real_model"] = real
	}
	if req.Weight != nil {
		updates["weight"] = *req.Weight
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if len(updates) == 0 {
		return &row, nil
	}
	if err := s.db.WithContext(ctx).Model(&row).Updates(updates).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}
	return &row, nil
}

// AliasCandidateInput 是 PUT /api/aliases/{alias}/candidates 里的一条候选。
type AliasCandidateInput struct {
	GroupID   uint   `json:"group_id"`
	RealModel string `json:"real_model"`
	Weight    int    `json:"weight"`
	Priority  int    `json:"priority"`
	Enabled   *bool  `json:"enabled"`
}

// ReplaceCandidates 用传入的列表**整体替换**某个别名的候选集合。
//
// 语义: 传进来的就是最终想要的集合 —— 不在列表里的 (group_id, real_model) 被删除,
// 已有的更新 weight/priority/enabled, 新的插入。整个过程在一个事务里, 要么全成
// 要么全不成。
//
// 为什么需要它: 前端编辑抽屉是"改完一次性保存"的交互。逐条 create/update/delete
// 会产生 N 个请求, 而且中途失败会留下半改状态 —— 最坏的情况是旧的删掉了、新的没
// 建上, 别名直接失去全部候选。整体替换把这类中间态消掉了。
//
// 注意: 保留别名的占位行 (group_id=0) 不参与替换。
func (s *AliasService) ReplaceCandidates(ctx context.Context, alias string, inputs []AliasCandidateInput) ([]models.ModelAlias, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil, app_errors.NewAPIError(app_errors.ErrValidation, "alias is required")
	}

	var existing []models.ModelAlias
	if err := s.db.WithContext(ctx).
		Where("alias = ? AND group_id <> 0", alias).
		Find(&existing).Error; err != nil {
		return nil, app_errors.ParseDBError(err)
	}

	type candKey struct {
		GroupID   uint
		RealModel string
	}
	// 同一个 (group_id, real_model) 在输入里出现多次时保留最后一条 ——
	// 唯一索引不允许两行, 这里先收敛掉, 免得进了事务才炸。
	want := make(map[candKey]AliasCandidateInput, len(inputs))
	for _, in := range inputs {
		real := strings.TrimSpace(in.RealModel)
		if real == "" || in.GroupID == 0 {
			return nil, app_errors.NewAPIError(app_errors.ErrValidation,
				"each candidate needs a non-zero group_id and a non-empty real_model")
		}
		want[candKey{in.GroupID, real}] = in
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		byKey := make(map[candKey]models.ModelAlias, len(existing))
		for _, row := range existing {
			byKey[candKey{row.GroupID, row.RealModel}] = row
		}
		// 1) 删除目标集合里没有的旧行。
		for k, row := range byKey {
			if _, keep := want[k]; keep {
				continue
			}
			if err := tx.Delete(&models.ModelAlias{}, row.ID).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
		}
		// 2) 已有的更新, 新增的插入。
		for k, in := range want {
			enabled := true
			if in.Enabled != nil {
				enabled = *in.Enabled
			}
			weight := nonZero(in.Weight, 1)
			// 拖拽顺序落库为 priority(从 1 开始), 所以 0 视为未设置。
			priority := nonZero(in.Priority, defaultAliasPriority)
			if row, ok := byKey[k]; ok {
				updates := map[string]any{
					"weight":   weight,
					"priority": priority,
					"enabled":  enabled,
				}
				if err := tx.Model(&models.ModelAlias{}).
					Where("id = ?", row.ID).Updates(updates).Error; err != nil {
					return app_errors.ParseDBError(err)
				}
				continue
			}
			nr := &models.ModelAlias{
				Alias:     alias,
				GroupID:   k.GroupID,
				RealModel: k.RealModel,
				Weight:    weight,
				Priority:  priority,
				Enabled:   enabled,
			}
			if err := tx.Create(nr).Error; err != nil {
				return app_errors.ParseDBError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ListByAlias(ctx, alias)
}

// isReservedAliasName 报告一个名字是否属于智能路由的保留别名
// (simple/medium/complex)。改名要避开它们, 见 RenameAlias 的说明。
func isReservedAliasName(name string) bool {
	for _, r := range ReservedAliases {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

// RenameAlias 把一个别名整体改名 —— 该别名下所有候选行的 alias 字段一起改。
//
// 为什么不能交给调用方逐行 Update: 改名必须是一个整体动作。中途只改了一部分会
// 留下两个半截别名, 而且前端抽屉对"我正在编辑哪个名字"的认知会在瞬间失效。
// 所以放在一个事务里完成。
//
// 三条限制, 每条都是为了避免"看起来成功了、实际把别的东西弄坏了":
//  1. 保留别名不能改名 —— 它们是 auto 智能路由的三档池, 而触发路径是按名字找的
//     (model=="auto" -> 估算 token -> tier -> ReservedAlias(tier))。改了名字等于把
//     自动路由的入口悄悄改掉。
//  2. 也不能改成保留名 —— 那会撞进自动路由的命名空间, 让一个普通别名被当成档位池。
//  3. 目标名若已存在候选行则拒绝 —— 否则两边的候选会合并成一个池, 权重分配莫名变化,
//     而且唯一索引 (alias, group_id, real_model) 会撞车导致整批回滚, 用户只看到失败。
func (s *AliasService) RenameAlias(ctx context.Context, from, to string) (int, error) {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" {
		return 0, app_errors.NewAPIError(app_errors.ErrValidation, "from and to are required")
	}
	if from == to {
		return 0, nil
	}
	if isReservedAliasName(from) {
		return 0, app_errors.NewAPIError(app_errors.ErrValidation, "reserved alias cannot be renamed")
	}
	if isReservedAliasName(to) {
		return 0, app_errors.NewAPIError(app_errors.ErrValidation,
			"cannot rename to a reserved alias name")
	}

	var existing int64
	if err := s.db.WithContext(ctx).Model(&models.ModelAlias{}).
		Where("alias = ? AND group_id <> 0", to).
		Count(&existing).Error; err != nil {
		return 0, app_errors.ParseDBError(err)
	}
	if existing > 0 {
		return 0, app_errors.NewAPIError(app_errors.ErrValidation, "target alias already exists")
	}

	var affected int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.ModelAlias{}).
			Where("alias = ? AND group_id <> 0", from).
			Update("alias", to)
		if res.Error != nil {
			return app_errors.ParseDBError(res.Error)
		}
		affected = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// Delete removes an alias row. Reserved placeholder rows (group_id=0,
// is_reserved=true) cannot be deleted; their members can be removed
// individually instead.
func (s *AliasService) Delete(ctx context.Context, id uint) error {
	var row models.ModelAlias
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	if row.IsReserved && row.GroupID == 0 {
		return fmt.Errorf("reserved alias placeholder cannot be deleted")
	}
	if err := s.db.WithContext(ctx).Delete(&row).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	return nil
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}
