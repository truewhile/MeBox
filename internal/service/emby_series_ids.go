package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/truewhile/MeBox/internal/model"
)

func (e *EmbyService) seriesIDForMedia(ctx context.Context, m *model.Media) string {
	if strings.TrimSpace(m.SeriesID) != "" {
		return m.SeriesID
	}
	return stableEmbyID(embyVirtualSeriesPrefix, m.LibraryID, e.seriesNameForMedia(ctx, m))
}

func (e *EmbyService) seasonIDForMedia(ctx context.Context, m *model.Media) string {
	return seasonID(e.seriesIDForMedia(ctx, m), embySeasonNumForMedia(m))
}

func (e *EmbyService) seriesNameForMedia(ctx context.Context, m *model.Media) string {
	if strings.TrimSpace(m.SeriesID) != "" {
		// 走请求级缓存；无缓存 ctx 时退化为单次查询。
		if title, ok, err := e.payloadSeriesTitle(ctx, m.SeriesID); err == nil && ok && strings.TrimSpace(title) != "" {
			return title
		}
	}
	if strings.EqualFold(strings.TrimSpace(m.ScrapeStatus), "matched") && strings.TrimSpace(m.Title) != "" {
		name := strings.TrimSpace(m.Title)
		name = embyEpisodeTitleRE.ReplaceAllString(name, "")
		name = embyYearSuffixRE.ReplaceAllString(name, "")
		if name != "" {
			return name
		}
	}
	if name := inferSeriesNameFromPath(m.Path); name != "" {
		return name
	}
	name := strings.TrimSpace(m.Title)
	name = embyEpisodeTitleRE.ReplaceAllString(name, "")
	name = embyYearSuffixRE.ReplaceAllString(name, "")
	if name == "" {
		name = strings.TrimSpace(m.OriginalName)
	}
	return name
}

func inferSeriesNameFromPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	dir := filepath.Dir(path)
	base := filepath.Base(dir)
	if embySeasonDirRE.MatchString(base) {
		dir = filepath.Dir(dir)
		base = filepath.Base(dir)
	} else if stripped := strings.TrimSpace(embySeasonSuffixRE.ReplaceAllString(base, "")); stripped != "" && stripped != base {
		parentDir := filepath.Dir(dir)
		parentBase := filepath.Base(parentDir)
		if parentBase != "." && parentBase != string(filepath.Separator) && !isEmbyGenericContainer(parentBase) {
			dir = parentDir
			base = parentBase
		} else {
			base = stripped
		}
	}
	base = strings.TrimSpace(embyYearSuffixRE.ReplaceAllString(base, ""))
	if base == "." || base == string(filepath.Separator) || isEmbyGenericContainer(base) {
		return ""
	}
	return base
}

func isEmbyGenericContainer(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "movie", "movies", "film", "films", "tv", "series", "show", "shows", "anime", "animation", "variety",
		"电视剧", "剧集", "连续剧", "短剧", "国产剧", "国剧", "欧美剧", "美剧", "英剧", "日韩剧", "日剧", "韩剧", "港剧", "台剧", "港台剧",
		"综艺", "纪录片", "儿童", "动漫", "番剧", "国漫", "日番", "韩漫", "美漫", "欧美动漫", "欧美动画", "其他动漫", "电影", "成人", "未分类",
		"media", "downloads", "download", "videos", "video", "share", "shares":
		return true
	default:
		return false
	}
}

func stableEmbyID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(part))))
		_, _ = h.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(h.Sum(nil))[:32]
}

const (
	embySeasonTheatrical     = -1
	embySeasonOVA            = -2
	embySeasonOAD            = -3
	embySeasonOVD            = -4
	embySeasonONA            = -5
	embySeasonExtra          = -6
	embySeasonBonus          = -7
	embySeasonOmake          = -8
	embySeasonPictureDrama   = -9
	embySeasonNCOP           = -10
	embySeasonNCED           = -11
	embySeasonGenericSpecial = 0
)

