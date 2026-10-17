package repository

import (
	"context"

	"github.com/truewhile/MeBox/internal/model"
)

// MediaSeriesIndexColumns 是解析剧集归组 key 所需的全部列。
//
// 剧集归组（首页「最近添加」、媒体库卡片）必须看到整库每一行才能算出正确的
// key 和集数，因此不能只用 SQL 顶端的几行。但整行扫描会把 overview、genres、
// strm_url 这些大文本列一并读进来再丢掉：7k 行规模下驱动层的逐列 bind 占了
// 绝大部分耗时（实测 ~60%）。这里只取归组真正用到的列，卡片代表行在选出
// 前 N 个剧集后按 id 再补一次完整行。
const MediaSeriesIndexColumns = "" +
	"id, created_at, updated_at, library_id, library_root_id, series_id, " +
	"title, original_name, path, poster_url, backdrop_url, " +
	"year, release_date, season_num, episode_num, episode_fraction, " +
	"tm_db_id, bangumi_id, douban_id, thetvdb_id, " +
	"width, height, size_bytes, video_codec, audio_codec, container, nsfw"

// ListMediaSeriesIndex 按 created_at 倒序返回可见媒体的「归组投影」。
//
// 与 SearchFiltered 的区别：
//   - 不做 COUNT(*)（调用方不需要总数，7k 行时这一条 SQL 白跑一遍全表）；
//   - 只 SELECT 归组所需列，避免大文本列的解码与内存拷贝。
func (r *MediaRepository) ListMediaSeriesIndex(ctx context.Context, limit int, filter MediaQueryFilter) ([]model.Media, error) {
	var items []model.Media
	q := r.db.WithContext(ctx).Model(&model.Media{}).
		Select(MediaSeriesIndexColumns).
		Order("created_at desc")
	q = applyMediaQueryFilter(q, filter)
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListMediaByIDs 按 ID 批量取完整行（用于补齐剧集卡片的代表行）。
func (r *MediaRepository) ListMediaByIDs(ctx context.Context, ids []string) ([]model.Media, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var items []model.Media
	if err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
