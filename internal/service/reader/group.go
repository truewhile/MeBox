package reader

import (
	"context"
	"fmt"

	"github.com/truewhile/MeBox/internal/model"
)

// 书架分组（对应 legado BookGroup；存储见 model.ReaderBookGroups）。
//
// 语义与影视模块的媒体库标签一致：整份替换、一本书只归一个组、组内顺序即展示顺序。
// 额外的两条收敛放在服务层，因为它们需要知道书籍是否存在：
//   - 只保留该用户书架上的书（书被移出书架后分组里不留死 ID）；
//   - 一本书只归一个组（越靠前的分组优先）。
// 空分组会保留：用户可能就是先建好分组再往里放书。

// GetBookGroups 读用户的书架分组。没有分组时返回空切片（非 nil），方便前端直接遍历。
func (s *ReaderService) GetBookGroups(ctx context.Context, userID string) ([]model.BookGroupSet, error) {
	if userID == "" {
		return []model.BookGroupSet{}, nil
	}
	row, err := s.repo.GetBookGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	groups := model.NormalizeBookGroups(row.DecodeBookGroups())
	if len(groups) == 0 {
		return []model.BookGroupSet{}, nil
	}
	owned, err := s.ownedBookIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	return filterBookGroups(groups, owned), nil
}

// SetBookGroups 覆盖保存用户的书架分组，落库前做过收敛与归属过滤。
func (s *ReaderService) SetBookGroups(ctx context.Context, userID string, groups []model.BookGroupSet) ([]model.BookGroupSet, error) {
	if userID == "" {
		return nil, fmt.Errorf("缺少用户信息")
	}
	owned, err := s.ownedBookIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	normalized := dedupeBookGroups(filterBookGroups(model.NormalizeBookGroups(groups), owned))
	raw, err := model.EncodeBookGroups(normalized)
	if err != nil {
		return nil, err
	}
	if err := s.repo.SaveBookGroups(ctx, &model.ReaderBookGroups{UserID: userID, Groups: raw}); err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return []model.BookGroupSet{}, nil
	}
	return normalized, nil
}

// ownedBookIDs 返回该用户书架上的书籍 ID 集合。
func (s *ReaderService) ownedBookIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	books, err := s.repo.ListBooks(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(books))
	for i := range books {
		out[books[i].ID] = struct{}{}
	}
	return out, nil
}

// filterBookGroups 只保留 owned 里的书籍 ID；空分组原样保留。
func filterBookGroups(groups []model.BookGroupSet, owned map[string]struct{}) []model.BookGroupSet {
	if len(groups) == 0 {
		return nil
	}
	out := make([]model.BookGroupSet, 0, len(groups))
	for _, group := range groups {
		ids := make([]string, 0, len(group.BookIDs))
		for _, id := range group.BookIDs {
			if _, ok := owned[id]; ok {
				ids = append(ids, id)
			}
		}
		out = append(out, model.BookGroupSet{Name: group.Name, BookIDs: ids})
	}
	return out
}

// dedupeBookGroups 收敛成「一本书只归一个组」：越靠前的分组优先，后面的重复项被移除。
func dedupeBookGroups(groups []model.BookGroupSet) []model.BookGroupSet {
	if len(groups) == 0 {
		return nil
	}
	claimed := make(map[string]struct{})
	out := make([]model.BookGroupSet, 0, len(groups))
	for _, group := range groups {
		ids := make([]string, 0, len(group.BookIDs))
		for _, id := range group.BookIDs {
			if _, ok := claimed[id]; ok {
				continue
			}
			claimed[id] = struct{}{}
			ids = append(ids, id)
		}
		out = append(out, model.BookGroupSet{Name: group.Name, BookIDs: ids})
	}
	return out
}