// embySeasonNumForMedia keeps MeBox's special categories distinct in the Emby
// hierarchy. Databases may store every special as season 0 or -1, so the path
// classification is authoritative when it identifies a special category.
func embySeasonNumForMedia(m *model.Media) int {
	if m == nil {
		return 0
	}
	switch mediaSpecialKind(m.Path) {
	case mediaSpecialTheatrical:
		return embySeasonTheatrical
	case mediaSpecialOVA:
		return embySeasonOVA
	case mediaSpecialOAD:
		return embySeasonOAD
	case mediaSpecialOVD:
		return embySeasonOVD
	case mediaSpecialONA:
		return embySeasonONA
	case mediaSpecialExtra:
		return embySeasonExtra
	case mediaSpecialBonus:
		return embySeasonBonus
	case mediaSpecialOmake:
		return embySeasonOmake
	case mediaSpecialPicture:
		return embySeasonPictureDrama
	case mediaSpecialNCOP:
		return embySeasonNCOP
	case mediaSpecialNCED:
		return embySeasonNCED
	case mediaSpecialGeneric:
		return embySeasonGenericSpecial
	}
	if m.SeasonNum < 0 {
		return embySeasonGenericSpecial
	}
	return m.SeasonNum
}

func embySeasonCandidates(seasonNum int) []int {
	if seasonNum > 0 {
		return []int{seasonNum}
	}
	return []int{
		embySeasonGenericSpecial,
		embySeasonTheatrical,
		embySeasonOVA,
		embySeasonOAD,
		embySeasonOVD,
		embySeasonONA,
		embySeasonExtra,
		embySeasonBonus,
		embySeasonOmake,
		embySeasonPictureDrama,
		embySeasonNCOP,
		embySeasonNCED,
	}
}

func seasonID(seriesID string, seasonNum int) string {
	return stableEmbyID(embyVirtualSeasonPrefix, seriesID, strconv.Itoa(seasonNum))
}

func seasonName(seasonNum int) string {
	switch seasonNum {
	case embySeasonGenericSpecial:
		return "特别篇"
	case embySeasonTheatrical:
		return "剧场版"
	case embySeasonOVA:
		return "OVA"
	case embySeasonOAD:
		return "OAD"
	case embySeasonOVD:
		return "OVD"
	case embySeasonONA:
		return "ONA"
	case embySeasonExtra:
		return "Extra"
	case embySeasonBonus:
		return "Bonus"
	case embySeasonOmake:
		return "Omake"
	case embySeasonPictureDrama:
		return "Picture Drama"
	case embySeasonNCOP:
		return "NCOP"
	case embySeasonNCED:
		return "NCED"
	}
	return fmt.Sprintf("第 %d 季", seasonNum)
}

func sortSeriesGroups(groups []embySeriesGroup, p ItemsParams) {
	switch primarySupportedEmbySort(p.SortBy, false) {
	case "sortname", "name":
		sort.SliceStable(groups, func(i, j int) bool {
			if strings.EqualFold(p.SortOrder, "Descending") {
				return groups[i].Name > groups[j].Name
			}
			return groups[i].Name < groups[j].Name
		})
		case "datecreated":
			sort.SliceStable(groups, func(i, j int) bool {
				if strings.EqualFold(p.SortOrder, "Ascending") {
					return groups[i].CreatedAt.Before(groups[j].CreatedAt)
				}
				return groups[i].CreatedAt.After(groups[j].CreatedAt)
			})
		case "datelastmediaadded", "datelastcontentadded":
			sort.SliceStable(groups, func(i, j int) bool {
				tI := groups[i].DateLastMediaAdded
				if tI.IsZero() {
					tI = groups[i].CreatedAt
				}
				tJ := groups[j].DateLastMediaAdded
				if tJ.IsZero() {
					tJ = groups[j].CreatedAt
				}
				if strings.EqualFold(p.SortOrder, "Ascending") {
					return tI.Before(tJ)
				}
				return tI.After(tJ)
			})
	default:
		sort.SliceStable(groups, func(i, j int) bool {
			if strings.EqualFold(p.SortOrder, "Ascending") {
				return embySeriesReleaseSortTime(groups[i]).Before(embySeriesReleaseSortTime(groups[j]))
			}
			return embySeriesReleaseSortTime(groups[i]).After(embySeriesReleaseSortTime(groups[j]))
		})
	}
}

func embySeriesReleaseSortTime(group embySeriesGroup) time.Time {
	return mediaReleaseSortTime(model.Media{
		ReleaseDate: group.ReleaseDate,
		Year:        group.Year,
		Base: model.Base{
			CreatedAt: group.CreatedAt,
			UpdatedAt: group.CreatedAt,
		},
	})
}
